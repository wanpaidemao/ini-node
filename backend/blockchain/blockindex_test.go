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

	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/database"
	"github.com/btcsuite/btcd/pow"
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

			err = bi.flushToDB(false)
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
	node1Hash := chain1.bestHeader.NodeByHeight(1).hash
	node4Hash := chain1.bestHeader.NodeByHeight(4).hash
	node14Hash := chain1.bestHeader.NodeByHeight(14).hash
	node5Hash := chain1.bestHeader.NodeByHeight(5).hash

	// Flush: this writes all nodes and triggers window eviction.
	if err := chain1.FlushBlockIndex(); err != nil {
		db1.Close()
		t.Fatalf("failed to flush block index: %v", err)
	}

	// After the flush, the index retains the header window proper (heights
	// [15, 20]), the block-connection frontier below it (heights 1-5, since
	// the best chain tip is still genesis and the frontier window spans one
	// window around it), and genesis (part of the best chain view).  Heights
	// 6-14 are below both windows and are evicted.
	chain1.index.RLock()
	numIndex := len(chain1.index.index)
	chain1.index.RUnlock()
	if numIndex != 12 {
		db1.Close()
		t.Fatalf("expected 12 nodes in index after eviction (genesis + "+
			"frontier heights 1-5 + header window 15-20), got %d", numIndex)
	}
	if chain1.index.LookupNode(&genesisHash) == nil {
		db1.Close()
		t.Fatalf("expected genesis to be retained (best chain view)")
	}
	if chain1.index.LookupNode(&node14Hash) != nil {
		db1.Close()
		t.Fatalf("expected height 14 node to be evicted from the index")
	}
	// A frontier node below the header boundary keeps its parent chain
	// intact so the next block can resolve it during connection.
	frontierNode := chain1.index.LookupNode(&node5Hash)
	if frontierNode == nil || frontierNode.parent == nil {
		db1.Close()
		t.Fatalf("expected frontier node at height 5 to be retained with "+
			"its parent chain intact")
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
	if numIndex2 != 12 {
		db2.Close()
		t.Fatalf("expected 12 nodes materialized after windowed restart "+
			"(genesis + block-connection frontier heights 1-5 + header "+
			"window 15-20), got %d", numIndex2)
	}
	if chain2.bestChain.Tip() == nil ||
		chain2.bestChain.Tip().hash != genesisHash {
		db2.Close()
		t.Fatalf("expected best chain tip to be genesis")
	}
	// The block-connection frontier must be materialized after restart so the
	// next blocks to download can resolve their parents from the in-memory
	// index (block acceptance never falls back to cold reads).
	for _, hash := range []chainhash.Hash{node1Hash, node4Hash, node5Hash} {
		n := chain2.index.LookupNode(&hash)
		if n == nil {
			db2.Close()
			t.Fatalf("expected frontier node %v to be materialized after "+
				"restart", hash)
		}
		if n.parent == nil {
			db2.Close()
			t.Fatalf("expected frontier node %v to keep its in-memory "+
				"parent after restart", hash)
		}
	}
	// The whole frontier must form one intact chain back to genesis so each
	// block's parent lookup succeeds as the download advances.
	for n := chain2.index.LookupNode(&node5Hash); n != nil; n = n.parent {
		if n.parent == nil && n.hash != genesisHash {
			db2.Close()
			t.Fatalf("expected frontier parent chain to terminate at "+
				"genesis, but stopped at %v", n.hash)
		}
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
	if numIndex3 != 12 {
		db2.Close()
		t.Fatalf("expected 12 nodes after advancing the window "+
			"(genesis + frontier heights 1-5 + heights 17-22), got %d",
			numIndex3)
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

// TestBlockConnectAfterWindowedRestart reproduces the "previous block unknown"
// stall that hits when a windowed node restarts with a header-only sync but a
// block's payload already persisted in a session whose connection never
// completed (the best chain stays at genesis while the block is in the
// database).  Block acceptance resolves the previous block through the
// in-memory index only, so the block-connection frontier (a window ahead of the
// best chain tip) must be materialized at startup or the parent is unresolvable
// even though its data is on disk.
func TestBlockConnectAfterWindowedRestart(t *testing.T) {
	if err := os.MkdirAll(testDbRoot, 0700); err != nil {
		t.Fatalf("failed to create test db root: %v", err)
	}
	dbPath := filepath.Join(testDbRoot, "blockconnectrestart")
	_ = os.RemoveAll(dbPath)

	const window = 5
	params := &chaincfg.SimNetParams

	newChain := func(db database.DB) (*BlockChain, error) {
		return New(&Config{
			DB:           db,
			ChainParams:  params,
			Checkpoints:  nil,
			TimeSource:   NewMedianTime(),
			SigCache:     txscript.NewSigCache(1000),
			HeaderWindow: window,
		})
	}

	// Build a chain of 20 solved blocks on top of genesis without processing
	// them, so their headers can seed a header-only sync.
	db1, err := database.Create(testDbType, dbPath, blockDataNet)
	if err != nil {
		t.Fatalf("failed to create db: %v", err)
	}
	chain1, err := newChain(db1)
	if err != nil {
		db1.Close()
		t.Fatalf("failed to create chain: %v", err)
	}
	tip := btcutil.NewBlock(params.GenesisBlock)
	tip.SetHeight(0)
	blocks := make([]*btcutil.Block, 0, 20)
	for i := int32(1); i <= 20; i++ {
		block, _, err := newBlock(chain1, tip, nil)
		if err != nil {
			db1.Close()
			t.Fatalf("failed to create block %d: %v", i, err)
		}

		// newBlock's solver hashes with double-SHA256, but Sugarchain
		// validates proof of work with yespower; re-solve the header nonce
		// against the chain's actual PoW hash so acceptance is deterministic.
		solveYespower(&block.MsgBlock().Header)

		blocks = append(blocks, block)
		tip = block
	}

	// Seed a header-only sync: every block's header becomes a header-only
	// index entry and the best header tip advances to height 20.
	headerTip := chain1.bestChain.Tip()
	for _, block := range blocks {
		node := newBlockNode(&block.MsgBlock().Header, headerTip)
		node.status = statusHeaderStored
		chain1.index.AddNode(node)
		headerTip = node
	}
	chain1.bestHeader.SetTip(headerTip)

	// Persist block 1's payload and mark its node data-stored without ever
	// connecting it, mirroring a session where the block was written to disk
	// before the chain connection failed (best chain stays at genesis).
	err = db1.Update(func(dbTx database.Tx) error {
		return dbStoreBlock(dbTx, blocks[0])
	})
	if err != nil {
		db1.Close()
		t.Fatalf("failed to store block 1: %v", err)
	}
	node1 := chain1.index.LookupNode(blocks[0].Hash())
	if node1 == nil {
		db1.Close()
		t.Fatalf("expected header node for block 1")
	}
	chain1.index.SetStatusFlags(node1, statusDataStored)
	if err := chain1.FlushBlockIndex(); err != nil {
		db1.Close()
		t.Fatalf("failed to flush block index: %v", err)
	}
	db1.Close()

	// Restart with the window enabled.  The block-connection frontier must
	// include height 1 so block 2 can resolve its parent from the in-memory
	// index.
	db2, err := database.Open(testDbType, dbPath, blockDataNet)
	if err != nil {
		t.Fatalf("failed to reopen db: %v", err)
	}
	chain2, err := newChain(db2)
	if err != nil {
		db2.Close()
		t.Fatalf("failed to reopen chain: %v", err)
	}

	if node := chain2.index.LookupNode(blocks[0].Hash()); node == nil {
		db2.Close()
		t.Fatalf("expected the block-connection frontier to materialize " +
			"block 1's node after a windowed restart")
	}

	// Feed the blocks in order, exactly as block download resumes after a
	// restart.  Block 2's parent has its payload on disk, so it is not an
	// orphan; its node must resolve through the in-memory index.
	for i := 1; i < len(blocks); i++ {
		_, isOrphan, err := chain2.ProcessBlock(blocks[i], BFNone)
		if err != nil {
			db2.Close()
			t.Fatalf("block %d failed to connect after windowed restart: %v",
				i+1, err)
		}
		if isOrphan {
			db2.Close()
			t.Fatalf("block %d unexpectedly orphaned after windowed restart",
				i+1)
		}
	}
	if got := chain2.bestChain.Height(); got != 20 {
		db2.Close()
		t.Fatalf("best chain height = %d, want 20", got)
	}
	db2.Close()
	_ = os.RemoveAll(dbPath)
	_ = os.RemoveAll(testDbRoot)
}

// TestBlockDownloadCursorPersists verifies that the furthest stored block is
// persisted alongside the chain state and restored after a restart, so a block
// download can resume from the highest locally-available block instead of
// re-scanning every height below it.
func TestBlockDownloadCursorPersists(t *testing.T) {
	if err := os.MkdirAll(testDbRoot, 0700); err != nil {
		t.Fatalf("failed to create test db root: %v", err)
	}
	dbPath := filepath.Join(testDbRoot, "blockdownloadcursor")
	_ = os.RemoveAll(dbPath)

	const window = 5
	params := &chaincfg.SimNetParams

	newChain := func(db database.DB) (*BlockChain, error) {
		return New(&Config{
			DB:           db,
			ChainParams:  params,
			Checkpoints:  nil,
			TimeSource:   NewMedianTime(),
			SigCache:     txscript.NewSigCache(1000),
			HeaderWindow: window,
		})
	}

	db1, err := database.Create(testDbType, dbPath, blockDataNet)
	if err != nil {
		t.Fatalf("failed to create db: %v", err)
	}
	chain1, err := newChain(db1)
	if err != nil {
		db1.Close()
		t.Fatalf("failed to create chain: %v", err)
	}

	// Connect 20 solved blocks in the normal IBD order.
	tip := btcutil.NewBlock(params.GenesisBlock)
	tip.SetHeight(0)
	blocks := make([]*btcutil.Block, 0, 20)
	for i := int32(1); i <= 20; i++ {
		block, _, err := newBlock(chain1, tip, nil)
		if err != nil {
			db1.Close()
			t.Fatalf("failed to create block %d: %v", i, err)
		}
		solveYespower(&block.MsgBlock().Header)
		blocks = append(blocks, block)
		tip = block
	}
	for _, block := range blocks {
		_, isOrphan, err := chain1.ProcessBlock(block, BFNone)
		if err != nil {
			db1.Close()
			t.Fatalf("failed to connect block: %v", err)
		}
		if isOrphan {
			db1.Close()
			t.Fatalf("block unexpectedly orphaned")
		}
	}

	// The download cursor tracks the highest block stored during the session.
	wantHash := blocks[19].Hash()
	gotHash, gotHeight := chain1.BestDownloadState()
	if gotHeight != 20 || gotHash != *wantHash {
		db1.Close()
		t.Fatalf("best download state = %v@%d, want %v@20",
			gotHash, gotHeight, *wantHash)
	}
	db1.Close()

	// Restart.  The cursor must be restored from the database and the stored
	// payloads remain fetchable even though their nodes were windowed out of
	// memory.
	db2, err := database.Open(testDbType, dbPath, blockDataNet)
	if err != nil {
		t.Fatalf("failed to reopen db: %v", err)
	}
	chain2, err := newChain(db2)
	if err != nil {
		db2.Close()
		t.Fatalf("failed to reopen chain: %v", err)
	}
	gotHash, gotHeight = chain2.BestDownloadState()
	if gotHeight != 20 || gotHash != *wantHash {
		db2.Close()
		t.Fatalf("best download state after restart = %v@%d, want %v@20",
			gotHash, gotHeight, *wantHash)
	}
	if _, err := chain2.FetchBlockByHash(blocks[4].Hash()); err != nil {
		db2.Close()
		t.Fatalf("failed to fetch stored block by hash after restart: %v", err)
	}
	// The cursor block is already connected, so a resume connect is a no-op.
	if isMain, err := chain2.ResumeBlockConnect(blocks[19].Hash(), BFNone); err != nil || !isMain {
		db2.Close()
		t.Fatalf("resume connect of connected block returned (%v, %v)", isMain, err)
	}
	db2.Close()
	_ = os.RemoveAll(dbPath)
	_ = os.RemoveAll(testDbRoot)
}

// TestResumeBlockConnectConnectsStoredBlocks verifies that blocks whose data
// was written to disk in a session whose connection never completed are driven
// through the connection logic by ResumeBlockConnect after a restart.  Without
// this, a resumed download would skip them (they are already present) and never
// connect them, stalling the sync at the stored frontier.
func TestResumeBlockConnectConnectsStoredBlocks(t *testing.T) {
	if err := os.MkdirAll(testDbRoot, 0700); err != nil {
		t.Fatalf("failed to create test db root: %v", err)
	}
	dbPath := filepath.Join(testDbRoot, "blockresumeconnect")
	_ = os.RemoveAll(dbPath)

	const window = 5
	params := &chaincfg.SimNetParams

	newChain := func(db database.DB) (*BlockChain, error) {
		return New(&Config{
			DB:           db,
			ChainParams:  params,
			Checkpoints:  nil,
			TimeSource:   NewMedianTime(),
			SigCache:     txscript.NewSigCache(1000),
			HeaderWindow: window,
		})
	}

	db1, err := database.Create(testDbType, dbPath, blockDataNet)
	if err != nil {
		t.Fatalf("failed to create db: %v", err)
	}
	chain1, err := newChain(db1)
	if err != nil {
		db1.Close()
		t.Fatalf("failed to create chain: %v", err)
	}

	// Build 20 solved blocks and seed a header-only sync whose best header
	// tip reaches height 20 while the best chain stays at genesis.
	tip := btcutil.NewBlock(params.GenesisBlock)
	tip.SetHeight(0)
	blocks := make([]*btcutil.Block, 0, 20)
	for i := int32(1); i <= 20; i++ {
		block, _, err := newBlock(chain1, tip, nil)
		if err != nil {
			db1.Close()
			t.Fatalf("failed to create block %d: %v", i, err)
		}
		solveYespower(&block.MsgBlock().Header)
		blocks = append(blocks, block)
		tip = block
	}
	headerTip := chain1.bestChain.Tip()
	for _, block := range blocks {
		node := newBlockNode(&block.MsgBlock().Header, headerTip)
		node.status = statusHeaderStored
		chain1.index.AddNode(node)
		headerTip = node
	}
	chain1.bestHeader.SetTip(headerTip)

	// Write every block's payload to disk and mark its node data-stored
	// without connecting any of them, mirroring a session interrupted between
	// the store and connect steps.
	err = db1.Update(func(dbTx database.Tx) error {
		for _, block := range blocks {
			if err := dbStoreBlock(dbTx, block); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db1.Close()
		t.Fatalf("failed to store blocks: %v", err)
	}
	for _, block := range blocks {
		node := chain1.index.LookupNode(block.Hash())
		if node == nil {
			db1.Close()
			t.Fatalf("expected header node for block %v", block.Hash())
		}
		chain1.index.SetStatusFlags(node, statusDataStored)
	}
	if err := chain1.FlushBlockIndex(); err != nil {
		db1.Close()
		t.Fatalf("failed to flush block index: %v", err)
	}
	db1.Close()

	// Restart and connect every stored block in height order through
	// ResumeBlockConnect, exactly as reconnectStoredBlocks drives it.
	db2, err := database.Open(testDbType, dbPath, blockDataNet)
	if err != nil {
		t.Fatalf("failed to reopen db: %v", err)
	}
	chain2, err := newChain(db2)
	if err != nil {
		db2.Close()
		t.Fatalf("failed to reopen chain: %v", err)
	}

	for i := int32(1); i <= 20; i++ {
		hash, err := chain2.HeaderHashByHeight(i)
		if err != nil {
			db2.Close()
			t.Fatalf("header at height %d: %v", i, err)
		}
		have, err := chain2.HaveBlock(hash)
		if err != nil {
			db2.Close()
			t.Fatalf("HaveBlock at height %d: %v", i, err)
		}
		if !have {
			db2.Close()
			t.Fatalf("stored block at height %d not reported as present", i)
		}
		if _, err := chain2.ResumeBlockConnect(hash, BFNone); err != nil {
			db2.Close()
			t.Fatalf("failed to resume-connect block at height %d: %v", i, err)
		}
	}
	if got := chain2.bestChain.Height(); got != 20 {
		db2.Close()
		t.Fatalf("best chain height = %d, want 20 after resume connect", got)
	}

	// Connecting the same block again is an idempotent no-op.  Use a block
	// whose node is retained in the in-memory window (the tip) so the guard
	// can observe it as already connected.
	if isMain, err := chain2.ResumeBlockConnect(blocks[19].Hash(), BFNone); err != nil || !isMain {
		db2.Close()
		t.Fatalf("repeated resume connect returned (%v, %v)", isMain, err)
	}

	// Resume-connecting the stored blocks advances the download cursor.
	gotHash, gotHeight := chain2.BestDownloadState()
	if gotHeight != 20 || gotHash != *blocks[19].Hash() {
		db2.Close()
		t.Fatalf("best download state after resume = %v@%d, want %v@20",
			gotHash, gotHeight, *blocks[19].Hash())
	}
	db2.Close()
	_ = os.RemoveAll(dbPath)
	_ = os.RemoveAll(testDbRoot)
}

// solveYespower finds a nonce for the provided header that satisfies the
// chain's yespower proof-of-work target, matching the PoW hash used during
// block validation.
func solveYespower(header *wire.BlockHeader) {
	target := CompactToBig(header.Bits)
	for i := uint32(0); ; i++ {
		header.Nonce = i
		hash := pow.BlockPoWHash(header)
		if HashToBig(&hash).Cmp(target) <= 0 {
			return
		}
	}
}
