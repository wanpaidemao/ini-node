// Copyright (c) 2015-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package blockchain

import (
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/database"
	"github.com/btcsuite/btcd/wire/v2"
)

// blockStatus is a bit field representing the validation state of the block.
type blockStatus byte

// blockWorkPool recycles the big.Int work sums stored on block nodes.  A node's
// work sum never aliases another node's value: initBlockNode computes it into
// its own big.Int from the parent's value, so recycling the values is free of
// side effects.
var blockWorkPool sync.Pool

// blockNodePool recycles block node structs that have been evicted from the
// in-memory block index and both chain views.  Reuse avoids per-header
// allocation and GC churn during the initial sync.  See evictWindow for the
// invariants that make returning a node to the pool safe.
var blockNodePool sync.Pool

const (
	// statusDataStored indicates that the block's payload is stored on disk.
	statusDataStored blockStatus = 1 << iota

	// statusValid indicates that the block has been fully validated.
	statusValid

	// statusValidateFailed indicates that the block has failed validation.
	statusValidateFailed

	// statusInvalidAncestor indicates that one of the block's ancestors has
	// has failed validation, thus the block is also invalid.
	statusInvalidAncestor

	// statusHeaderStored indicates that the block's header is stored on disk.
	statusHeaderStored

	// statusNone indicates that the block has no validation state flags set.
	//
	// NOTE: This must be defined last in order to avoid influencing iota.
	statusNone blockStatus = 0
)

// HaveData returns whether the full block data is stored in the database. This
// will return false for a block node where only the header is downloaded or
// kept.
func (status blockStatus) HaveData() bool {
	return status&statusDataStored != 0
}

// HaveHeader returns whether the header data is stored in the database.
func (status blockStatus) HaveHeader() bool {
	return status&statusHeaderStored != 0
}

// KnownValid returns whether the block is known to be valid. This will return
// false for a valid block that has not been fully validated yet.
func (status blockStatus) KnownValid() bool {
	return status&statusValid != 0
}

// KnownInvalid returns whether the block is known to be invalid. This may be
// because the block itself failed validation or any of its ancestors is
// invalid. This will return false for invalid blocks that have not been proven
// invalid yet.
func (status blockStatus) KnownInvalid() bool {
	return status&(statusValidateFailed|statusInvalidAncestor) != 0
}

// blockNode represents a block within the block chain and is primarily used to
// aid in selecting the best chain to be the main chain.  The main chain is
// stored into the block database.
type blockNode struct {
	// NOTE: Additions, deletions, or modifications to the order of the
	// definitions in this struct should not be changed without considering
	// how it affects alignment on 64-bit platforms.  The current order is
	// specifically crafted to result in minimal padding.  There will be
	// hundreds of thousands of these in memory, so a few extra bytes of
	// padding adds up.

	// parent is the parent block for this node.  It may be nil for nodes at
	// the lower boundary of the in-memory header window, in which case the
	// parent must be (re)materialized from disk via parentHash if needed.
	parent *blockNode

	// parentHash is the hash of the parent block.  It is always populated from
	// the header's previous block hash during construction so that a node's
	// header serialization does not depend on whether the parent pointer is
	// resolved, and so a severed parent (due to header windowing) does not
	// corrupt the reconstructed header.
	parentHash chainhash.Hash

	// ancestor is a block that is more than one block back from this node.
	ancestor *blockNode

	// hash is the double sha 256 of the block.
	hash chainhash.Hash

	// workSum is the total amount of work in the chain up to and including
	// this node.
	workSum *big.Int

	// height is the position in the block chain.
	height int32

	// Some fields from block headers to aid in best chain selection and
	// reconstructing headers from memory.  These must be treated as
	// immutable and are intentionally ordered to avoid padding on 64-bit
	// platforms.
	version    int32
	bits       uint32
	nonce      uint32
	timestamp  int64
	merkleRoot chainhash.Hash

	// status is a bitfield representing the validation state of the block. The
	// status field, unlike the other fields, may be written to and so should
	// only be accessed using the concurrent-safe NodeStatus method on
	// blockIndex once the node has been added to the global index.
	status blockStatus
}

