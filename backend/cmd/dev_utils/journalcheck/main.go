// journalcheck 只读诊断工具:验证卡住区块的 spend journal 与块体输入数。
// 用法: journalcheck -dbpath <blocks_ffldb 目录>
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/big"
	"os"

	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/database"
	_ "github.com/btcsuite/btcd/database/ffldb"
	"github.com/btcsuite/btcd/wire/v2"
)

var failedHashes = []string{
	"be12969b725eb8614c2bd2a5e0d8b8c6d66da67cc7f8591ee5092ed2db9cd3f2",
	"e187326d14712b6b02edc8509a17d62e81f50aee4e753a6b6f9074c2db6ad015",
	"3a63b1dc909fc4a0588ee12fe60884f4e6019630b9e932cf43f973b994d35633",
	"a798dd77839559ed4a158d73cd26dc702632c9e042a78254a0b00d54d170f274",
	"e08b528040d81dd825288178804d6b9a956d4b6fe9267574f8fceda84b0ba483",
	"68a32b4ce515b92507dceaa7a4fcf9cc4be7d9c6a0b8108dda02e04935f2d013",
	"f878cecce088f3efe98a7d407373a056c55fac0aa4fbd1801cc2677ce35f88e5",
	"fe50d1787e07f237f5da77bc21588c19f36c7ca007a5c1f1f90f8528355bef81",
	"56e3e02ba26d8a7de3d1b4c4bffe34a66425550e32dc9ce9f537524993ab8e26",
	"6d5e88821f0018d3eeaa9160bd7cc55e2353f65ea8fe36cea8dfedd3c7c2d5aa",
	"05acf8a3fc6e9d8a3826084947a4e59b79aa12dbdd6d48f97fec29fe5c11263c",
	"136a7a673378ff417d6e5219b9d1521505e73c71cecba4c15c3cada83b663739",
	"52da86d8407de9f9c0dad1c04df9c9d098166d563b539bb6675088589f3fd753",
	"15e427c31954389907ae0fb508330cf464f84a42c36cacfe4dfa50a1e16cfd2f",
	"7ee212bbe7ff1edc6dbe2ce461aa0d5dbb6a9eda8c6ba5869350bc69413f2800",
	"dd52f2d16b92522ff810c3949f99d658379df2cc2bf690b9e4e1e3c42d4110f1",
	"cb366c269dfc44f64d0bc8ecbaa83ce6c0afd459afae8cdcd89a4e98d5dd4e09", // parent 44362627
	"f592067723c9a4bce9a050d0aff0ef6c7e4f4277ebc87bdf6a6d97c3211c0923", // child 44362629
}

func parseBestChainState(raw []byte) (hash chainhash.Hash, height uint32, totalTxns uint64) {
	if len(raw) < chainhash.HashSize+4+8+4 {
		return
	}
	copy(hash[:], raw[:chainhash.HashSize])
	height = binary.LittleEndian.Uint32(raw[chainhash.HashSize : chainhash.HashSize+4])
	totalTxns = binary.LittleEndian.Uint64(raw[chainhash.HashSize+4 : chainhash.HashSize+12])
	return
}

