// Copyright (c) 2013-2018 The btcsuite developers
// Copyright (c) 2015-2018 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package blockchain

import (
	"math/big"
	"sync"

	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/database"
)

// coldCacheSize is the maximum number of temporarily-materialized cold block
// nodes kept around for serving queries against heights that fall outside the
// in-memory header window.  It only needs to cover the working set of a single
// request (for example a P2P header/block serve or an RPC lookup), so a small
// bound is sufficient.
const coldCacheSize = 64

// coldNodeCache provides a small FIFO-evicted cache of block nodes that have
// been rebuilt from the database.  Keeping the identity of a cold node stable
// across lookups means a height walk (such as serving a range of blocks or
// headers to a peer) observes one node per hash rather than a fresh allocation
// per call.
//
// This type is safe for concurrent access.
type coldNodeCache struct {
	sync.Mutex
	order []chainhash.Hash
	nodes map[chainhash.Hash]*blockNode
}

// newColdNodeCache returns an empty cold node cache.
func newColdNodeCache() coldNodeCache {
	return coldNodeCache{
		nodes: make(map[chainhash.Hash]*blockNode),
	}
}

// get returns the cached node for the provided hash, or nil when absent.
func (c *coldNodeCache) get(hash *chainhash.Hash) *blockNode {
	c.Lock()
	defer c.Unlock()

	return c.nodes[*hash]
}

// put caches the provided node, evicting the oldest entry when the cache is
// full.  A node already cached for the same hash is left untouched.
func (c *coldNodeCache) put(node *blockNode) {
	c.Lock()
	defer c.Unlock()

	if _, exists := c.nodes[node.hash]; exists {
		return
	}

	c.nodes[node.hash] = node
	c.order = append(c.order, node.hash)
	if len(c.order) > coldCacheSize {
		evicted := c.order[0]
		c.order = c.order[1:]
		delete(c.nodes, evicted)
	}
}

// reset drops every cached cold node.  The BlockChain invokes it after each
// window eviction because a cached node may alias an evicted in-memory node
// through its parent pointer, and once that parent is recycled through the
// block node pool it would reference a node representing a different block.
func (c *coldNodeCache) reset() {
	c.Lock()
	c.order = c.order[:0]
	c.nodes = make(map[chainhash.Hash]*blockNode)
	c.Unlock()
}

// materializeColdNode builds a temporary block node for the provided hash by
// reading the two-hop block index, without touching the block file.  It returns
// nil when the hash is not indexed.
//
// The returned node carries its header, height, status and parent hash but has
// a severed parent pointer and a zero cumulative work sum, so it must not be
// used for proof-of-work comparisons or pointer-based chain view membership.
// It exists solely to serve hash/height/header queries for blocks outside the
// in-memory header window.
//
// coldReadEnabled reports whether the cold-read layer is operational.  It
// requires both a backing database and an in-memory header window; when either
// is absent there is nothing to page back in from disk, so cold lookups must
// report a miss.  The header window is fixed at construction time.
func (b *BlockChain) coldReadEnabled() bool {
	return b.db != nil && b.headerWindow != 0
}

// This function is safe for concurrent access.
func (b *BlockChain) materializeColdNode(hash *chainhash.Hash) *blockNode {
	if !b.coldReadEnabled() || hash == nil {
		return nil
	}

	return b.materializeColdNodeWithTx(hash, b.db.View)
}

