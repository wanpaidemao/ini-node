// Copyright (c) 2015-2026 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package blockchain

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/database"
	"github.com/btcsuite/btcd/txscript/v2"
	"github.com/btcsuite/btcd/wire/v2"
)

// TestColdReadFallback exercises the cold-read layer: after the in-memory
// header window has evicted older heights, the two-hop block index fallback
// must serve hash/height/header queries, height ranges and P2P block/header
// locates for those evicted heights, while keeping a stable node identity via
// the cold cache.
func TestColdReadFallback(t *testing.T) {
	if err := os.MkdirAll(testDbRoot, 0700); err != nil {
		t.Fatalf("failed to create test db root: %v", err)
	}
	dbPath := filepath.Join(testDbRoot, "coldreadtest")

	const window = 5
	headerBits := uint32(0x1f00ffff)

	newChain := func(db database.DB) (*BlockChain, error) {
		return New(&Config{
			DB:           db,
			ChainParams:  &chaincfg.SimNetParams,
			Checkpoints:  nil,
			TimeSource:   NewMedianTime(),
			SigCache:     txscript.NewSigCache(1000),
			HeaderWindow: window,
		})
	}

	_ = os.RemoveAll(dbPath)
	db1, err := database.Create(testDbType, dbPath, blockDataNet)
	if err != nil {
		t.Fatalf("failed to create db: %v", err)
	}
	chain1, err := newChain(db1)
	if err != nil {
		db1.Close()
		t.Fatalf("failed to create chain: %v", err)
	}

	// Build a header chain of 20 blocks on top of genesis.
	genesis := chain1.bestChain.Tip()
	tip := genesis
	var expectedHashes [21]chainhash.Hash
	expectedHashes[0] = genesis.hash
	for i := int32(1); i <= 20; i++ {
		node := newBlockNode(&wire.BlockHeader{
			PrevBlock: tip.hash,
			Bits:      headerBits,
			Nonce:     uint32(i),
		}, tip)
		node.status = statusHeaderStored
		chain1.index.AddNode(node)
		expectedHashes[i] = node.hash
		tip = node
	}
	chain1.bestHeader.SetTip(tip)

	// Flush: this writes every node to the block index and triggers window
	// eviction, leaving only heights [15, 20] plus genesis in memory.
	if err := chain1.FlushBlockIndex(); err != nil {
		db1.Close()
		t.Fatalf("failed to flush block index: %v", err)
	}

	// Seed the height index exactly as connecting the blocks to the main
	// chain would, so the cold-read layer has a main-chain height to hash
	// mapping for the evicted heights.
	err = db1.Update(func(dbTx database.Tx) error {
		for h := int32(1); h <= 20; h++ {
			if err := dbPutBlockIndex(dbTx, &expectedHashes[h], h); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db1.Close()
		t.Fatalf("failed to seed height index: %v", err)
	}
	db1.Close()

	// Restart with the window enabled: only genesis and heights [15, 20] are
	// materialized, so heights [1, 14] must be served from disk.
	db2, err := database.Open(testDbType, dbPath, blockDataNet)
	if err != nil {
		t.Fatalf("failed to reopen db: %v", err)
	}
	chain2, err := newChain(db2)
	if err != nil {
		db2.Close()
		t.Fatalf("failed to reopen chain: %v", err)
	}

	if chain2.bestHeader.NodeByHeight(14) != nil {
		db2.Close()
		t.Fatalf("expected height 14 to be evicted from the header window")
	}

	// Header and hash lookups by height.
	hash14, err := chain2.HeaderHashByHeight(14)
	if err != nil || *hash14 != expectedHashes[14] {
		db2.Close()
		t.Fatalf("HeaderHashByHeight(14) = %v, err %v, want %v",
			hash14, err, expectedHashes[14])
	}
	hashByHeight, err := chain2.BlockHashByHeight(14)
	if err != nil || *hashByHeight != expectedHashes[14] {
		db2.Close()
		t.Fatalf("BlockHashByHeight(14) = %v, err %v, want %v",
			hashByHeight, err, expectedHashes[14])
	}

	// Height lookups by hash.
	height, err := chain2.HeaderHeightByHash(expectedHashes[14])
	if err != nil || height != 14 {
		db2.Close()
		t.Fatalf("HeaderHeightByHash(height 14 hash) = %d, err %v, want 14",
			height, err)
	}
	height, err = chain2.BlockHeightByHash(&expectedHashes[14])
	if err != nil || height != 14 {
		db2.Close()
		t.Fatalf("BlockHeightByHash(height 14 hash) = %d, err %v, want 14",
			height, err)
	}

	// Header validation from the persisted status.
	if !chain2.IsValidHeader(&expectedHashes[14]) {
		db2.Close()
		t.Fatalf("expected evicted header to remain valid")
	}

	// Cold materialization keeps a stable node identity via the cache.
	n1 := chain2.nodeAtHeight(14)
	n2 := chain2.nodeAtHeight(14)
	if n1 == nil || n1 != n2 {
		db2.Close()
		t.Fatalf("expected identical cold node identity, got %v vs %v", n1, n2)
	}
	if cached := chain2.coldCache.get(&expectedHashes[14]); cached != n1 {
		db2.Close()
		t.Fatalf("expected cold cache to retain the materialized node")
	}
	if n1.hash != expectedHashes[14] || n1.height != 14 {
		db2.Close()
		t.Fatalf("cold node hash/height = %v/%d, want %v/14",
			n1.hash, n1.height, expectedHashes[14])
	}
	if n1.parent != nil {
		db2.Close()
		t.Fatalf("expected severed parent on cold node")
	}
	if n1.workSum.Sign() != 0 {
		db2.Close()
		t.Fatalf("expected zero cumulative work on cold node")
	}

	// Pointer identity: an in-window boundary node queried through the
	// cold-read path must resolve to the very same pointer the in-memory view
	// holds, never a rebuilt duplicate.  Reorg/fork decisions depend on this.
	boundary := chain2.bestHeader.NodeByHeight(15)
	if boundary == nil {
		db2.Close()
		t.Fatalf("expected boundary node at height 15 to be in-memory")
	}
	if got := chain2.materializeColdNode(&expectedHashes[15]); got != boundary {
		db2.Close()
		t.Fatalf("cold query of in-memory boundary node returned a distinct pointer")
	}
	if got := chain2.coldNodeAtHeight(15); got != boundary {
		db2.Close()
		t.Fatalf("coldNodeAtHeight(15) must return the in-memory pointer")
	}

	// Advance the in-memory best chain to the header tip so range and locate
	// helpers see a long main chain above the evicted heights.  (In a running
	// node this follows from connecting blocks.)
	chain2.bestChain.SetTip(chain2.bestHeader.NodeByHeight(20))

	// Height range spanning evicted and retained heights.
	rangeHashes, err := chain2.HeightRange(10, 16)
	if err != nil {
		db2.Close()
		t.Fatalf("HeightRange failed: %v", err)
	}
	if len(rangeHashes) != 6 {
		db2.Close()
		t.Fatalf("HeightRange length = %d, want 6", len(rangeHashes))
	}
	for i := 0; i < len(rangeHashes); i++ {
		want := expectedHashes[10+i]
		if rangeHashes[i] != want {
			db2.Close()
			t.Fatalf("HeightRange[%d] = %v, want %v", i, rangeHashes[i], want)
		}
	}

	// P2P block locate anchored at an evicted height.
	locator := BlockLocator{&expectedHashes[14]}
	blockHashes := chain2.LocateBlocks(locator, &expectedHashes[20], 2000)
	if len(blockHashes) != 6 {
		db2.Close()
		t.Fatalf("LocateBlocks length = %d, want 6", len(blockHashes))
	}
	for i := 0; i < len(blockHashes); i++ {
		want := expectedHashes[15+i]
		if blockHashes[i] != want {
			db2.Close()
			t.Fatalf("LocateBlocks[%d] = %v, want %v", i, blockHashes[i], want)
		}
	}

	// P2P header locate anchored at an evicted height.
	headers := chain2.LocateHeaders(locator, &expectedHashes[20])
	if len(headers) != 6 {
		db2.Close()
		t.Fatalf("LocateHeaders length = %d, want 6", len(headers))
	}
	for i := 0; i < len(headers); i++ {
		want := expectedHashes[15+i]
		if headers[i].BlockHash() != want {
			db2.Close()
			t.Fatalf("LocateHeaders[%d] hash = %v, want %v",
				i, headers[i].BlockHash(), want)
		}
	}

	// A stop hash below the window with no locators must return that single
	// evicted block.
	single := chain2.LocateBlocks(nil, &expectedHashes[10], 2000)
	if len(single) != 1 || single[0] != expectedHashes[10] {
		db2.Close()
		t.Fatalf("LocateBlocks(nil locator) = %v, want [%v]",
			single, expectedHashes[10])
	}

	db2.Close()
}