// initBlockNode initializes a block node from the given header and parent node,
// calculating the height and workSum from the respective fields on the parent.
// This function is NOT safe for concurrent access.  It must only be called when
// initially creating a node.
func initBlockNode(node *blockNode, blockHeader *wire.BlockHeader, parent *blockNode) {
	// Recycle a work sum big.Int from the pool instead of allocating one per
	// node.  CalcWorkInto writes into the pooled value, so the only remaining
	// allocations are the transient values used to compute the work.
	ws, _ := blockWorkPool.Get().(*big.Int)
	if ws == nil {
		ws = new(big.Int)
	}

	*node = blockNode{
		hash:       blockHeader.BlockHash(),
		parentHash: blockHeader.PrevBlock,
		workSum:    CalcWorkInto(blockHeader.Bits, ws),
		version:    blockHeader.Version,
		bits:       blockHeader.Bits,
		nonce:      blockHeader.Nonce,
		timestamp:  blockHeader.Timestamp.Unix(),
		merkleRoot: blockHeader.MerkleRoot,
	}
	if parent != nil {
		node.parent = parent
		node.height = parent.height + 1
		node.workSum = node.workSum.Add(parent.workSum, node.workSum)
		node.buildAncestor()
	}
}

// newBlockNode returns a new block node for the given block header and parent
// node, calculating the height and workSum from the respective fields on the
// parent.  The node struct and its work sum are recycled from the pool when
// available. This function is NOT safe for concurrent access.
func newBlockNode(blockHeader *wire.BlockHeader, parent *blockNode) *blockNode {
	node, _ := blockNodePool.Get().(*blockNode)
	if node == nil {
		node = new(blockNode)
	}

	// A recycled node retains the work sum from its previous incarnation.
	// Return it to the work pool before initBlockNode overwrites the struct,
	// which would otherwise drop the last reference to the pooled value.
	if node.workSum != nil {
		blockWorkPool.Put(node.workSum)
	}

	initBlockNode(node, blockHeader, parent)
	return node
}

// Equals compares all the fields of the block node except for the parent and
// ancestor and returns true if they're equal.
func (node *blockNode) Equals(other *blockNode) bool {
	return node.hash == other.hash &&
		node.workSum.Cmp(other.workSum) == 0 &&
		node.height == other.height &&
		node.version == other.version &&
		node.bits == other.bits &&
		node.nonce == other.nonce &&
		node.timestamp == other.timestamp &&
		node.merkleRoot == other.merkleRoot &&
		node.status == other.status
}

// Header constructs a block header from the node and returns it.
//
// This function is safe for concurrent access.
func (node *blockNode) Header() wire.BlockHeader {
	// No lock is needed because all accessed fields are immutable.  The
	// parent hash is stored on the node so the reconstructed header stays
	// correct even when the parent pointer is severed by the header window.
	return wire.BlockHeader{
		Version:    node.version,
		PrevBlock:  node.parentHash,
		MerkleRoot: node.merkleRoot,
		Timestamp:  time.Unix(node.timestamp, 0),
		Bits:       node.bits,
		Nonce:      node.nonce,
	}
}

// invertLowestOne turns the lowest 1 bit in the binary representation of a number into a 0.
func invertLowestOne(n int32) int32 {
	return n & (n - 1)
}

// getAncestorHeight returns a suitable ancestor for the node at the given height.
func getAncestorHeight(height int32) int32 {
	// We pop off two 1 bits of the height.
	// This results in a maximum of 330 steps to go back to an ancestor
	// from height 1<<29.
	return invertLowestOne(invertLowestOne(height))
}

// buildAncestor sets an ancestor for the given blocknode.
func (node *blockNode) buildAncestor() {
	if node.parent != nil {
		node.ancestor = node.parent.Ancestor(getAncestorHeight(node.height))
	}
}