func main() {
	dbPath := "D:\\LS\\data\\sugarmainnet\\blocks_ffldb"
	if len(os.Args) > 1 && os.Args[1] == "-dbpath" && len(os.Args) > 2 {
		dbPath = os.Args[2]
	}
	if _, err := os.Stat(dbPath); err != nil {
		fmt.Println("DB PATH ERR:", err)
		os.Exit(1)
	}

	db, err := database.Open("ffldb", dbPath, chaincfg.MainNetParams.Net)
	if err != nil {
		fmt.Println("OPEN ERR:", err)
		os.Exit(1)
	}
	defer db.Close()

	err = db.View(func(dbTx database.Tx) error {
		meta := dbTx.Metadata()

		// chainstate
		if raw := meta.Get([]byte("chainstate")); raw != nil {
			h, height, totalTxns := parseBestChainState(raw)
			fmt.Printf("chainstate: height=%d hash=%v totalTxns=%d\n", height, h, totalTxns)
		} else {
			fmt.Println("chainstate: missing")
		}
		// utxo consistency
		if raw := meta.Get([]byte("utxostateconsistency")); raw != nil {
			var h chainhash.Hash
			copy(h[:], raw)
			fmt.Printf("utxostateconsistency: %v\n", h)
		}
		// watermark
		if wm := meta.Bucket([]byte("writewatermark")); wm != nil {
			if v := wm.Get([]byte("height")); v != nil && len(v) == 4 {
				fmt.Printf("writewatermark: %d\n", int32(binary.LittleEndian.Uint32(v)))
			}
		}
		// best header state
		if raw := meta.Get([]byte("bestheaderstate")); raw != nil && len(raw) >= 36 {
			var h chainhash.Hash
			copy(h[:], raw[:32])
			fmt.Printf("bestheaderstate: height=%d hash=%v\n", int32(binary.LittleEndian.Uint32(raw[32:36])), h)
		}
		fmt.Println("---")

		// Dump persisted block-index rows (status byte) for heights around the stall.
		hasIdx := meta.Bucket([]byte("hashidx"))
		blockIdx := meta.Bucket([]byte("blockheaderidx"))
		if blockIdx != nil {
			fmt.Println("block-index rows at stall heights (status byte):")
			for h := 44362625; h <= 44362646; h++ {
				for _, hs := range failedHashes {
					hh, _ := chainhash.NewHashFromStr(hs)
					if v := hasIdx.Get(hh[:]); len(v) == 4 && int32(binary.LittleEndian.Uint32(v)) == int32(h) {
						key := make([]byte, 4+chainhash.HashSize)
						binary.BigEndian.PutUint32(key[:4], uint32(h))
						copy(key[4:], hh[:])
						row := blockIdx.Get(key)
						if row != nil && len(row) > 80 {
							fmt.Printf("  height=%d hash=%s... status=[0x%02x]\n", h, hs[:12], row[len(row)-1])
						} else if row != nil {
							fmt.Printf("  height=%d hash=%s... row=%d bytes (short, no status)\n", h, hs[:12], len(row))
						}
						break
					}
				}
			}
		}
		fmt.Println("---")

		spendBucket := meta.Bucket([]byte("spendjournal"))

		for _, hs := range failedHashes {
			h, err := chainhash.NewHashFromStr(hs)
			if err != nil {
				fmt.Println("bad hash:", hs)
				continue
			}
			// height from hash index
			height := int32(-1)
			if v := hasIdx.Get(h[:]); len(v) == 4 {
				height = int32(binary.LittleEndian.Uint32(v))
			}
			// block payload
			raw, err := dbTx.FetchBlock(h)
			var nInputs, nTx int
			var hdr wire.BlockHeader
			if err == nil {
				msg := new(wire.MsgBlock)
				if err2 := msg.Deserialize(bytes.NewReader(raw)); err2 == nil {
					nTx = len(msg.Transactions)
					for _, tx := range msg.Transactions[1:] {
						nInputs += len(tx.TxIn)
					}
					hdr = msg.Header
				}
			}
			// journal raw length
			jlen := -1
			var jraw []byte
			if spendBucket != nil {
				jraw = spendBucket.Get(h[:])
				if jraw != nil {
					jlen = len(jraw)
				}
			}
			// disk-verify: does the payload really exist per HasBlock?
			hasBlock, _ := dbTx.HasBlock(h)
			state := "missing"
			if err != nil {
				state = "NO-BLOCK:" + err.Error()
			}
			fmt.Printf("hash=%s height=%d blockTxns=%d nonCoinbaseInputs=%d journalBytes=%d hasBlock=%v %s\n",
				hs[:16], height, nTx, nInputs, jlen, hasBlock, state)
			if err == nil {
				fmt.Printf("   hdr: prev=%v version=%d", hdr.PrevBlock, hdr.Version)
				if nInputs > 0 && jlen == 0 {
					fmt.Printf("  <== 空 journal 但块有输入,反序列化必然断言")
				}
				fmt.Println()
			}
		}
		return nil
	})
	if err != nil {
		fmt.Println("VIEW ERR:", err)
		os.Exit(1)
	}
	fmt.Println("OK")

	// also count total nodes? skip.
	_ = new(big.Int)
}