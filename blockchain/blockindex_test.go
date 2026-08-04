// Copyright (c) 2015-2026 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package blockchain

import (
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/database"
	"github.com/btcsuite/btcd/txscript/v2"
	"github.com/btcsuite/btcd/wire/v2"
)

// countingDB wraps a database.DB and counts the number of Update calls.
type countingDB struct {
	database.DB
	updates int
}

// Update increments the updates counter on a call.
func (c *countingDB) Update(fn func(tx database.Tx) error) error {
	c.updates++
	return c.DB.Update(fn)
}

// TestFlushToDB tests that flushToDB opens a single write transaction for any
// non-empty dirty set and persists every node, including header-only nodes.
func TestFlushToDB(t *testing.T) {
	tests := []struct {
		name string

		// statuses defines the dirty nodes to create for this test
		// case. Each entry's status determines whether the node is
		// header-only or has block data. A nil slice means no nodes
		// are added (empty dirty set).
		statuses []blockStatus

		// wantUpdates is the expected number of DB Update calls.
		wantUpdates int
	}{
		{
			name:        "empty dirty set",
			statuses:    nil,
			wantUpdates: 0,
		},
		{
			name:        "single header-only node",
			statuses:    []blockStatus{statusHeaderStored},
			wantUpdates: 1,
		},
		{
			name: "multiple header-only nodes",
			statuses: []blockStatus{
				statusHeaderStored,
				statusHeaderStored,
				statusHeaderStored,
			},
			wantUpdates: 1,
		},
		{
			name:        "single data node",
			statuses:    []blockStatus{statusDataStored | statusHeaderStored},
			wantUpdates: 1,
		},
		{
			name: "header-only and data nodes mixed",
			statuses: []blockStatus{
				statusHeaderStored,
				statusDataStored | statusHeaderStored,
			},
			wantUpdates: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chain, teardown, err := chainSetup(
				"flushtodbtest", &chaincfg.SimNetParams,
			)
			if err != nil {
				t.Fatalf("failed to setup chain: %v", err)
			}
			defer teardown()

			bi := chain.index
			cdb := &countingDB{DB: bi.db}
			bi.db = cdb

			// Create the dirty nodes for this test case, chaining
			// each off the genesis tip.
			tip := chain.bestChain.Tip()
			var nodes []*blockNode
			for i, status := range test.statuses {
				node := newBlockNode(&wire.BlockHeader{
					PrevBlock: tip.hash,
					Nonce:     uint32(i),
				}, tip)
				node.status = status
				bi.AddNode(node)
				nodes = append(nodes, node)
				tip = node
			}

			err = bi.flushToDB()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if cdb.updates != test.wantUpdates {
				t.Fatalf("expected %d Update calls, got %d",
					test.wantUpdates, cdb.updates)
			}

			bi.RLock()
			dirtyLen := len(bi.dirty)
			bi.RUnlock()

			if dirtyLen != 0 {
				t.Fatalf("expected dirty set to be empty, got %d",
					dirtyLen)
			}

			// Every node, including header-only nodes, should be in
			// the DB after the flush.
			for i, node := range nodes {
				var found bool
				err := bi.db.View(func(dbTx database.Tx) error {
					bucket := dbTx.Metadata().Bucket(blockIndexBucketName)
					key := blockIndexKey(&node.hash, uint32(node.height))
					found = bucket.Get(key) != nil
					return nil
				})
				if err != nil {
					t.Fatalf("node %d: View failed: %v", i, err)
				}

				if !found {
					t.Fatalf("node %d: expected node to be in database",
						i)
				}
			}
		})
	}
}