// Ancestor returns the ancestor block node at the provided height by following
// the chain backwards from this node.  The returned block will be nil when a
// height is requested that is after the height of the passed node or is less
// than zero.
//
// This function is safe for concurrent access.
func (node *blockNode) Ancestor(height int32) *blockNode {
	if height < 0 || height > node.height {
		return nil
	}

	// Traverse back until we find the desired node.
	n := node
	for n != nil && n.height != height {
		// If there's an ancestor available, use it. Otherwise, just
		// follow the parent.
		if n.ancestor != nil {
			// Calculate the height for this ancestor and
			// check if we can take the ancestor skip.
			if getAncestorHeight(n.height) >= height {
				n = n.ancestor
				continue
			}
		}

		// We couldn't take the ancestor skip so traverse back to the parent.
		n = n.parent
	}

	return n
}

// Height returns the blockNode's height in the chain.
//
// NOTE: Part of the HeaderCtx interface.
func (node *blockNode) Height() int32 {
	return node.height
}

// Bits returns the blockNode's nBits.
//
// NOTE: Part of the HeaderCtx interface.
func (node *blockNode) Bits() uint32 {
	return node.bits
}

// Timestamp returns the blockNode's timestamp.
//
// NOTE: Part of the HeaderCtx interface.
func (node *blockNode) Timestamp() int64 {
	return node.timestamp
}

// Parent returns the blockNode's parent.
//
// NOTE: Part of the HeaderCtx interface.
func (node *blockNode) Parent() HeaderCtx {
	if node.parent == nil {
		// This is required since node.parent is a *blockNode and if we
		// do not explicitly return nil here, the caller may fail when
		// nil-checking this.
		return nil
	}

	return node.parent
}

// RelativeAncestorCtx returns the blockNode's ancestor that is distance blocks
// before it in the chain. This is equivalent to the RelativeAncestor function
// below except that the return type is different.
//
// This function is safe for concurrent access.
//
// NOTE: Part of the HeaderCtx interface.
func (node *blockNode) RelativeAncestorCtx(distance int32) HeaderCtx {
	ancestor := node.RelativeAncestor(distance)
	if ancestor == nil {
		// This is required since RelativeAncestor returns a *blockNode
		// and if we do not explicitly return nil here, the caller may
		// fail when nil-checking this.
		return nil
	}

	return ancestor
}

// IsAncestor returns if the other node is an ancestor of this block node.
func (node *blockNode) IsAncestor(otherNode *blockNode) bool {
	// Return early as false if the otherNode is nil.
	if otherNode == nil {
		return false
	}

	ancestor := node.Ancestor(otherNode.height)
	if ancestor == nil {
		return false
	}

	// If the otherNode has the same height as me, then the returned
	// ancestor will be me.  Return false since I'm not an ancestor of me.
	if node.height == ancestor.height {
		return false
	}

	// Return true if the fetched ancestor is other node.
	return ancestor.Equals(otherNode)
}

// RelativeAncestor returns the ancestor block node a relative 'distance' blocks
// before this node.  This is equivalent to calling Ancestor with the node's
// height minus provided distance.
//
// This function is safe for concurrent access.
func (node *blockNode) RelativeAncestor(distance int32) *blockNode {
	return node.Ancestor(node.height - distance)
}

// CalcPastMedianTime calculates the median time of the previous few blocks
// prior to, and including, the block node.
//
// This function is safe for concurrent access.
func CalcPastMedianTime(node HeaderCtx) time.Time {
	// Create a slice of the previous few block timestamps used to calculate
	// the median per the number defined by the constant medianTimeBlocks.
	timestamps := make([]int64, medianTimeBlocks)
	numNodes := 0
	iterNode := node
	for i := 0; i < medianTimeBlocks && iterNode != nil; i++ {
		timestamps[i] = iterNode.Timestamp()
		numNodes++

		iterNode = iterNode.Parent()
	}

	// Prune the slice to the actual number of available timestamps which
	// will be fewer than desired near the beginning of the block chain
	// and sort them.
	timestamps = timestamps[:numNodes]
	sort.Sort(timeSorter(timestamps))

	// NOTE: The consensus rules incorrectly calculate the median for even
	// numbers of blocks.  A true median averages the middle two elements
	// for a set with an even number of elements in it.   Since the constant
	// for the previous number of blocks to be used is odd, this is only an
	// issue for a few blocks near the beginning of the chain.  I suspect
	// this is an optimization even though the result is slightly wrong for
	// a few of the first blocks since after the first few blocks, there
	// will always be an odd number of blocks in the set per the constant.
	//
	// This code follows suit to ensure the same rules are used, however, be
	// aware that should the medianTimeBlocks constant ever be changed to an
	// even number, this code will be wrong.
	medianTimestamp := timestamps[numNodes/2]
	return time.Unix(medianTimestamp, 0)
}

