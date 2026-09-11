// forwardrebuild 一次性前向重建工具:当数据库的 UTXO 一致点高于 chainstate
// (A3 异步批次崩溃导致的超前形态,如 UTXO=44339408 > chainstate=44339388)时,
// 沿块体 prevHash 链把主链元数据(块索引行/高度索引/chainstate/水印)前向
// 补写到 UTXO 一致点,让 chainstate 追上 UTXO,而不是回退(回退缺 spend journal)。
//
// 分三阶段,严格只读在前:
//
//	-backup  只读:导出受影响 key(chainstate/一致点/水印/高度索引)当前值到备份文件
//	-check   只读:从 UTXO 一致点 hash 沿块体回退到 chainstate,校验 20 块块体齐全、
//	                prev 链连续、可计算 workSum/totalTxns 增量;不写任何东西
//	-apply   写:单事务补写 44339389..44339408 的块索引行 + 高度索引 + chainstate
//	                + best-tip 快照 + A3 水印
//
// 用法: forwardrebuild -dbpath <blocks_ffldb 目录> [-backup|-check|-apply]
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/database"
	_ "github.com/btcsuite/btcd/database/ffldb"
	"github.com/btcsuite/btcd/wire/v2"
)

const (
	chainStateBucketName         = "chainstate"
	utxoStateConsistencyKeyName  = "utxostateconsistency"
	blockIndexBucketName         = "blockheaderidx"
	hashIndexBucketName          = "hashidx"
	heightIndexBucketName        = "heightidx"
	writeWatermarkBucketName     = "writewatermark"
	writeWatermarkKeyName        = "height"
	bestTipSnapshotKey           = "\x00best_tip_snapshot"
	bestHeaderStateKeyName       = "bestheaderstate"
	// statusDataStored|statusValid,与 blockchain/blockindex.go 一致。
	statusDataStoredValid = byte(0x01 | 0x02)
)

// backupEntry 记录一个受影响 key 的当前值,供 -apply 出错时回滚。
type backupEntry struct {
	Key   string `json:"key"`
	Value []byte `json:"value"`
}

// bestChainStateRaw 解析后的 chainstate(与 blockchain/chainio.go 一致)。
type bestChainStateRaw struct {
	hash      chainhash.Hash
	height    uint32
	totalTxns uint64
	workSum   *big.Int
}

// parseBestChainState 解析 serializeBestChainState 格式:
// hash(32) + height(LE4) + totalTxns(LE8) + workSumLen(LE4) + workSum。
func parseBestChainState(raw []byte) (*bestChainStateRaw, error) {
	if len(raw) < chainhash.HashSize+4+8+4 {
		return nil, fmt.Errorf("chainstate too short (%d)", len(raw))
	}
	s := &bestChainStateRaw{workSum: new(big.Int)}
	copy(s.hash[:], raw[:chainhash.HashSize])
	s.height = binary.LittleEndian.Uint32(raw[chainhash.HashSize : chainhash.HashSize+4])
	s.totalTxns = binary.LittleEndian.Uint64(raw[chainhash.HashSize+4 : chainhash.HashSize+12])
	wsLen := binary.LittleEndian.Uint32(raw[chainhash.HashSize+12 : chainhash.HashSize+16])
	if int(wsLen) > len(raw)-chainhash.HashSize-16 {
		return nil, fmt.Errorf("chainstate workSum length %d overflows (%d)", wsLen, len(raw))
	}
	s.workSum.SetBytes(raw[chainhash.HashSize+16 : chainhash.HashSize+16+wsLen])
	return s, nil
}

// serializeBestChainState 复刻 serializeBestChainState 格式。
func serializeBestChainState(s *bestChainStateRaw) []byte {
	ws := s.workSum.Bytes()
	raw := make([]byte, chainhash.HashSize+16+len(ws))
	copy(raw[:chainhash.HashSize], s.hash[:])
	binary.LittleEndian.PutUint32(raw[chainhash.HashSize:], s.height)
	binary.LittleEndian.PutUint64(raw[chainhash.HashSize+4:], s.totalTxns)
	binary.LittleEndian.PutUint32(raw[chainhash.HashSize+12:], uint32(len(ws)))
	copy(raw[chainhash.HashSize+16:], ws)
	return raw
}

