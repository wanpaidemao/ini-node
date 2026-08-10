package main

import (
	"encoding/binary"
	"fmt"
	"os"

	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/database"
	_ "github.com/btcsuite/btcd/database/ffldb"
)

const (
	chainStateBucketName     = "chainstate"
	headerChainStateBucket   = "headerchainstate"
	blockIndexBucketName     = "blockheaderidx"
	heightIndexBucketName    = "heightidx"
)

// stateHeight parses the height stored in a serialized best-chain/best-header
// state value.  Both formats store the hash first (32 bytes) followed by the
// height as a little-endian uint32 at offset 32.
func stateHeight(state []byte) int32 {
	if len(state) < 36 {
		return -1
	}
	return int32(binary.LittleEndian.Uint32(state[32:36]))
}

func main() {
	repair := false
	for _, a := range os.Args[1:] {
		if a == "-repair" {
			repair = true
		}
	}

	dbPath := `C:\Users\adest\AppData\Local\btcd\sugarmainnet\blocks_ffldb`
	db, err := database.Open("ffldb", dbPath, chaincfg.MainNetParams.Net)
	if err != nil {
		fmt.Println("OPEN ERR:", err)
		os.Exit(1)
	}
	defer db.Close()

	var (
		headerHeight int32
		missing      []uint32
	)

	err = db.View(func(dbTx database.Tx) error {
		md := dbTx.Metadata()

		chainRaw := md.Get([]byte(chainStateBucketName))
		fmt.Printf("chainstate best height: %d\n", stateHeight(chainRaw))

		hs := md.Get([]byte(headerChainStateBucket))
		headerHeight = stateHeight(hs)
		fmt.Printf("headerchainstate best header height: %d\n", headerHeight)

		bi := md.Bucket([]byte(blockIndexBucketName))
		if bi == nil {
			fmt.Println("blockheaderidx bucket: MISSING")
			return nil
		}
		rowCount := 0
		bc := bi.Cursor()
		for ok := bc.First(); ok; ok = bc.Next() {
			rowCount++
		}
		fmt.Printf("blockheaderidx rows: %d\n", rowCount)

		hb := md.Bucket([]byte(heightIndexBucketName))
		if hb == nil {
			fmt.Println("heightidx bucket: MISSING")
			return nil
		}

		// Detect gaps: mark every height present in the height index and
		// report those missing in [0, headerHeight].
		present := make([]bool, headerHeight+1)
		c := hb.Cursor()
		for ok := c.First(); ok; ok = c.Next() {
			k := c.Key()
			if len(k) == 4 {
				h := binary.LittleEndian.Uint32(k)
				if int32(h) <= headerHeight {
					present[h] = true
				}
			}
		}
		for h := int32(0); h <= headerHeight; h++ {
			if !present[h] {
				missing = append(missing, uint32(h))
			}
		}
		fmt.Printf("heightidx entries: %d; missing heights: %d\n",
			len(present)-len(missing), len(missing))
		for i, h := range missing {
			if i < 12 {
				fmt.Printf("  missing height %d\n", h)
			}
		}
		if len(missing) > 12 {
			fmt.Printf("  ... (%d more)\n", len(missing)-12)
		}
		return nil
	})
	if err != nil {
		fmt.Println("VIEW ERR:", err)
		os.Exit(1)
	}

	if !repair {
		return
	}

	missingSet := make(map[uint32]struct{}, len(missing))
	for _, h := range missing {
		missingSet[h] = struct{}{}
	}

	filled := 0
	seen := 0
	err = db.Update(func(dbTx database.Tx) error {
		md := dbTx.Metadata()
		hi := md.Bucket([]byte(heightIndexBucketName))
		bi := md.Bucket([]byte(blockIndexBucketName))

		bc := bi.Cursor()
		for ok := bc.First(); ok; ok = bc.Next() {
			key := bc.Key()
			if len(key) < 36 {
				continue
			}
			h := binary.BigEndian.Uint32(key[0:4])
			if int32(h) > headerHeight {
				continue
			}
			if _, ok := missingSet[h]; !ok {
				continue
			}
			seen++
			var hk [4]byte
			binary.LittleEndian.PutUint32(hk[:], h)
			if hi.Get(hk[:]) != nil {
				continue
			}
			hash := key[4:36]
			var hashCopy [32]byte
			copy(hashCopy[:], hash)
			if err := hi.Put(hk[:], hashCopy[:]); err != nil {
				return err
			}
			filled++
		}
		return nil
	})
	if err != nil {
		fmt.Println("REPAIR ERR:", err)
		os.Exit(1)
	}
	fmt.Printf("REPAIR: matched %d missing heights, filled %d new entries\n",
		seen, filled)
}