// A compile-time assertion to ensure blockNode implements the HeaderCtx
// interface.
var _ HeaderCtx = (*blockNode)(nil)

// blockIndex provides facilities for keeping track of an in-memory index of the
// block chain.  Although the name block chain suggests a single chain of
// blocks, it is actually a tree-shaped structure where any node can have
// multiple children.  However, there can only be one active branch which does
// indeed form a chain from the tip all the way back to the genesis block.
type blockIndex struct {
	// The following fields are set when the instance is created and can't
	// be changed afterwards, so there is no need to protect them with a
	// separate mutex.
	db          database.DB
	chainParams *chaincfg.Params

	// bestHeaderNode, when set, returns the current best header tip.  It is
	// used by flushToDB to persist the best header state in the same write
	// transaction that flushes the dirty block nodes, so a restart can
	// resume a header sync from the last accepted header.
	bestHeaderNode func() *blockNode

	// onEvicted, when set, is invoked with every block node removed from the
	// in-memory index by an eviction, after the nodes have been dropped from
	// both chain views but before their structs are recycled through the
	// node pool.  The BlockChain wires this to clear the cold-read cache,
	// whose entries may alias an evicted node through their parent pointer.
	onEvicted func(evicted []*blockNode)

	sync.RWMutex

	// windowSize, when greater than zero, bounds the in-memory block index
	// to the most recent windowSize blocks from each active chain tip.
	// Nodes at or above the window boundary are materialized in memory;
	// everything below it is evicted after each successful flushToDB and
	// must be re-materialized from disk on demand.  A value of zero
	// preserves the historical btcd behavior of keeping the entire block
	// index in memory.
	//
	// The remaining fields in this section are only written while holding
	// the index lock, so reads performed under the same lock (as in
	// evictWindow and flushToDB) never observe a half-updated state.
	windowSize int32

	// bestChainView and bestHeaderView are the chain views owned by the
	// BlockChain instance.  They are used to determine the current window
	// boundary and are pruned in lockstep with the index so evicted block
	// nodes are not kept alive by the views and can be reclaimed by the
	// garbage collector.
	bestChainView  *chainView
	bestHeaderView *chainView

	// ready is set once the initial block index load has completed.  Window
	// eviction is disabled until then so the startup path never races with
	// the initial materialization of the block index.
	ready bool

	// evictCount tracks the number of successful flushToDB calls since the
	// last window eviction.  Window eviction scans the entire in-memory
	// index and is O(indexSize), so it is throttled to run every
	// blockFlushBatchSize flushes rather than on every block; the per-block
	// index write itself stays small and is required for crash consistency
	// with the per-block chain-state commit in connectBlock.
	evictCount int32

	index map[chainhash.Hash]*blockNode
	dirty map[*blockNode]struct{}
}

// newBlockIndex returns a new empty instance of a block index.  The index will
// be dynamically populated as block nodes are loaded from the database and
// manually added.
func newBlockIndex(db database.DB, chainParams *chaincfg.Params) *blockIndex {
	return &blockIndex{
		db:          db,
		chainParams: chainParams,
		index:       make(map[chainhash.Hash]*blockNode),
		dirty:       make(map[*blockNode]struct{}),
	}
}

// HaveBlock returns whether or not the block index contains the provided hash
// and if the data exists on disk.
//
// This function is safe for concurrent access.
func (bi *blockIndex) HaveBlock(hash *chainhash.Hash) bool {
	bi.RLock()
	node, hasBlock := bi.index[*hash]
	haveData := hasBlock && node.status.HaveData()
	bi.RUnlock()
	return haveData
}

