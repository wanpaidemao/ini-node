package main

import (
	"fmt"
	"time"

	"github.com/btcsuite/btcd/pow"
	"github.com/btcsuite/btcd/wire/v2"
	"github.com/btcsuite/btcd/chaincfg/v2"
)

func main() {
	params := &chaincfg.MainNetParams
	header := &wire.BlockHeader{
		Version:    1,
		Bits:       params.PowLimitBits,
		Timestamp:  time.Now(),
	}
	const n = 500
	start := time.Now()
	for i := 0; i < n; i++ {
		pow.BlockPoWHash(header)
	}
	el := time.Since(start)
	fmt.Printf("%d hashes in %v => %v/hash (%.0f/s)\n", n, el, el/time.Duration(n), float64(n)/el.Seconds())
}
