package main

import (
	"encoding/binary"
	"fmt"
	"os"

	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/database"
	_ "github.com/btcsuite/btcd/database/ffldb"
)

func main() {
	dbPath := `C:\Users\adest\AppData\Local\btcd\sugarmainnet\blocks_ffldb`
	db, err := database.Open("ffldb", dbPath, chaincfg.MainNetParams.Net)
	if err != nil {
		fmt.Println("OPEN ERR:", err)
		os.Exit(1)
	}
	defer db.Close()

	err = db.View(func(dbTx database.Tx) error {
		md := dbTx.Metadata()

		chainRaw := md.Get([]byte("chainstate"))
		fmt.Printf("chainstate (%d bytes): %x\n", len(chainRaw), chainRaw)
		if len(chainRaw) >= 4+32+32 {
			h := binary.LittleEndian.Uint32(chainRaw[0:4])
			fmt.Printf("  best chain height: %d\n", h)
		}

		hs := md.Get([]byte("headerchainstate"))
		fmt.Printf("headerchainstate (%d bytes): %x\n", len(hs), hs)
		if len(hs) >= 4+32+32 {
			hh := binary.LittleEndian.Uint32(hs[0:4])
			fmt.Printf("  best header height: %d\n", hh)
		}

		bds := md.Get([]byte("blockdownloadstate"))
		fmt.Printf("blockdownloadstate (%d bytes): %x\n", len(bds), bds)

		bucket := md.Bucket([]byte("blockheaderidx"))
		if bucket == nil {
			fmt.Println("blockheaderidx bucket: MISSING")
			return nil
		}
		count := 0
		c := bucket.Cursor()
		for ok := c.First(); ok; ok = c.Next() {
			count++
		}
		fmt.Printf("blockheaderidx rows: %d\n", count)

		// Sample a few height->hash entries if the height index bucket exists.
		hb := md.Bucket([]byte("heightidx"))
		if hb == nil {
			fmt.Println("heightidx bucket: MISSING")
		} else {
			for _, h := range []uint32{0, 1, 10000, 100000, 1000000, 20000000, 43000000, 43709998, 43710000} {
				var key [4]byte
				binary.LittleEndian.PutUint32(key[:], h)
				v := hb.Get(key[:])
				fmt.Printf("  height %d -> %x\n", h, v)
			}
		}

		hi := md.Bucket([]byte("hashidx"))
		if hi != nil {
			c := hi.Cursor()
			n := 0
			for ok := c.First(); ok; ok = c.Next() {
				n++
			}
			fmt.Printf("hashidx entries: %d\n", n)
		} else {
			fmt.Println("hashidx bucket: MISSING")
		}

		he := md.Bucket([]byte("heightidx"))
		if he != nil {
			c := he.Cursor()
			n := 0
			firstH, lastH := uint32(0xffffffff), uint32(0)
			var firstKey, lastKey [4]byte
			for ok := c.First(); ok; ok = c.Next() {
				binary.LittleEndian.PutUint32(firstKey[:], firstH)
				k := c.Key()
				if len(k) == 4 {
					h := binary.LittleEndian.Uint32(k)
					if h < firstH {
						firstH = h
						copy(firstKey[:], k)
					}
					if h > lastH {
						lastH = h
						copy(lastKey[:], k)
					}
				}
				n++
			}
			fmt.Printf("heightidx entries: %d (first=%d last=%d)\n", n, firstH, lastH)
			_ = lastKey
		} else {
			fmt.Println("heightidx bucket: MISSING")
		}

		// Fetch a raw block by hash to confirm block storage works.
		return nil
	})
	if err != nil {
		fmt.Println("VIEW ERR:", err)
		os.Exit(1)
	}
}