// LookupNode returns the block node identified by the provided hash.  It will
// return nil if there is no entry for the hash.
//
// This function is safe for concurrent access.
func (bi *blockIndex) LookupNode(hash *chainhash.Hash) *blockNode {
	bi.RLock()
	node := bi.index[*hash]
	bi.RUnlock()
	return node
}

// AddNode adds the provided node to the block index and marks it as dirty.
// Duplicate entries are not checked so it is up to caller to avoid adding them.
//
// This function is safe for concurrent access.
func (bi *blockIndex) AddNode(node *blockNode) {
	bi.Lock()
	bi.addNode(node)
	bi.dirty[node] = struct{}{}
	bi.Unlock()
}

// addNode adds the provided node to the block index, but does not mark it as
// dirty. This can be used while initializing the block index.
//
// This function is NOT safe for concurrent access.
func (bi *blockIndex) addNode(node *blockNode) {
	bi.index[node.hash] = node
}

// setWindow enables or disables the in-memory header window.  When windowSize
// is greater than zero, the block index keeps only the most recent windowSize
// blocks from each active chain tip in memory and evicts everything below that
// boundary after each successful flushToDB.  A windowSize of zero (or less)
// preserves the historical btcd behavior of keeping the entire block index in
// memory.
//
// bestChain and bestHeader are the chain views owned by the BlockChain
// instance; they are pruned in lockstep with the index so evicted block nodes
// are not kept alive by the views and can be reclaimed by the garbage
// collector.
//
// This function is safe for concurrent access.
func (bi *blockIndex) setWindow(windowSize int32, bestChain, bestHeader *chainView) {
	bi.Lock()
	bi.windowSize = windowSize
	bi.bestChainView = bestChain
	bi.bestHeaderView = bestHeader
	bi.ready = false
	bi.Unlock()
}

// markInitialized marks the block index as fully loaded so window eviction is
// enabled.  Until this is called, evictWindow is a no-op.
//
// This function is safe for concurrent access.
func (bi *blockIndex) markInitialized() {
	bi.Lock()
	bi.ready = true
	bi.Unlock()
}

// windowEnabled returns whether in-memory header windowing is active.
func (bi *blockIndex) windowEnabled() bool {
	return bi.windowSize > 0
}

// windowBoundary returns the lowest height that must stay materialized in
// memory for the provided tip height.  Nodes below the returned boundary are
// eligible for eviction.  When windowing is disabled, or the tip is shorter
// than the window, the returned boundary is zero so nothing is ever evicted.
func (bi *blockIndex) windowBoundary(tipHeight int32) int32 {
	if !bi.windowEnabled() || tipHeight <= 0 {
		return 0
	}
	if boundary := tipHeight - bi.windowSize; boundary > 0 {
		return boundary
	}
	return 0
}

// tipHeight returns the height of the given chain view's tip, or -1 when the
// view is unset or empty.
func (bi *blockIndex) tipHeight(view *chainView) int32 {
	if view == nil {
		return -1
	}
	if tip := view.Tip(); tip != nil {
		return tip.height
	}
	return -1
}