// materializeColdNodeWithTx builds a temporary block node for the provided hash
// by reading the two-hop block index via the supplied read function, without
// touching the block file.  It returns nil when the hash is not indexed.
//
// The read function is invoked lazily (on a cache miss) and is passed a closure
// that performs the actual database row read; callers that already hold an open
// database view can run the closure against it, while the single-lookup path
// supplies the database View method directly so one height never opens more
// than a single read transaction.
//
// See materializeColdNode for a description of the returned node's properties.
//
// This function is safe for concurrent access.
func (b *BlockChain) materializeColdNodeWithTx(hash *chainhash.Hash,
	read func(func(database.Tx) error) error) *blockNode {

	if hash == nil {
		return nil
	}

	// Preserve pointer identity: a node that is still present in the in-memory
	// block index must be returned as the same pointer rather than duplicated as
	// a cold rebuild, so that reorg/fork decisions that rely on pointer equality
	// never see two distinct nodes with the same hash.  This check runs before
	// the cold cache so an in-memory node always wins over a stale cold entry.
	if node := b.index.LookupNode(hash); node != nil {
		return node
	}

	if cached := b.coldCache.get(hash); cached != nil {
		return cached
	}

	var node *blockNode
	err := read(func(dbTx database.Tx) error {
		header, status, height, err := dbFetchBlockRowByHash(dbTx, hash)
		if err != nil {
			return err
		}
		if header == nil {
			return nil
		}

		node = &blockNode{
			hash:       *hash,
			parentHash: header.PrevBlock,
			workSum:    new(big.Int),
			height:     height,
			status:     status,
			version:    header.Version,
			bits:       header.Bits,
			nonce:      header.Nonce,
			timestamp:  header.Timestamp.Unix(),
			merkleRoot: header.MerkleRoot,
		}
		return nil
	})
	if err != nil || node == nil {
		return nil
	}

	// Resolve a parent when it is still present in the in-memory index so the
	// node behaves like a regular server node as far as possible.
	//
	// The parent lookup and the cache insertion are performed under the index
	// read lock so a concurrent window eviction (which holds the index write
	// lock through both the index sweep and the node recycling) can never
	// recycle the parent between the lookup and the insertion.  Without this
	// the cached node could alias a recycled struct through its parent
	// pointer.
	b.index.RLock()
	if parent := b.index.index[node.parentHash]; parent != nil {
		node.parent = parent
	}
	b.coldCache.put(node)
	b.index.RUnlock()
	return node
}

// coldNodeAtHeight returns a temporarily-materialized block node for the
// main-chain block at the provided height, or nil when the height is unknown.
//
// This function is safe for concurrent access.
func (b *BlockChain) coldNodeAtHeight(height int32) *blockNode {
	if !b.coldReadEnabled() {
		return nil
	}

	var node *blockNode
	err := b.db.View(func(dbTx database.Tx) error {
		hash, err := dbFetchHashByHeight(dbTx, height)
		if err != nil {
			return err
		}

		node = b.materializeColdNodeWithTx(hash,
			func(fn func(database.Tx) error) error { return fn(dbTx) })
		return nil
	})
	if err != nil || node == nil {
		return nil
	}

	return node
}

// nodeAtHeight returns the main-chain block node at the requested height,
// serving from the in-memory header window when the height is retained and
// falling back to a lazily-cached cold materialization otherwise.  It returns
// nil when the height is unknown.
//
// This function is safe for concurrent access.
func (b *BlockChain) nodeAtHeight(height int32) *blockNode {
	if node := b.bestChain.NodeByHeight(height); node != nil {
		return node
	}

	return b.coldNodeAtHeight(height)
}

// nodesInHeightRange returns the main-chain block nodes for the inclusive
// height range [startHeight, endHeight].  Heights retained in the in-memory
// best chain window are served directly from the view; every remaining height
// is materialized through the cold-read layer within a single database view so
// serving a contiguous evicted range does not open one transaction per height.
// Heights unknown on the main chain yield nil entries.
//
// This function is safe for concurrent access.
func (b *BlockChain) nodesInHeightRange(startHeight, endHeight int32) []*blockNode {
	if endHeight < startHeight {
		return nil
	}

	nodes := make([]*blockNode, 0, endHeight-startHeight+1)

	b.bestChain.mtx.Lock()
	for h := startHeight; h <= endHeight; h++ {
		nodes = append(nodes, b.bestChain.nodeByHeight(h))
	}
	b.bestChain.mtx.Unlock()

	if !b.coldReadEnabled() {
		return nodes
	}

	// Materialize every height missing from the in-memory window in a single
	// read transaction.
	err := b.db.View(func(dbTx database.Tx) error {
		for i, node := range nodes {
			if node != nil {
				continue
			}

			h := startHeight + int32(i)
			hash, err := dbFetchHashByHeight(dbTx, h)
			if err != nil {
				// Not on the main chain; leave the slot nil.
				continue
			}

			nodes[i] = b.materializeColdNodeWithTx(hash,
				func(fn func(database.Tx) error) error { return fn(dbTx) })
		}
		return nil
	})
	if err != nil {
		return nil
	}

	return nodes
}