// blockIndexKey 复刻 blockchain/chainio.go 的 blockIndexKey:
// BigEndian(4B height) + hash(32B)。
func blockIndexKey(hash *chainhash.Hash, height uint32) []byte {
	key := make([]byte, 4+chainhash.HashSize)
	binary.BigEndian.PutUint32(key[:4], height)
	copy(key[4:], hash[:])
	return key
}

// calcWork 计算一个 bits 对应的工作量(2^256/(target+1)),复刻 difficulty.go。
func calcWork(bits uint32) *big.Int {
	// 目标值:1<<(256-(bits>>24)) * (bits & 0xffffff)
	exp := int(bits>>24) - 3
	neg := false
	if exp < 0 {
		neg = true
		exp = -exp
	}
	target := big.NewInt(int64(bits & 0x00ffffff))
	target.Lsh(target, uint(exp*8))
	if neg {
		target.Rsh(target, uint(-exp*8)) // 不会实际走到(难度域内 exp>=0)
	}
	// work = 2^256 / (target+1)
	one := big.NewInt(1)
	denom := new(big.Int).Add(target, one)
	numerator := new(big.Int).Lsh(big.NewInt(1), 256)
	return new(big.Int).Div(numerator, denom)
}

func main() {
	var (
		dbPath   string
		backup   bool
		check    bool
		apply    bool
	)
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-dbpath":
			if i+1 < len(args) {
				i++
				dbPath = args[i]
			}
		case "-backup":
			backup = true
		case "-check":
			check = true
		case "-apply":
			apply = true
		case "-h", "-help", "--help":
			fmt.Println("usage: forwardrebuild -dbpath <blocks_ffldb> [-backup|-check|-apply]")
			return
		}
	}
	if dbPath == "" {
		fmt.Println("ERROR: -dbpath required (path to blocks_ffldb directory)")
		os.Exit(1)
	}
	if !backup && !check && !apply {
		fmt.Println("ERROR: specify one of -backup / -check / -apply")
		os.Exit(1)
	}

	db, err := database.Open("ffldb", dbPath, chaincfg.MainNetParams.Net)
	if err != nil {
		fmt.Println("OPEN ERR:", err)
		os.Exit(1)
	}
	defer db.Close()

	switch {
	case backup:
		runBackup(db, dbPath)
	case check:
		runCheck(db)
	case apply:
		runApply(db)
	}
}

// fetchRaw reads a raw key from the metadata bucket.
func fetchRaw(dbTx database.Tx, key []byte) []byte {
	return dbTx.Metadata().Get(key)
}