// evictWindow prunes the block index and both chain views down to the current
// in-memory header windows.  Each view keeps its own trailing window: the best
// chain view retains the most recent windowSize connected blocks (the chain
// state depends on them, and during a header sync the best chain tip may be
// far behind the header tip), while the block index and the best header view
// are bounded by the best header tip's window.  Nodes below the respective
// boundaries that are not part of the best chain view are removed from the
// index map, and the parent and ancestor pointers of any in-window node that
// still points at an evicted node are severed so the evicted structs can be
// reclaimed by the garbage collector.
//
// The parent and ancestor pointers must be severed for any ancestor that was
// evicted.  Otherwise the dangling reference would keep the evicted struct --
// and, transitively through its own parent links, the entire evicted prefix of
// the chain -- alive, defeating the window's memory bound.  The severing is
// performed while holding both the index lock and the chain view locks so it
// can never race with a concurrent chain walk, and so upward walks terminate
// at the window boundary instead of walking past the end of the materialized
// index.
//
// The node at each window boundary height itself is retained as the severed
// anchor so the chain views still resolve heights down to the boundary.
//
// This function is NOT safe for concurrent access and must be called with the
// index lock held (for writes).
func (bi *blockIndex) evictWindow() {
	if !bi.windowEnabled() || !bi.ready {
		return
	}

	chainBoundary := bi.windowBoundary(bi.tipHeight(bi.bestChainView))
	headerBoundary := bi.windowBoundary(bi.tipHeight(bi.bestHeaderView))
	if headerBoundary <= 0 {
		return
	}

	// Compute the block-connection frontier window before acquiring the chain
	// view locks, since tipHeight needs to read the views.
	chainTip := bi.tipHeight(bi.bestChainView)
	frontierLow := chainTip - bi.windowSize
	frontierHigh := chainTip + bi.windowSize

	// Grab the chain view locks so the eviction and pointer severing below
	// cannot race with a concurrent chain walk.
	if bi.bestChainView != nil {
		bi.bestChainView.mtx.Lock()
	}
	if bi.bestHeaderView != nil {
		bi.bestHeaderView.mtx.Lock()
	}

	// Remove every node strictly below the header window boundary from the
	// index map, except nodes that are part of the best chain view.  The
	// connected chain's trailing window is allowed to sit below the header
	// window boundary during a header sync and must stay materialized.
	//
	// Additionally protect the block-connection frontier.  During an initial
	// block download the connected chain tip sits far below the header
	// window boundary, and both the node currently being connected (which
	// has not entered the best chain view yet) and the header nodes for the
	// blocks about to be requested would otherwise be evicted on the very
	// flush that stores them, making their parents unresolvable ("previous
	// block ... is unknown").  Nodes within one window of the best chain tip
	// are therefore retained alongside the header window; this keeps the
	// download hot without blowing up the memory bound, since the frontier
	// window slides with the tip as blocks connect.
	//
	// Evicted nodes are collected so they can be recycled through the node
	// pool after the chain views have been pruned below their boundaries.
	// At that point the invariants required for safe reuse all hold: the
	// node has been removed from the index map, it is no longer referenced
	// by either chain view (bestHeader only spans its own window and any
	// best-chain node was retained via the contains check), the dirty set
	// was cleared by finishFlushLocked before eviction, and any in-window
	// node whose parent/ancestor pointed at it was severed below.
	var evicted []*blockNode
	if bi.bestChainView != nil {
		for hash, node := range bi.index {
			if node.height < headerBoundary && !bi.bestChainView.contains(node) {
				if node.height >= frontierLow && node.height <= frontierHigh {
					continue
				}
				delete(bi.index, hash)
				evicted = append(evicted, node)
			}
		}
	} else {
		for hash, node := range bi.index {
			if node.height < headerBoundary {
				if node.height >= frontierLow && node.height <= frontierHigh {
					continue
				}
				delete(bi.index, hash)
				evicted = append(evicted, node)
			}
		}
	}

	// Sever the parent and ancestor pointers of any in-window node that
	// still points below the boundary applicable to it.  This is done under
	// the chain view locks so concurrent chain walks never observe a
	// partially severed view, and the boundary nodes are covered even though
	// they may not be reachable through either view (a side chain that has
	// fallen out of the window).
	for _, node := range bi.index {
		bound := headerBoundary
		// The best chain view and the block-connection frontier below the
		// header window boundary must keep their ancestor chains intact so
		// context validation (median time past, BIP9 state) can walk them
		// while the next blocks connect; they are severed only below the
		// best chain's own window boundary.  Nodes at or above the header
		// boundary (the header window proper) are still severed below it.
		if bi.bestChainView != nil &&
			(bi.bestChainView.contains(node) ||
				(node.height < headerBoundary && node.height >= frontierLow)) {

			bound = chainBoundary
		}
		if node.parent != nil && node.parent.height < bound {
			node.parent = nil
		}
		if node.ancestor != nil && node.ancestor.height < bound {
			node.ancestor = nil
		}
	}

	// Prune the chain views below their respective boundaries in lockstep so
	// they no longer reference the evicted structs.
	if bi.bestChainView != nil {
		bi.bestChainView.pruneBelow(chainBoundary)
	}
	if bi.bestHeaderView != nil {
		bi.bestHeaderView.pruneBelow(headerBoundary)
	}

	// Recycle the evicted nodes.  The cold-read cache is cleared first (it
	// may hold entries whose parent aliases an evicted in-memory node) so no
	// cached node can observe a recycled struct through its parent pointer.
	// The work sums are returned to their pool before the structs so a reuse
	// never resurrects a pooled value as its own.  The workSum pointer is
	// then nulled out on the struct: the value now belongs to the pool, and
	// leaving the reference in place would cause the same big.Int to be
	// returned to the pool twice (here and again when the struct is reused),
	// so two live nodes could end up aliasing a single work sum.
	if len(evicted) > 0 && bi.onEvicted != nil {
		bi.onEvicted(evicted)
	}
	for _, node := range evicted {
		if node.workSum != nil {
			blockWorkPool.Put(node.workSum)
			node.workSum = nil
		}
		blockNodePool.Put(node)
	}

	if bi.bestHeaderView != nil {
		bi.bestHeaderView.mtx.Unlock()
	}
	if bi.bestChainView != nil {
		bi.bestChainView.mtx.Unlock()
	}
}

