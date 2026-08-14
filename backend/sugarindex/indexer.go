package sugarindex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/database"
	"github.com/btcsuite/btcd/wire/v2"
	"github.com/btcsuite/btclog"
	"github.com/syndtr/goleveldb/leveldb"
)

// log 是包级日志器,默认禁用(与 indexers/log.go 惯例一致)。
// log is the package logger, disabled by default (mirrors indexers/log.go).
var log btclog.Logger

// DisableLog 禁用包日志。DisableLog disables package logging.
func DisableLog() { log = btclog.Disabled }

// UseLogger 设置包日志器。UseLogger sets the package logger.
func UseLogger(logger btclog.Logger) { log = logger }

// Manager 持有一个独立的 raw LevelDB(位于节点 datadir 下的 index/),以与 umami
// 逐字节兼容的格式维护地址/花费/时间戳索引,并实现 blockchain.IndexManager。
//
// Manager owns a standalone raw LevelDB (index/ under the node datadir) that
// mirrors the umami address/spent/timestamp index byte-for-byte, and implements
// blockchain.IndexManager.
type Manager struct {
	db   *leveldb.DB
	key  []byte // 8 字节混淆密钥
	path string
}

// Ensure Manager satisfies the blockchain.IndexManager interface.
var _ blockchain.IndexManager = (*Manager)(nil)

// NewManager 打开(必要时创建)sugar index 的 LevelDB。
// NewManager opens (creating if needed) the sugar index LevelDB.
func NewManager(path string) (*Manager, error) {
	ldb, key, err := openIndexDB(path, log)
	if err != nil {
		return nil, err
	}
	return &Manager{db: ldb, key: key, path: path}, nil
}

// Close 关闭底层 LevelDB。Close the underlying LevelDB.
func (m *Manager) Close() error { return m.db.Close() }

// DB 返回底层 LevelDB(供 RPC 层读取)。Returns the underlying LevelDB.
func (m *Manager) DB() *leveldb.DB { return m.db }

// ---------------------------------------------------------------------------
// blockchain.IndexManager interface