// isMainChainHash reports whether the provided hash is the block stored at its
// own height in the main chain's height index.
//
// This function is safe for concurrent access.
func (b *BlockChain) isMainChainHash(hash *chainhash.Hash) bool {
	if !b.coldReadEnabled() || hash == nil {
		return false
	}

	var dbHash *chainhash.Hash
	err := b.db.View(func(dbTx database.Tx) error {
		height, err := dbFetchHeightByHash(dbTx, hash)
		if err != nil {
			return err
		}

		var err2 error
		dbHash, err2 = dbFetchHashByHeight(dbTx, height)
		return err2
	})
	if err != nil || dbHash == nil {
		return false
	}

	return *dbHash == *hash
}

// repairDifficultyChain re-links, from the cold index, any parent pointers in
// the ancestor chain of the provided node that a window eviction severed, so
// consensus walks (the SugarShield difficulty calculation and its median-time
// comparisons) can always traverse the full chain.  Without this, a valid
// block whose ancestors fell out of the in-memory header window would have its
// expected difficulty computed as the PowLimit and be falsely rejected.
//
// This function MUST be called with the chain lock held (for writes): it
// mutates parent pointers of in-memory index nodes, and window eviction (the
// only other parent-pointer writer) also runs under the chain lock via
// flushToDB, so the two can never race.
func (b *BlockChain) repairDifficultyChain(node *blockNode) *blockNode {
	return b.repairAncestorChain(node, difficultyProtectDepth)
}

// repairAncestorChain re-links, from the cold index, any parent pointers in
// the ancestor chain of the provided node that a window eviction severed, up
// to depth levels.  It is the general form of repairDifficultyChain, used by
// every consensus walk that traverses parent pointers further back than the
// difficulty window -- most notably the BIP9 threshold-state calculation,
// which walks back a full miner confirmation window (MinerConfirmationWindow)
// while counting votes.  Without this repair that walk would hit a severed
// parent pointer and dereference a nil node, crashing the node during block
// connection.
//
// The walk stops once it reaches a live in-memory link, so intact chains are
// untouched and a repair is a no-op.  Every severed node carries its parent
// hash (parentHash is immutable, populated at construction), so the true
// parent is always resolvable from the hash-keyed block index on disk.  A
// materialized parent may itself have a severed parent, so the walk continues
// from the materialized node, chaining cold nodes until the required depth or
// a live link is reached.
//
// This function MUST be called with the chain lock held (for writes): it
// mutates parent pointers of in-memory index nodes, and window eviction (the
// only other parent-pointer writer) also runs under the chain lock via
// flushToDB, so the two can never race.
func (b *BlockChain) repairAncestorChain(node *blockNode, depth int32) *blockNode {
	if node == nil {
		return nil
	}

	cur := node
	for i := int32(0); i < depth && cur != nil; i++ {
		if cur.parent != nil {
			cur = cur.parent
			continue
		}

		// Severed link: resolve the true parent by its hash.  The genesis
		// block has a zero parent hash and terminates the walk naturally.
		if cur.parentHash == (chainhash.Hash{}) {
			return node
		}
		parent := b.materializeColdNode(&cur.parentHash)
		if parent == nil {
			// Not indexed (cannot happen for an accepted header); leave the
			// link severed and let the caller fall back to its previous
			// behavior.
			return node
		}
		cur.parent = parent
		cur = parent
	}

	return node
}