// NodeStatus provides concurrent-safe access to the status field of a node.
//
// This function is safe for concurrent access.
func (bi *blockIndex) NodeStatus(node *blockNode) blockStatus {
	bi.RLock()
	status := node.status
	bi.RUnlock()
	return status
}

// SetStatusFlags flips the provided status flags on the block node to on,
// regardless of whether they were on or off previously. This does not unset any
// flags currently on.
//
// This function is safe for concurrent access.
func (bi *blockIndex) SetStatusFlags(node *blockNode, flags blockStatus) {
	bi.Lock()
	node.status |= flags
	bi.dirty[node] = struct{}{}
	bi.Unlock()
}

// UnsetStatusFlags flips the provided status flags on the block node to off,
// regardless of whether they were on or off previously.
//
// This function is safe for concurrent access.
func (bi *blockIndex) UnsetStatusFlags(node *blockNode, flags blockStatus) {
	bi.Lock()
	node.status &^= flags
	bi.dirty[node] = struct{}{}
	bi.Unlock()
}

// InactiveTips returns all the block nodes that aren't in the best chain.
//
// This function is safe for concurrent access.
func (bi *blockIndex) InactiveTips(bestChain *chainView) []*blockNode {
	bi.RLock()
	defer bi.RUnlock()

	// Look through the entire blockindex and look for nodes that aren't in
	// the best chain. We're gonna keep track of all the orphans and the parents
	// of the orphans.
	orphans := make(map[chainhash.Hash]*blockNode)
	orphanParent := make(map[chainhash.Hash]*blockNode)
	for hash, node := range bi.index {
		found := bestChain.Contains(node)
		if !found {
			orphans[hash] = node
			// A node may have a severed parent when it sits at the
			// boundary of the in-memory header window.  Its true parent
			// lives below the window and is handled by the caller's
			// orphan resolution, so simply skip tracking it here.
			if node.parent != nil {
				orphanParent[node.parent.hash] = node.parent
			}
		}
	}

	// If an orphan isn't pointed to by another orphan, it is a chain tip.
	//
	// We can check this by looking for the orphan in the orphan parent map.
	// If the orphan exists in the orphan parent map, it means that another
	// orphan is pointing to it.
	tips := make([]*blockNode, 0, len(orphans))
	for hash, orphan := range orphans {
		_, found := orphanParent[hash]
		if !found {
			tips = append(tips, orphan)
		}

		delete(orphanParent, hash)
	}

	return tips
}