// Init 追上当前主链尖点;若本地尖点是孤儿(索引停用期间发生过重组)则整体重建。
// Init catches the index up to the current tip, rebuilding from scratch when
// the local tip became an orphan (reorg while the index was disabled).
func (m *Manager) Init(chain *blockchain.BlockChain,
	interrupt <-chan struct{}) error {

	tipHash, tipHeight, err := m.fetchIndexTip()
	if err != nil {
		return err
	}

	// MainChainHasBlock only consults the in-memory index, which evicts the
	// tip after a restart (the tip sits far below the header window).  Use
	// the database-backed height lookup instead so a valid tip is not
	// falsely declared orphaned, which would wipe and rebuild the whole
	// index from scratch (~30h on 43.8M blocks).
	// MainChainHasBlock 只查内存索引,重启后 tip 远低于窗口会被驱逐;改用
	// 走数据库的高度查询,避免把合法 tip 误判为孤儿而整库重来。
	if tipHash != nil {
		mainHeight, herr := chain.BlockHeightByHash(tipHash)
		if herr != nil || mainHeight != tipHeight {
			log.Warnf("Sugar index tip %v is orphaned; rebuilding from scratch",
				tipHash)
			if err := m.wipeIndex(); err != nil {
				return err
			}
			tipHeight = -1
		}
	}

	bestHeight := chain.BestSnapshot().Height
	if tipHeight >= bestHeight {
		return nil
	}

	// Rebuild batching: accumulate up to rebuildBatchBlocks blocks into one
	// leveldb batch before writing, cutting the number of write transactions
	// (and fsyncs) ~100x during the initial catch-up.  The tip is stored with
	// each flush so an interrupted rebuild resumes from the last flushed
	// height instead of redoing the whole chain.
	// 批量重建:每 rebuildBatchBlocks 块合并为一次 leveldb 写入,将初始追赶
	// 期间的写事务(及 fsync)次数降低约 100 倍;每次落盘同时记录尖点,中断后
	// 从上次落盘高度继续,无需整链重来。
	const rebuildBatchBlocks = 100

	log.Infof("Sugar index catching up from height %d to %d",
		tipHeight+1, bestHeight)

	// Parallel read-ahead.  BlockByHeight + FetchSpendJournal are the rebuild
	// bottleneck (random reads from the main DB), so run several workers to
	// fetch blocks/spent-journals ahead of the writer, then merge results in
	// height order so the write side stays strictly sequential.
	// 并行预读:主库随机读(BlockByHeight+FetchSpendJournal)是重建瓶颈,用多个
	// worker 提前读取,再按高度顺序合并,写入保持串行。
	const rebuildReadWorkers = 4

	type fetchResult struct {
		height int32
		block  *btcutil.Block
		stxos  []blockchain.SpentTxOut
		err    error
	}

	heights := make(chan int32, rebuildReadWorkers*2)
	results := make(chan fetchResult, rebuildReadWorkers*2)
	var wg sync.WaitGroup
	for i := 0; i < rebuildReadWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Batch spend-journal reads: fetch up to fetchBatchBlocks blocks
			// per database view so the per-block read-transaction overhead is
			// amortized (BlockByHeight stays per-block, but the journal reads
			// dominate).  Results are emitted per block; the consumer merges
			// them in height order, so out-of-order emission is fine.
			// 批量读 spend journal:每 fetchBatchBlocks 块一次数据库视图读取,
			// 摊薄每块读事务开销;结果仍按块发出,由消费端按高度顺序合并。
			const fetchBatchBlocks = 32
			var blocks []*btcutil.Block
			var blockHeights []int32

			flush := func() error {
				if len(blocks) == 0 {
					return nil
				}
				journals, err := chain.FetchSpendJournals(blocks)
				if err != nil {
					for _, h := range blockHeights {
						results <- fetchResult{height: h, err: err}
					}
				} else {
					for i, h := range blockHeights {
						results <- fetchResult{
							height: h, block: blocks[i], stxos: journals[i],
						}
					}
				}
				blocks = blocks[:0]
				blockHeights = blockHeights[:0]
				return nil
			}

			for height := range heights {
				if interruptRequested(interrupt) {
					return
				}
				block, err := chain.BlockByHeight(height)
				if err != nil {
					results <- fetchResult{height: height, err: err}
					continue
				}
				blocks = append(blocks, block)
				blockHeights = append(blockHeights, height)
				if len(blocks) >= fetchBatchBlocks {
					if err := flush(); err != nil {
						return
					}
				}
			}
			_ = flush()
		}()
	}

	// Producer: feed the height range to the workers, stopping on interrupt.
	go func() {
		defer close(heights)
		for height := tipHeight + 1; height <= bestHeight; height++ {
			select {
			case heights <- height:
			case <-interrupt:
				return
			}
		}
	}()
	// Close the results channel once every reader has finished.
	go func() {
		wg.Wait()
		close(results)
	}()

	batch := new(leveldb.Batch)
	batched := 0
	next := tipHeight + 1
	pending := make(map[int32]fetchResult)
	for r := range results {
		if r.err != nil {
			return r.err
		}
		pending[r.height] = r

		// Consume strictly in height order.
		for {
			cur, ok := pending[next]
			if !ok {
				break
			}
			if err := m.connectBlockBatch(cur.block, cur.stxos, batch); err != nil {
				return err
			}
			batched++
			if batched >= rebuildBatchBlocks || next == bestHeight {
				m.storeIndexTip(batch, cur.block.Hash(), next)
				if err := m.db.Write(batch, nil); err != nil {
					return err
				}
				batch = new(leveldb.Batch)
				batched = 0
			}
			if next%10000 == 0 {
				log.Infof("Sugar index: indexed height %d", next)
				m.writeProgress(next, bestHeight)
			}
			delete(pending, next)
			next++
		}
	}
	if interruptRequested(interrupt) {
		return errInterruptRequested
	}
	log.Infof("Sugar index caught up to height %d", bestHeight)
	return nil
}

// ConnectBlock writes the index entries for a newly connected block.  The
// database.Tx of btcd's core DB is ignored: the sugar index lives in its own
// raw LevelDB.
func (m *Manager) ConnectBlock(_ database.Tx, block *btcutil.Block,
	stxos []blockchain.SpentTxOut) error {
	return m.connectBlock(block, stxos)
}

// DisconnectBlock reverses the entries written by ConnectBlock.
func (m *Manager) DisconnectBlock(_ database.Tx, block *btcutil.Block,
	stxos []blockchain.SpentTxOut) error {
	return m.disconnectBlock(block, stxos)
}

// ---------------------------------------------------------------------------
// index computation