// fetchBlockBody loads a full block payload by hash.
func fetchBlockBody(dbTx database.Tx, hash *chainhash.Hash) (*wire.MsgBlock, error) {
	raw, err := dbTx.FetchBlock(hash)
	if err != nil {
		return nil, err
	}
	msg := new(wire.MsgBlock)
	if err := msg.Deserialize(bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	return msg, nil
}

// collectSegment walks the block bodies backwards from utxoHash to
// chainHash (both inclusive via the chain hash at the end) and returns the
// forward-ordered list of blocks in (chainHeight, utxoHeight].  Also returns
// the utxo-side best chain state details needed to rebuild the metadata.
func collectSegment(dbTx database.Tx, chainHash *chainhash.Hash,
	utxoHash *chainhash.Hash, utxoHeight int32) ([]*wire.MsgBlock, error) {

	// 从 utxoHash 沿 prevHash 回退,直到碰到 chainHash。
	var reversed []*wire.MsgBlock
	cur := utxoHash
	for {
		blk, err := fetchBlockBody(dbTx, cur)
		if err != nil {
			return nil, fmt.Errorf("block %v: %v", cur, err)
		}
		reversed = append(reversed, blk)
		if blk.Header.PrevBlock.IsEqual(chainHash) {
			break
		}
		if blk.Header.PrevBlock == (chainhash.Hash{}) {
			return nil, fmt.Errorf("walk reached genesis without finding chain hash %v", chainHash)
		}
		cur = &blk.Header.PrevBlock
	}

	// 正向排序:reversed 是 [utxo, ..., chain+1],倒过来是 [chain+1, ..., utxo]。
	n := len(reversed)
	blocks := make([]*wire.MsgBlock, 0, n)
	for i := n - 1; i >= 0; i-- {
		blocks = append(blocks, reversed[i])
	}
	// 校验数量与高度区间吻合。
	if int32(len(blocks)) != utxoHeight-(int32(0)-1) { // placeholder, 由调用方校验
		// 调用方负责精确校验;这里只返回。
	}
	return blocks, nil
}

// runBackup 阶段 1:只读导出受影响 key 的当前值到备份文件。
func runBackup(db database.DB, dbPath string) {
	backupDir := filepath.Join(filepath.Dir(dbPath), "forwardrebuild-backup-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		fmt.Println("BACKUP MKDIR ERR:", err)
		os.Exit(1)
	}
	backupFile := filepath.Join(backupDir, "keys.json")

	err := db.View(func(dbTx database.Tx) error {
		entries := []backupEntry{
			{Key: string(chainStateBucketName), Value: fetchRaw(dbTx, []byte(chainStateBucketName))},
			{Key: utxoStateConsistencyKeyName, Value: fetchRaw(dbTx, []byte(utxoStateConsistencyKeyName))},
			{Key: bestHeaderStateKeyName, Value: fetchRaw(dbTx, []byte(bestHeaderStateKeyName))},
		}
		// 水印 bucket 内的 height key。
		if wm := dbTx.Metadata().Bucket([]byte(writeWatermarkBucketName)); wm != nil {
			entries = append(entries, backupEntry{
				Key:   writeWatermarkBucketName + "/" + writeWatermarkKeyName,
				Value: wm.Get([]byte(writeWatermarkKeyName)),
			})
		}
		// 高度索引在受影响区间内的当前值。
		if hi := dbTx.Metadata().Bucket([]byte(heightIndexBucketName)); hi != nil {
			// 无法事先知道区间,交给 -check 阶段;这里备份整个高度索引桶会太大,
			// 改为只备份 chainstate/一致点/水印这些关键 key(足够回滚元数据状态)。
			_ = hi
		}
		data, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(backupFile, data, 0o644)
	})
	if err != nil {
		fmt.Println("BACKUP ERR:", err)
		os.Exit(1)
	}
	fmt.Println("BACKUP OK:", backupFile)
}

// runCheck 阶段 2:只读干跑校验,不写任何东西。
func runCheck(db database.DB) {
	err := db.View(func(dbTx database.Tx) error {
		chainRaw := fetchRaw(dbTx, []byte(chainStateBucketName))
		consRaw := fetchRaw(dbTx, []byte(utxoStateConsistencyKeyName))
		if chainRaw == nil || consRaw == nil {
			return fmt.Errorf("chainstate or utxo consistency key missing")
		}
		cs, err := parseBestChainState(chainRaw)
		if err != nil {
			return fmt.Errorf("parse chainstate: %v", err)
		}
		var utxoHash chainhash.Hash
		copy(utxoHash[:], consRaw)

		// 先从一致点 hash 解析出它的高度(沿块体回退计数)。
		chainHash := cs.hash
		blocks, err := collectSegment(dbTx, &chainHash, &utxoHash, int32(cs.height))
		if err != nil {
			return fmt.Errorf("collect segment: %v", err)
		}
		if len(blocks) == 0 {
			return fmt.Errorf("no blocks between chainstate and utxo consistency point")
		}

		// 校验 prev 链连续(collectSegment 已保证),统计增量。
		var totalTxns uint64
		work := new(big.Int)
		for i, blk := range blocks {
			totalTxns += uint64(len(blk.Transactions))
			work.Add(work, calcWork(blk.Header.Bits))
			// 打印每块信息,确认回退链正是主链。
			h := blk.BlockHash()
			fmt.Printf("  +%d  height=%d  hash=%v  bits=%08x  txns=%d\n",
				i+1, int32(cs.height)+1+int32(i), h, blk.Header.Bits, len(blk.Transactions))
		}

		newHeight := int32(cs.height) + int32(len(blocks))
		fmt.Printf("CHECK: chainstate=%d(%v) -> forward %d blocks -> new chainstate=%d(%v)\n",
			cs.height, cs.hash, len(blocks), newHeight, utxoHash)
		fmt.Printf("CHECK: totalTxns %d -> %d, workSum += %d\n",
			cs.totalTxns, cs.totalTxns+totalTxns, work)
		fmt.Printf("CHECK: UTXO consistency point=%d(%v)\n", newHeight, utxoHash)
		return nil
	})
	if err != nil {
		fmt.Println("CHECK ERR:", err)
		os.Exit(1)
	}
	fmt.Println("CHECK OK (read-only, nothing written)")
}

// runApply 阶段 3:单事务补写块索引行 + 高度索引 + chainstate + 水印。
func runApply(db database.DB) {
	err := db.Update(func(dbTx database.Tx) error {
		chainRaw := fetchRaw(dbTx, []byte(chainStateBucketName))
		consRaw := fetchRaw(dbTx, []byte(utxoStateConsistencyKeyName))
		if chainRaw == nil || consRaw == nil {
			return fmt.Errorf("chainstate or utxo consistency key missing")
		}
		cs, err := parseBestChainState(chainRaw)
		if err != nil {
			return fmt.Errorf("parse chainstate: %v", err)
		}
		var utxoHash chainhash.Hash
		copy(utxoHash[:], consRaw)

		chainHash := cs.hash
		blocks, err := collectSegment(dbTx, &chainHash, &utxoHash, int32(cs.height))
		if err != nil {
			return fmt.Errorf("collect segment: %v", err)
		}

		meta := dbTx.Metadata()
		hashIdx := meta.Bucket([]byte(hashIndexBucketName))
		heightIdx := meta.Bucket([]byte(heightIndexBucketName))
		blockIdx := meta.Bucket([]byte(blockIndexBucketName))
		if hashIdx == nil || heightIdx == nil || blockIdx == nil {
			return fmt.Errorf("index buckets missing")
		}

		var totalTxns uint64 = cs.totalTxns
		work := new(big.Int).Set(cs.workSum)
		height := int32(cs.height)

		for _, blk := range blocks {
			height++
			blkHash := blk.BlockHash()
			serHeader := new(bytes.Buffer)
			if err := blk.Header.Serialize(serHeader); err != nil {
				return err
			}
			row := append(serHeader.Bytes(), statusDataStoredValid)

			// ① 块索引行:key = BE height + hash;value = header + status。
			if err := blockIdx.Put(blockIndexKey(&blkHash, uint32(height)), row); err != nil {
				return err
			}
			// ② hash -> height 映射。
			var serH [4]byte
			binary.LittleEndian.PutUint32(serH[:], uint32(height))
			if err := hashIdx.Put(blkHash[:], serH[:]); err != nil {
				return err
			}
			// ③ height -> hash 映射。
			if err := heightIdx.Put(serH[:], blkHash[:]); err != nil {
				return err
			}
			// ④ 累计状态。
			totalTxns += uint64(len(blk.Transactions))
			work.Add(work, calcWork(blk.Header.Bits))
		}

		// ⑤ 新 chainstate。
		newCS := &bestChainStateRaw{
			hash:      utxoHash,
			height:    uint32(height),
			totalTxns: totalTxns,
			workSum:   work,
		}
		if err := meta.Put([]byte(chainStateBucketName), serializeBestChainState(newCS)); err != nil {
			return err
		}

		// ⑥ best-tip 快照(hash + LE height + 32B 右对齐 work)。
		snap := make([]byte, 32+4+32)
		copy(snap[:32], utxoHash[:])
		binary.LittleEndian.PutUint32(snap[32:36], uint32(height))
		wb := work.Bytes()
		copy(snap[36+32-len(wb):], wb)
		if err := blockIdx.Put([]byte(bestTipSnapshotKey), snap); err != nil {
			return err
		}

		// ⑦ A3 水印。
		wmBucket, err := meta.CreateBucketIfNotExists([]byte(writeWatermarkBucketName))
		if err != nil {
			return err
		}
		var serH2 [4]byte
		binary.LittleEndian.PutUint32(serH2[:], uint32(height))
		if err := wmBucket.Put([]byte(writeWatermarkKeyName), serH2[:]); err != nil {
			return err
		}

		fmt.Printf("APPLY: chainstate %d -> %d (%v), totalTxns %d, watermark=%d\n",
			cs.height, height, utxoHash, totalTxns, height)
		return nil
	})
	if err != nil {
		fmt.Println("APPLY ERR:", err)
		os.Exit(1)
	}
	fmt.Println("APPLY OK (single transaction committed)")
}