// flushToDB writes all dirty block nodes to the database. If all writes
// succeed, this clears the dirty set.
//
// Header-only nodes are written as well (including a hash -> height index
// entry) so that a restart can resume the header sync from the last written
// header instead of re-downloading every header from genesis.  This changes
// the historical btcd behavior documented in the NOTE that was removed here:
// older btcd versions assume a blockNode being present means the block data is
// available as well.  That assumption is only relied upon by initChainState
// when converting an old db without the best header state key, and the new
// startup path never uses header-only block nodes as a source of block data.
//
// forceEvict runs window eviction immediately after the flush regardless of
// the throttling counter.  It must be set for the batched header-sync flush:
// that flush fires only once every headerFlushBatchSize headers, so the
// blockFlushBatchSize throttle would otherwise let the in-memory index grow by
// blockFlushBatchSize*headerFlushBatchSize (10M) nodes between evictions,
// blowing past the --headerwindow memory bound.
func (bi *blockIndex) flushToDB(forceEvict bool) error {
	bi.Lock()
	if len(bi.dirty) == 0 {
		bi.Unlock()
		return nil
	}
	err := bi.db.Update(func(dbTx database.Tx) error {
		return bi.flushDirtyLocked(dbTx)
	})
	if err == nil {
		bi.finishFlushLocked(forceEvict)
	}
	bi.Unlock()
	return err
}

// flushToDBTx writes all dirty block nodes into the provided database
// transaction without committing it.  The caller is responsible for committing
// the transaction and, once the transaction has committed, for calling
// finishFlush to clear the in-memory dirty set and run throttled window
// eviction.  This lets connectBlock fold the per-block block-index write into
// the same transaction that commits the chain state (best state, spend journal,
// UTXO consistency), so each block costs a single database commit instead of
// two, without ever committing a chain state ahead of the block index rows it
// points at.
//
// This method must be called with bi.Lock held.
func (bi *blockIndex) flushDirtyLocked(dbTx database.Tx) error {
	if len(bi.dirty) == 0 {
		return nil
	}
	for node := range bi.dirty {
		if err := dbStoreBlockNode(dbTx, node); err != nil {
			return err
		}
		if err := dbPutHashIndex(dbTx, &node.hash, node.height); err != nil {
			return err
		}

		// Maintain the height-to-hash mapping for nodes on the best header
		// chain so the DB cold-read fallback can resolve evicted heights.
		// Side-chain rows are skipped since they must never claim a
		// main-chain height, and rows below the window are no longer dirty
		// because an earlier flush already persisted them.
		if bi.bestHeaderView != nil && bi.bestHeaderView.Contains(node) {
			if err := dbPutHeightIndex(dbTx, node.height, &node.hash); err != nil {
				return err
			}
		}
	}

	// Persist the best header state in the same transaction so the stored
	// header tip is always consistent with the written block index rows.
	if bi.bestHeaderNode != nil {
		if tip := bi.bestHeaderNode(); tip != nil {
			if err := dbPutBestHeaderState(dbTx, &tip.hash, tip.height); err != nil {
				return err
			}
		}
	}
	return nil
}

// finalizeFlushLocked must be called (with bi.Lock held) after dirty block
// nodes have been committed, whether through flushToDB's own transaction or
// through flushDirtyTx + an external commit.  It clears the dirty set and evicts
// nodes that have fallen out of the in-memory header window.
//
// Eviction is also throttled: it scans the entire in-memory index and is
// O(indexSize), so deferring it to run only once every blockFlushBatchSize
// flushes removes the per-block O(indexSize) scan during initial block download
// without giving up crash consistency (which the per-block write preserves).
//
// When forceEvict is true the throttle is bypassed and eviction runs on every
// flush.  The batched header-sync flush passes true because its flush cadence
// (every headerFlushBatchSize headers) is far too coarse for the block-oriented
// throttle, and the index must be trimmed back to the window on each batch to
// keep header sync memory bounded.
func (bi *blockIndex) finishFlushLocked(forceEvict bool) {
	bi.dirty = make(map[*blockNode]struct{})
	if forceEvict {
		bi.evictWindow()
		return
	}
	bi.evictCount++
	if bi.evictCount >= blockFlushBatchSize {
		bi.evictCount = 0
		bi.evictWindow()
	}
}