// connectBlock 索引一个新连接区块:输出写 +delta 与新增 unspent,输入写 -delta、
// 删除 unspent 并写入 spent-index,最后写时间戳与尖点。
//
// connectBlock indexes a freshly connected block: outputs get positive deltas
// and fresh unspents, inputs get negative deltas, spent unspents are removed
// and spent-index entries added, plus timestamp and tip entries.
func (m *Manager) connectBlock(block *btcutil.Block,
	stxos []blockchain.SpentTxOut) error {

	batch := new(leveldb.Batch)
	if err := m.connectBlockBatch(block, stxos, batch); err != nil {
		return err
	}
	m.storeIndexTip(batch, block.Hash(), block.Height())
	return m.db.Write(batch, nil)
}

// connectBlockBatch 填充 batch 但不写库,供 Init 批量重建与单块 connectBlock
// 共用。connectBlockBatch fills the batch without writing, shared by the
// batched rebuild in Init and the single-block connectBlock.
func (m *Manager) connectBlockBatch(block *btcutil.Block,
	stxos []blockchain.SpentTxOut, batch *leveldb.Batch) error {

	bd := newBlockDeltas()
	height := block.Height()
	stxoIndex := 0

	for txIdx, tx := range block.Transactions() {
		msgTx := tx.MsgTx()
		txHash := msgTx.TxHash()

		// The coinbase (txIdx 0) has no entries in the spend journal.
		if txIdx != 0 {
			for inIdx := range msgTx.TxIn {
				stxo := stxos[stxoIndex]
				bd.addSpent(msgTx.TxIn[inIdx], stxo, txHash, height,
					uint32(txIdx), uint32(inIdx))
				stxoIndex++
			}
		}

		for vIdx, txOut := range msgTx.TxOut {
			bd.addReceived(txHash, txOut, height, uint32(txIdx),
				uint32(vIdx))
		}
	}

	for _, e := range bd.addressIndex {
		ev := &enc{}
		ev.i64(e.delta)
		m.putObfuscated(batch, e.key, ev.bytes())
	}
	for _, u := range bd.addressUnspent {
		if u.isNull {
			batch.Delete(u.key)
		} else {
			m.putObfuscated(batch, u.key, u.encode())
		}
	}
	for _, s := range bd.spentIndex {
		m.putObfuscated(batch, s.key, s.encode())
	}

	tk := &TimestampIndexKey{
		Timestamp: uint32(block.MsgBlock().Header.Timestamp.Unix()),
		BlockHash: *block.Hash(),
	}
	m.putObfuscated(batch, tk.Key(), []byte{0})

	return nil
}

// disconnectBlock 撤销 connectBlock:删除该区块产生的全部 address deltas 与
// 新 unspent,恢复被花费的 unspent,删除 spent-index 条目。
//
// disconnectBlock reverses connectBlock: it erases all address deltas and the
// freshly created unspents of the block, restores the spent unspents, and
// erases the spent-index entries.
func (m *Manager) disconnectBlock(block *btcutil.Block,
	stxos []blockchain.SpentTxOut) error {

	bd := newBlockDeltas()
	height := block.Height()
	stxoIndex := 0

	for txIdx, tx := range block.Transactions() {
		msgTx := tx.MsgTx()
		txHash := msgTx.TxHash()

		if txIdx != 0 {
			for inIdx := range msgTx.TxIn {
				stxo := stxos[stxoIndex]
				bd.undoSpent(msgTx.TxIn[inIdx], stxo, txHash, height,
					uint32(txIdx), uint32(inIdx))
				stxoIndex++
			}
		}

		for vIdx, txOut := range msgTx.TxOut {
			bd.undoReceived(txHash, txOut, height, uint32(txIdx),
				uint32(vIdx))
		}
	}

	batch := new(leveldb.Batch)

	// AddressIndex: erase everything this block wrote.
	for _, e := range bd.addressErase {
		batch.Delete(e.key)
	}
	// AddressUnspent: erase the outputs, restore the inputs.
	for _, u := range bd.addressUnspent {
		if u.isNull {
			batch.Delete(u.key)
		} else {
			m.putObfuscated(batch, u.key, u.encode())
		}
	}
	// SpentIndex: erase.
	for _, sk := range bd.spentErase {
		batch.Delete((&SpentIndexKey{
			TxID:        sk.txID,
			OutputIndex: sk.outN,
		}).Key())
	}

	m.storeIndexTip(batch, &block.MsgBlock().Header.PrevBlock, height-1)
	return m.db.Write(batch, nil)
}

// ---------------------------------------------------------------------------
// delta accumulation

// blockDeltas 汇总一个区块产生的全部索引增量。
// blockDeltas accumulates all index deltas produced by one block.
type blockDeltas struct {
	addressIndex   []addrIndexEntry
	addressUnspent []addrUnspentEntry
	spentIndex     []spentIndexEntry
	// disconnect only
	addressErase []addrIndexEntry
	spentErase   []spentIndexKey
}