func TestBestHeaderState(t *testing.T) {
	if err := os.MkdirAll(testDbRoot, 0700); err != nil {
		t.Fatalf("failed to create test db root: %v", err)
	}
	dbPath := filepath.Join(testDbRoot, "bestheaderstatetest")

	newChain := func(db database.DB) (*BlockChain, error) {
		return New(&Config{
			DB:          db,
			ChainParams: &chaincfg.SimNetParams,
			Checkpoints: nil,
			TimeSource:  NewMedianTime(),
			SigCache:    txscript.NewSigCache(1000),
		})
	}

	addHeaderNodes := func(chain *BlockChain, n int) *blockNode {
		tip := chain.bestChain.Tip()
		var last *blockNode
		for i := int32(1); i <= int32(n); i++ {
			node := newBlockNode(&wire.BlockHeader{
				PrevBlock: tip.hash,
				Nonce:     uint32(i),
			}, tip)
			node.status = statusHeaderStored
			chain.index.AddNode(node)
			tip = node
			last = node
		}
		chain.bestHeader.SetTip(last)
		return last
	}

	// Positive case: the best header tip must be restored from the
	// persisted best header state after a restart.
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
	last := addHeaderNodes(chain1, 10)
	if err := chain1.FlushBlockIndex(); err != nil {
		db1.Close()
		t.Fatalf("failed to flush block index: %v", err)
	}
	wantHash, wantHeight := last.hash, last.height
	db1.Close()

	db2, err := database.Open(testDbType, dbPath, blockDataNet)
	if err != nil {
		t.Fatalf("failed to reopen db: %v", err)
	}
	chain2, err := newChain(db2)
	if err != nil {
		db2.Close()
		t.Fatalf("failed to reopen chain: %v", err)
	}
	gotHash, gotHeight := chain2.BestHeader()
	if gotHash != wantHash || gotHeight != wantHeight {
		db2.Close()
		_ = os.RemoveAll(dbPath)
		_ = os.RemoveAll(testDbRoot)
		t.Fatalf("best header after restart = %s(%d), want %s(%d)",
			gotHash, gotHeight, wantHash, wantHeight)
	}

	// Negative case: a database that predates the best header state key
	// (simulated by deleting it) must fall back to the best chain tip.
	err = db2.Update(func(dbTx database.Tx) error {
		return dbTx.Metadata().Delete(bestHeaderStateKeyName)
	})
	if err != nil {
		db2.Close()
		_ = os.RemoveAll(dbPath)
		_ = os.RemoveAll(testDbRoot)
		t.Fatalf("failed to delete best header state: %v", err)
	}
	db2.Close()

	db3, err := database.Open(testDbType, dbPath, blockDataNet)
	if err != nil {
		t.Fatalf("failed to reopen db: %v", err)
	}
	chain3, err := newChain(db3)
	if err != nil {
		db3.Close()
		t.Fatalf("failed to reopen chain: %v", err)
	}
	gotHash, gotHeight = chain3.BestHeader()
	chainTip := chain3.bestChain.Tip()
	db3.Close()
	_ = os.RemoveAll(dbPath)
	_ = os.RemoveAll(testDbRoot)
	if gotHash != chainTip.hash || gotHeight != chainTip.height {
		t.Fatalf("best header without state = %s(%d), want best chain tip %s(%d)",
			gotHash, gotHeight, chainTip.hash, chainTip.height)
	}
}