func newBlockDeltas() *blockDeltas { return &blockDeltas{} }

type addrIndexEntry struct {
	key   []byte
	delta int64
}

type addrUnspentEntry struct {
	key      []byte
	isNull   bool
	satoshis int64
	script   []byte
	height   int32
}

func (u *addrUnspentEntry) encode() []byte {
	return (&AddressUnspentValue{
		Satoshis:    u.satoshis,
		Script:      u.script,
		BlockHeight: u.height,
	}).Encode()
}

type spentIndexEntry struct {
	key           []byte
	txHash        chainhash.Hash
	inIndex       uint32
	blockHeight   int32
	satoshis      int64
	addrType      int32
	addrHashBytes []byte
}

func (s *spentIndexEntry) encode() []byte {
	e := &enc{}
	e.hash(s.txHash)
	e.u32(s.inIndex)
	e.i32(s.blockHeight)
	e.i64(s.satoshis)
	e.i32(s.addrType)
	e.hashIndex(s.addrHashBytes)
	return e.bytes()
}

type spentIndexKey struct {
	txID chainhash.Hash
	outN uint32
}

// pushAddrIndex 追加一条地址增量(连接或断开均可用于构造删除键)。
// pushAddrIndex appends one address delta entry.
func (bd *blockDeltas) pushAddrIndex(erase bool, spending bool,
	scriptType int, hashBytes []byte, height int32, txIdx uint32,
	txHash chainhash.Hash, index uint32, delta int64) {

	k := &AddressIndexKey{
		Type:        uint32(scriptType),
		HashBytes:   hashBytes,
		BlockHeight: height,
		TxIndex:     txIdx,
		TxHash:      txHash,
		Index:       index,
		Spending:    spending,
	}
	entry := addrIndexEntry{key: k.Key(), delta: delta}
	if erase {
		bd.addressErase = append(bd.addressErase, entry)
	} else {
		bd.addressIndex = append(bd.addressIndex, entry)
	}
}

// addReceived 处理一个输出的 +delta 与 unspent 写入。
// addReceived handles one output's positive delta + unspent write.
func (bd *blockDeltas) addReceived(txHash chainhash.Hash, txOut *wire.TxOut,
	height int32, txIdx, vIdx uint32) {

	scriptType, hashBytes := ExtractIndexInfo(txOut.PkScript)
	if scriptType == AddrIndtUnknown {
		return
	}
	bd.pushAddrIndex(false, false, scriptType, hashBytes, height, txIdx,
		txHash, vIdx, txOut.Value)
	bd.addressUnspent = append(bd.addressUnspent, addrUnspentEntry{
		key: (&AddressUnspentKey{
			Type:      uint32(scriptType),
			HashBytes: hashBytes,
			TxHash:    txHash,
			Index:     vIdx,
		}).Key(),
		satoshis: txOut.Value,
		script:   txOut.PkScript,
		height:   height,
	})
}

// addSpent 处理一个输入的 -delta、unspent 删除与 spent-index 写入。
// addSpent handles one input's negative delta, unspent erasure and spent-index
// write.
func (bd *blockDeltas) addSpent(txIn *wire.TxIn,
	stxo blockchain.SpentTxOut, txHash chainhash.Hash, height int32,
	txIdx, inIdx uint32) {

	scriptType, hashBytes := ExtractIndexInfo(stxo.PkScript)
	if scriptType == AddrIndtUnknown {
		return
	}
	prev := &txIn.PreviousOutPoint
	bd.pushAddrIndex(false, true, scriptType, hashBytes, height, txIdx,
		txHash, inIdx, -stxo.Amount)
	bd.addressUnspent = append(bd.addressUnspent, addrUnspentEntry{
		key: (&AddressUnspentKey{
			Type:      uint32(scriptType),
			HashBytes: hashBytes,
			TxHash:    prev.Hash,
			Index:     prev.Index,
		}).Key(),
		isNull: true,
	})
	bd.spentIndex = append(bd.spentIndex, spentIndexEntry{
		key:           (&SpentIndexKey{TxID: prev.Hash, OutputIndex: prev.Index}).Key(),
		txHash:        txHash,
		inIndex:       inIdx,
		blockHeight:   height,
		satoshis:      stxo.Amount,
		addrType:      int32(scriptType),
		addrHashBytes: hashBytes,
	})
}

// undoReceived 断开时:删除该输出的 address delta 与 unspent。
// undoReceived on disconnect: erase the output's delta and unspent.
func (bd *blockDeltas) undoReceived(txHash chainhash.Hash, txOut *wire.TxOut,
	height int32, txIdx, vIdx uint32) {

	scriptType, hashBytes := ExtractIndexInfo(txOut.PkScript)
	if scriptType == AddrIndtUnknown {
		return
	}
	bd.pushAddrIndex(true, false, scriptType, hashBytes, height, txIdx,
		txHash, vIdx, txOut.Value)
	bd.addressUnspent = append(bd.addressUnspent, addrUnspentEntry{
		key: (&AddressUnspentKey{
			Type:      uint32(scriptType),
			HashBytes: hashBytes,
			TxHash:    txHash,
			Index:     vIdx,
		}).Key(),
		isNull: true,
	})
}

// undoSpent 断开时:删除输入的 address delta 与 spent-index,并恢复 unspent。
// undoSpent on disconnect: erase the input's delta and spent-index, restore
// the unspent.
func (bd *blockDeltas) undoSpent(txIn *wire.TxIn,
	stxo blockchain.SpentTxOut, txHash chainhash.Hash, height int32,
	txIdx, inIdx uint32) {

	scriptType, hashBytes := ExtractIndexInfo(stxo.PkScript)
	if scriptType == AddrIndtUnknown {
		return
	}
	prev := &txIn.PreviousOutPoint
	bd.pushAddrIndex(true, true, scriptType, hashBytes, height, txIdx,
		txHash, inIdx, -stxo.Amount)
	bd.addressUnspent = append(bd.addressUnspent, addrUnspentEntry{
		key: (&AddressUnspentKey{
			Type:      uint32(scriptType),
			HashBytes: hashBytes,
			TxHash:    prev.Hash,
			Index:     prev.Index,
		}).Key(),
		satoshis: stxo.Amount,
		script:   stxo.PkScript,
		height:   stxo.Height,
	})
	bd.spentErase = append(bd.spentErase, spentIndexKey{
		txID: prev.Hash,
		outN: prev.Index,
	})
}

// ---------------------------------------------------------------------------
// wipe / interrupt helpers

// writeProgress 把重建进度写为 JSON 到 index 目录下的 progress.json,供前端
// 在 RPC 尚未就绪时轮询显示。writeProgress writes the rebuild progress as
// JSON to progress.json under the index dir so the frontend can poll it while
// the RPC server is still starting.
func (m *Manager) writeProgress(height, total int32) {
	raw, err := json.Marshal(map[string]interface{}{
		"height":  height,
		"total":   total,
		"percent": float64(height) / float64(total) * 100,
	})
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(m.path, "progress.json"), raw, 0o600)
}

// wipeIndex 清空全部四类索引命名空间与尖点标记。
// wipeIndex removes all four index namespaces plus the tip marker.
//
// 键按批次删除,避免把全库键累积进单个 leveldb.Batch 导致内存爆炸
// (43.8M 高度时键数可达数亿,单个 batch 曾使进程内存飙至 ~6GB)。
// Keys are deleted in bounded batches so the whole key set is never
// accumulated in a single leveldb.Batch (at 43.8M blocks the key count
// reaches hundreds of millions; one batch previously ballooned the process
// to ~6GB).
func (m *Manager) wipeIndex() error {
	// wipeBatchKeys bounds the number of deletes per leveldb batch write.
	const wipeBatchKeys = 100000

	iter := m.db.NewIterator(nil, nil)
	batch := new(leveldb.Batch)
	batched := 0
	for iter.Next() {
		k := iter.Key()
		if len(k) > 0 {
			switch k[0] {
			case DBAddressIndex, DBAddressUnspent, DBTimestampIndex,
				DBSpentIndex:
				batch.Delete(append([]byte{}, k...))
				batched++
				if batched >= wipeBatchKeys {
					if err := m.db.Write(batch, nil); err != nil {
						iter.Release()
						return err
					}
					batch = new(leveldb.Batch)
					batched = 0
				}
			}
		}
	}
	iter.Release()
	if err := iter.Error(); err != nil {
		return err
	}

	batch.Delete(indexTipKey)
	return m.db.Write(batch, nil)
}

// interruptRequested reports whether the interrupt channel was closed.
func interruptRequested(interrupt <-chan struct{}) bool {
	select {
	case <-interrupt:
		return true
	default:
		return false
	}
}

// errInterruptRequested indicates a user-requested interrupt.
var errInterruptRequested = fmt.Errorf("interrupt requested")