// TestHeaderWindowEviction exercises the in-memory header window: nodes below
// the window boundary are evicted from the index after a flush, the boundary
// anchor keeps a severed parent (with the parent hash retained) and the correct
// cumulative workSum, and a restart with the window enabled materializes only
// the in-window nodes and reproduces the exact same cumulative work at the
// boundary.
func TestHeaderWindowEviction(t *testing.T) {
	if err := os.MkdirAll(testDbRoot, 0700); err != nil {
		t.Fatalf("failed to create test db root: %v", err)
	}
	dbPath := filepath.Join(testDbRoot, "headerwindowtest")

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

	addHeaderNodes := func(chain *BlockChain, from *blockNode, start, n int32) *blockNode {
		tip := from
		var last *blockNode
		for i := start; i < start+n; i++ {
			node := newBlockNode(&wire.BlockHeader{
				PrevBlock: tip.hash,
				Bits:      headerBits,
				Nonce:     uint32(i),
			}, tip)
			node.status = statusHeaderStored
			chain.index.AddNode(node)
			tip = node
			last = node
		}
		chain.bestHeader.SetTip(last)
		return last
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
	last := addHeaderNodes(chain1, genesis, 1, 20)

	// Capture the state of the node that will become the window boundary
	// (height 15) before it is affected by eviction.
	wantBoundaryWork := new(big.Int).Set(
		chain1.bestHeader.NodeByHeight(15).workSum)
	wantBoundaryParentHash := chain1.bestHeader.NodeByHeight(15).parentHash
	wantTipWork := new(big.Int).Set(last.workSum)
	genesisHash := genesis.hash
	node14Hash := chain1.bestHeader.NodeByHeight(14).hash

	// Flush: this writes all nodes and triggers window eviction.
	if err := chain1.FlushBlockIndex(); err != nil {
		db1.Close()
		t.Fatalf("failed to flush block index: %v", err)
	}

	// After the flush, only heights [15, 20] plus the boundary anchor's
	// severed parent state should remain in the index.  Genesis and other
	// below-window nodes are evicted.
	chain1.index.RLock()
	numIndex := len(chain1.index.index)
	chain1.index.RUnlock()
	if numIndex != 7 {
		db1.Close()
		t.Fatalf("expected 7 nodes in index after eviction (genesis kept "+
			"in the best chain view + heights 15-20), got %d", numIndex)
	}
	if chain1.index.LookupNode(&genesisHash) == nil {
		db1.Close()
		t.Fatalf("expected genesis to be retained (best chain view)")
	}
	if chain1.index.LookupNode(&node14Hash) != nil {
		db1.Close()
		t.Fatalf("expected height 14 node to be evicted from the index")
	}
	boundaryNode := chain1.bestHeader.NodeByHeight(15)
	if boundaryNode == nil || boundaryNode.parent != nil {
		db1.Close()
		t.Fatalf("expected boundary node with severed parent")
	}
	if boundaryNode.parentHash != wantBoundaryParentHash {
		db1.Close()
		t.Fatalf("boundary parentHash = %v, want %v",
			boundaryNode.parentHash, wantBoundaryParentHash)
	}
	if chain1.bestHeader.NodeByHeight(14) != nil {
		db1.Close()
		t.Fatalf("expected view slot 14 to be pruned")
	}
	db1.Close()

	// Restart with the window enabled: only the two in-window regions are
	// materialized and the boundary node must reproduce the exact cumulative
	// work computed before the eviction.
	db2, err := database.Open(testDbType, dbPath, blockDataNet)
	if err != nil {
		t.Fatalf("failed to reopen db: %v", err)
	}
	chain2, err := newChain(db2)
	if err != nil {
		db2.Close()
		t.Fatalf("failed to reopen chain: %v", err)
	}

	chain2.index.RLock()
	numIndex2 := len(chain2.index.index)
	chain2.index.RUnlock()
	if numIndex2 != 7 {
		db2.Close()
		t.Fatalf("expected 7 nodes materialized after windowed restart "+
			"(genesis + heights 15-20), got %d", numIndex2)
	}
	if chain2.bestChain.Tip() == nil ||
		chain2.bestChain.Tip().hash != genesisHash {
		db2.Close()
		t.Fatalf("expected best chain tip to be genesis")
	}
	boundary2 := chain2.bestHeader.NodeByHeight(15)
	if boundary2 == nil {
		db2.Close()
		t.Fatalf("expected boundary node at height 15 after restart")
	}
	if boundary2.parent != nil {
		db2.Close()
		t.Fatalf("expected boundary parent to be severed after restart")
	}
	if boundary2.parentHash != wantBoundaryParentHash {
		db2.Close()
		t.Fatalf("restarted boundary parentHash = %v, want %v",
			boundary2.parentHash, wantBoundaryParentHash)
	}
	if boundary2.workSum.Cmp(wantBoundaryWork) != 0 {
		db2.Close()
		t.Fatalf("restarted boundary workSum = %v, want %v",
			boundary2.workSum, wantBoundaryWork)
	}
	tip2 := chain2.bestHeader.Tip()
	if tip2.hash != last.hash || tip2.workSum.Cmp(wantTipWork) != 0 {
		db2.Close()
		t.Fatalf("restarted header tip work = %v, want %v",
			tip2.workSum, wantTipWork)
	}
	if chain2.bestHeader.NodeByHeight(0) != nil {
		db2.Close()
		t.Fatalf("expected genesis slot to be pruned from header view")
	}

	// Extend the header chain past the window: eviction must advance the
	// window and sever the new boundary node.
	last2 := addHeaderNodes(chain2, tip2, 21, 2)
	if err := chain2.FlushBlockIndex(); err != nil {
		db2.Close()
		t.Fatalf("failed to flush after extension: %v", err)
	}
	_ = last2

	chain2.index.RLock()
	numIndex3 := len(chain2.index.index)
	chain2.index.RUnlock()
	if numIndex3 != 7 {
		db2.Close()
		t.Fatalf("expected 7 nodes after advancing the window "+
			"(genesis + heights 17-22), got %d", numIndex3)
	}
	if chain2.index.LookupNode(&node14Hash) != nil ||
		chain2.bestHeader.NodeByHeight(16) != nil {
		db2.Close()
		t.Fatalf("expected heights 15-16 to be evicted after advancing")
	}
	if n := chain2.bestHeader.NodeByHeight(17); n == nil || n.parent != nil {
		db2.Close()
		t.Fatalf("expected new boundary node at 17 with severed parent")
	}
	if n := chain2.bestHeader.NodeByHeight(18); n == nil ||
		n.parent != chain2.bestHeader.NodeByHeight(17) {
		db2.Close()
		t.Fatalf("expected node 18 to keep its in-window parent")
	}
	if n := chain2.bestHeader.NodeByHeight(17); n.Ancestor(0) != nil ||
		n.Parent() != nil {
		db2.Close()
		t.Fatalf("expected upward walks to terminate at the boundary")
	}
	db2.Close()
	_ = os.RemoveAll(dbPath)
	_ = os.RemoveAll(testDbRoot)
}

func TestAncestor(t *testing.T) {
	height := 500_000
	blockNodes := chainedNodes(nil, height)

	for i, blockNode := range blockNodes {
		// Grab a random node that's a child of this node
		// and try to fetch the current blockNode with Ancestor.
		randNode := blockNodes[rand.Intn(height-i)+i]
		got := randNode.Ancestor(blockNode.height)

		// See if we got the right one.
		if got.hash != blockNode.hash {
			t.Fatalf("expected ancestor at height %d "+
				"but got a node at height %d",
				blockNode.height, got.height)
		}

		// Gensis doesn't have ancestors so skip the check below.
		if blockNode.height == 0 {
			continue
		}

		// The ancestors are deterministic so check that this node's
		// ancestor is the correct one.
		if blockNode.ancestor.height != getAncestorHeight(blockNode.height) {
			t.Fatalf("expected anestor at height %d, but it was at %d",
				getAncestorHeight(blockNode.height),
				blockNode.ancestor.height)
		}
	}
}

// TestChainTipsWindowEviction verifies that ChainTips does not panic when a
// chain tip's fork point has been evicted below the in-memory header window.
// Such a tip is fully detached from the best chain view and FindFork returns
// nil, so the branch length must be reported as the tip's full height.
func TestChainTipsWindowEviction(t *testing.T) {
	if err := os.MkdirAll(testDbRoot, 0700); err != nil {
		t.Fatalf("failed to create test db root: %v", err)
	}
	dbPath := filepath.Join(testDbRoot, "chaintipswindowtest")

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
	var last *blockNode
	for i := int32(1); i <= 20; i++ {
		node := newBlockNode(&wire.BlockHeader{
			PrevBlock: tip.hash,
			Bits:      headerBits,
			Nonce:     uint32(i),
		}, tip)
		node.status = statusHeaderStored
		chain1.index.AddNode(node)
		tip = node
		last = node
	}
	chain1.bestHeader.SetTip(last)

	// Add a side branch forking at height 14.  After window eviction the
	// fork point (height 14) is evicted while the side tip (height 16)
	// stays materialized with a severed parent pointer.
	forkFrom := chain1.bestHeader.NodeByHeight(14)
	sTip := forkFrom
	for i := int32(0); i < 2; i++ {
		sNode := newBlockNode(&wire.BlockHeader{
			PrevBlock: sTip.hash,
			Bits:      headerBits,
			Nonce:     uint32(500 + i),
		}, sTip)
		sNode.status = statusHeaderStored
		chain1.index.AddNode(sNode)
		sTip = sNode
	}

	// Flush: this writes all nodes and triggers window eviction.  Heights
	// below 15 are removed and the boundary node's parent is severed.
	if err := chain1.FlushBlockIndex(); err != nil {
		db1.Close()
		t.Fatalf("failed to flush block index: %v", err)
	}

	// The side tip's parent chain is severed below the window boundary so
	// its fork point is not materialized.  ChainTips must report its branch
	// length as the full height instead of panicking on a nil fork.
	tips := chain1.ChainTips()

	sideFound := false
	for _, ct := range tips {
		if ct.BlockHash.IsEqual(&sTip.hash) {
			sideFound = true
			if ct.BranchLen != sTip.height {
				db1.Close()
				t.Fatalf("side tip branch len = %d, want full height %d",
					ct.BranchLen, sTip.height)
			}
		}
	}
	if !sideFound {
		db1.Close()
		t.Fatalf("expected the evicted-fork side tip in chaintips output")
	}

	// The main header tip also has a fork below the window (the best chain
	// view only contains genesis) and must not panic either.
	mainFound := false
	for _, ct := range tips {
		if ct.BlockHash.IsEqual(&last.hash) {
			mainFound = true
			if ct.BranchLen != last.height {
				db1.Close()
				t.Fatalf("main header tip branch len = %d, want %d",
					ct.BranchLen, last.height)
			}
		}
	}
	if !mainFound {
		db1.Close()
		t.Fatalf("expected main header tip in chaintips output")
	}

	db1.Close()
}
