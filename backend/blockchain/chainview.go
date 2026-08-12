// Copyright (c) 2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package blockchain

import (
	"sync"
)

// approxNodesPerWeek is an approximation of the number of new blocks there are
// in a week on average.
const approxNodesPerWeek = 6 * 24 * 7

// log2FloorMasks defines the masks to use when quickly calculating
// floor(log2(x)) in a constant log2(32) = 5 steps, where x is a uint32, using
// shifts.  They are derived from (2^(2^x) - 1) * (2^(2^x)), for x in 4..0.
var log2FloorMasks = []uint32{0xffff0000, 0xff00, 0xf0, 0xc, 0x2}

// fastLog2Floor calculates and returns floor(log2(x)) in a constant 5 steps.
func fastLog2Floor(n uint32) uint8 {
	rv := uint8(0)
	exponent := uint8(16)
	for i := 0; i < 5; i++ {
		if n&log2FloorMasks[i] != 0 {
			rv += exponent
			n >>= exponent
		}
		exponent >>= 1
	}
	return rv
}

// chainView provides a flat view of a specific branch of the block chain from
// its tip back to the genesis block and provides various convenience functions
// for comparing chains.
//
// For example, assume a block chain with a side chain as depicted below:
//
//	genesis -> 1 -> 2 -> 3 -> 4  -> 5 ->  6  -> 7  -> 8
//	                      \-> 4a -> 5a -> 6a
//
// The chain view for the branch ending in 6a consists of:
//
//	genesis -> 1 -> 2 -> 3 -> 4a -> 5a -> 6a
//
// When the in-memory header window is active the view only materializes the
// most recent part of the chain.  base is the absolute height of the node
// stored at index zero of the nodes slice, so the slice covers the absolute
// heights [base, base+len(nodes)).  Pruning the view (see pruneBelow) drops the
// prefix below the window and advances base, which keeps the backing array
// sized to the window rather than to the full chain height.
type chainView struct {
	mtx   sync.Mutex
	nodes []*blockNode
	base  int32
}

// newChainView returns a new chain view for the given tip block node.  Passing
// nil as the tip will result in a chain view that is not initialized.  The tip
// can be updated at any time via the setTip function.
func newChainView(tip *blockNode) *chainView {
	// The mutex is intentionally not held since this is a constructor.
	var c chainView
	c.setTip(tip)
	return &c
}

// genesis returns the oldest block retained by the chain view.  This only
// differs from the exported version in that it is up to the caller to ensure
// the lock is held.
//
// When the view is not windowed this is the true genesis block; when the
// in-memory header window is active it is the window boundary anchor instead,
// which is the lowest height the view can resolve.
//
// This function MUST be called with the view mutex locked (for reads).
func (c *chainView) genesis() *blockNode {
	if len(c.nodes) == 0 {
		return nil
	}

	return c.nodes[0]
}

// Genesis returns the genesis block for the chain view.
//
// This function is safe for concurrent access.
func (c *chainView) Genesis() *blockNode {
	c.mtx.Lock()
	genesis := c.genesis()
	c.mtx.Unlock()
	return genesis
}

// tip returns the current tip block node for the chain view.  It will return
// nil if there is no tip.  This only differs from the exported version in that
// it is up to the caller to ensure the lock is held.
//
// This function MUST be called with the view mutex locked (for reads).
func (c *chainView) tip() *blockNode {
	if len(c.nodes) == 0 {
		return nil
	}

	return c.nodes[len(c.nodes)-1]
}

// Tip returns the current tip block node for the chain view.  It will return
// nil if there is no tip.
//
// This function is safe for concurrent access.
func (c *chainView) Tip() *blockNode {
	c.mtx.Lock()
	tip := c.tip()
	c.mtx.Unlock()
	return tip
}

// setTip sets the chain view to use the provided block node as the current tip
// and ensures the view is consistent by populating it with the nodes obtained
// by walking backwards through the retained window as necessary.  Further calls
// will only perform the minimum work needed, so switching between chain tips is
// efficient.  This only differs from the exported version in that it is up to
// the caller to ensure the lock is held.
//
// This function MUST be called with the view mutex locked (for writes).
func (c *chainView) setTip(node *blockNode) {
	if node == nil {
		// Keep the backing array around for potential future use.
		c.nodes = c.nodes[:0]
		c.base = 0
		return
	}

	// A tip below the materialized window base cannot be represented by the
	// view.  This should never happen for the views maintained by the chain
	// (tips are always extensions of, or reorganizations near, the current
	// tip), but guard against it so a walk can never index a negative
	// offset.
	if node.height < c.base {
		return
	}

	// Create or resize the slice that will hold the block nodes for the
	// window from the view base through the provided tip height.  When
	// creating the slice, it is created with some additional capacity for
	// the underlying array as append would do in order to reduce overhead
	// when extending the chain later.  As long as the underlying array
	// already has enough capacity, simply expand or contract the slice
	// accordingly.  The additional capacity is chosen such that the array
	// should only have to be extended about once a week.
	needed := node.height - c.base + 1
	if int32(cap(c.nodes)) < needed {
		nodes := make([]*blockNode, needed, needed+approxNodesPerWeek)
		copy(nodes, c.nodes)
		c.nodes = nodes
	} else {
		prevLen := int32(len(c.nodes))
		c.nodes = c.nodes[0:needed]
		for i := prevLen; i < needed; i++ {
			c.nodes[i] = nil
		}
	}

	for node != nil && node.height >= c.base && c.nodes[node.height-c.base] != node {
		c.nodes[node.height-c.base] = node
		node = node.parent
	}
}

// SetTip sets the chain view to use the provided block node as the current tip
// and ensures the view is consistent by populating it with the nodes obtained
// by walking backwards all the way to genesis block as necessary.  Further
// calls will only perform the minimum work needed, so switching between chain
// tips is efficient.
//
// This function is safe for concurrent access.
func (c *chainView) SetTip(node *blockNode) {
	c.mtx.Lock()
	c.setTip(node)
	c.mtx.Unlock()
}

// pruneBelow drops every node strictly below the provided height from the
// chain view and severs the parent and ancestor pointers of the node at the
// boundary height so upward walks terminate there instead of walking past the
// end of the materialized window.  Nodes below the boundary become
// unreachable from the view and can be garbage collected once they are also
// removed from the block index.
//
// The retained window is compacted to the front of the backing array and the
// view base is advanced, so the array only needs to cover the in-memory window
// rather than the full chain height.
//
// This only differs from the exported version in that it is up to the caller
// to ensure the lock is held.
//
// This function MUST be called with the view mutex locked (for writes).
func (c *chainView) pruneBelow(height int32) {
	// The requested boundary sits at or below the current window base, so
	// every height below it has already been dropped.
	if height <= c.base {
		return
	}

	// Drop every height strictly below the provided height.  When the
	// boundary reaches or passes the tip, the view is left empty.
	drop := height - c.base
	if drop >= int32(len(c.nodes)) {
		c.nodes = c.nodes[:0]
		c.base = height
		return
	}

	// Sever the parent and ancestor of the node that now sits at the window
	// boundary so upward walks terminate there and so the evicted prefix of
	// the chain is not kept alive through those back-references.
	if node := c.nodes[drop]; node != nil {
		node.parent = nil
		node.ancestor = nil
	}

	// Compact the retained window to the front of the backing array and
	// advance the view base.  Reallocating the array to the retained size
	// also releases the memory that previously covered the evicted prefix.
	retained := int32(len(c.nodes)) - drop
	nodes := make([]*blockNode, retained, retained+approxNodesPerWeek)
	copy(nodes, c.nodes[drop:])
	c.nodes = nodes
	c.base = height
}

// PruneBelow drops every node strictly below the provided height from the
// chain view and severs the back-references of the boundary node.  This is
// used to keep the view consistent with an in-memory header window.
//
// This function is safe for concurrent access.
func (c *chainView) PruneBelow(height int32) {
	c.mtx.Lock()
	c.pruneBelow(height)
	c.mtx.Unlock()
}

// height returns the height of the tip of the chain view.  It will return -1 if
// there is no tip (which only happens if the chain view has not been
// initialized).  This only differs from the exported version in that it is up
// to the caller to ensure the lock is held.
//
// This function MUST be called with the view mutex locked (for reads).
func (c *chainView) height() int32 {
	if len(c.nodes) == 0 {
		return -1
	}

	return c.base + int32(len(c.nodes)) - 1
}

// Height returns the height of the tip of the chain view.  It will return -1 if
// there is no tip (which only happens if the chain view has not been
// initialized).
//
// This function is safe for concurrent access.
func (c *chainView) Height() int32 {
	c.mtx.Lock()
	height := c.height()
	c.mtx.Unlock()
	return height
}

// nodeByHeight returns the block node at the specified height.  Nil will be
// returned if the height does not exist.  This only differs from the exported
// version in that it is up to the caller to ensure the lock is held.
//
// This function MUST be called with the view mutex locked (for reads).
func (c *chainView) nodeByHeight(height int32) *blockNode {
	if height < c.base || height >= c.base+int32(len(c.nodes)) {
		return nil
	}

	return c.nodes[height-c.base]
}

// NodeByHeight returns the block node at the specified height.  Nil will be
// returned if the height does not exist.
//
// This function is safe for concurrent access.
func (c *chainView) NodeByHeight(height int32) *blockNode {
	c.mtx.Lock()
	node := c.nodeByHeight(height)
	c.mtx.Unlock()
	return node
}

// Equals returns whether or not two chain views are the same.  Uninitialized
// views (tip set to nil) are considered equal.
//
// This function is safe for concurrent access.
func (c *chainView) Equals(other *chainView) bool {
	c.mtx.Lock()
	other.mtx.Lock()
	equals := len(c.nodes) == len(other.nodes) && c.tip() == other.tip()
	other.mtx.Unlock()
	c.mtx.Unlock()
	return equals
}

// contains returns whether or not the chain view contains the passed block
// node.  This only differs from the exported version in that it is up to the
// caller to ensure the lock is held.
//
// This function MUST be called with the view mutex locked (for reads).
func (c *chainView) contains(node *blockNode) bool {
	return c.nodeByHeight(node.height) == node
}

// Contains returns whether or not the chain view contains the passed block
// node.
//
// This function is safe for concurrent access.
func (c *chainView) Contains(node *blockNode) bool {
	c.mtx.Lock()
	contains := c.contains(node)
	c.mtx.Unlock()
	return contains
}

// next returns the successor to the provided node for the chain view.  It will
// return nil if there is no successor or the provided node is not part of the
// view.  This only differs from the exported version in that it is up to the
// caller to ensure the lock is held.
//
// See the comment on the exported function for more details.
//
// This function MUST be called with the view mutex locked (for reads).
func (c *chainView) next(node *blockNode) *blockNode {
	if node == nil || !c.contains(node) {
		return nil
	}

	return c.nodeByHeight(node.height + 1)
}

// Next returns the successor to the provided node for the chain view.  It will
// return nil if there is no successfor or the provided node is not part of the
// view.
//
// For example, assume a block chain with a side chain as depicted below:
//
//	genesis -> 1 -> 2 -> 3 -> 4  -> 5 ->  6  -> 7  -> 8
//	                      \-> 4a -> 5a -> 6a
//
// Further, assume the view is for the longer chain depicted above.  That is to
// say it consists of:
//
//	genesis -> 1 -> 2 -> 3 -> 4 -> 5 -> 6 -> 7 -> 8
//
// Invoking this function with block node 5 would return block node 6 while
// invoking it with block node 5a would return nil since that node is not part
// of the view.
//
// This function is safe for concurrent access.
func (c *chainView) Next(node *blockNode) *blockNode {
	c.mtx.Lock()
	next := c.next(node)
	c.mtx.Unlock()
	return next
}

// findFork returns the final common block between the provided node and the
// the chain view.  It will return nil if there is no common block.  This only
// differs from the exported version in that it is up to the caller to ensure
// the lock is held.
//
// See the exported FindFork comments for more details.
//
// This function MUST be called with the view mutex locked (for reads).
func (c *chainView) findFork(node *blockNode) *blockNode {
	// No fork point for node that doesn't exist.
	if node == nil {
		return nil
	}

	// When the height of the passed node is higher than the height of the
	// tip of the current chain view, walk backwards through the nodes of
	// the other chain until the heights match (or there or no more nodes in
	// which case there is no common node between the two).
	//
	// NOTE: This isn't strictly necessary as the following section will
	// find the node as well, however, it is more efficient to avoid the
	// contains check since it is already known that the common node can't
	// possibly be past the end of the current chain view.  It also allows
	// this code to take advantage of any potential future optimizations to
	// the Ancestor function such as using an O(log n) skip list.
	chainHeight := c.height()
	if node.height > chainHeight {
		node = node.Ancestor(chainHeight)
	}

	// Walk the other chain backwards as long as the current one does not
	// contain the node or there are no more nodes in which case there is no
	// common node between the two.
	for node != nil && !c.contains(node) {
		node = node.parent
	}

	return node
}

// FindFork returns the final common block between the provided node and the
// the chain view.  It will return nil if there is no common block.
//
// For example, assume a block chain with a side chain as depicted below:
//
//	genesis -> 1 -> 2 -> ... -> 5 -> 6  -> 7  -> 8
//	                             \-> 6a -> 7a
//
// Further, assume the view is for the longer chain depicted above.  That is to
// say it consists of:
//
//	genesis -> 1 -> 2 -> ... -> 5 -> 6 -> 7 -> 8.
//
// Invoking this function with block node 7a would return block node 5 while
// invoking it with block node 7 would return itself since it is already part of
// the branch formed by the view.
//
// This function is safe for concurrent access.
func (c *chainView) FindFork(node *blockNode) *blockNode {
	c.mtx.Lock()
	fork := c.findFork(node)
	c.mtx.Unlock()
	return fork
}

// blockLocator returns a block locator for the passed block node.  The passed
// node can be nil in which case the block locator for the current tip
// associated with the view will be returned.  This only differs from the
// exported version in that it is up to the caller to ensure the lock is held.
//
// See the exported BlockLocator function comments for more details.
//
// This function MUST be called with the view mutex locked (for reads).
func (c *chainView) blockLocator(node *blockNode) BlockLocator {
	// Use the current tip if requested.
	if node == nil {
		node = c.tip()
	}
	if node == nil {
		return nil
	}

	// Calculate the max number of entries that will ultimately be in the
	// block locator.  See the description of the algorithm for how these
	// numbers are derived.
	var maxEntries uint8
	if node.height <= 12 {
		maxEntries = uint8(node.height) + 1
	} else {
		// Requested hash itself + previous 10 entries + genesis block.
		// Then floor(log2(height-10)) entries for the skip portion.
		adjustedHeight := uint32(node.height) - 10
		maxEntries = 12 + fastLog2Floor(adjustedHeight)
	}
	locator := make(BlockLocator, 0, maxEntries)

	step := int32(1)
	for node != nil {
		locator = append(locator, &node.hash)

		// Nothing more to add once the genesis block has been added.
		if node.height == 0 {
			break
		}

		// Calculate height of previous node to include ensuring the
		// final node is the genesis block.
		height := node.height - step
		if height < 0 {
			height = 0
		}

		// When the node is in the current chain view, all of its
		// ancestors must be too, so use a much faster O(1) lookup in
		// that case.  Otherwise, fall back to walking backwards through
		// the nodes of the other chain to the correct ancestor.  An
		// ancestor that falls below the retained window terminates the
		// locator, mirroring the historical behavior of stopping at the
		// oldest materialized node.
		if c.contains(node) {
			if height >= c.base {
				node = c.nodes[height-c.base]
			} else {
				node = nil
			}
		} else {
			node = node.Ancestor(height)
		}

		// Once 11 entries have been included, start doubling the
		// distance between included hashes.
		if len(locator) > 10 {
			step *= 2
		}
	}

	return locator
}

// BlockLocator returns a block locator for the passed block node.  The passed
// node can be nil in which case the block locator for the current tip
// associated with the view will be returned.
//
// See the BlockLocator type for details on the algorithm used to create a block
// locator.
//
// This function is safe for concurrent access.
func (c *chainView) BlockLocator(node *blockNode) BlockLocator {
	c.mtx.Lock()
	locator := c.blockLocator(node)
	c.mtx.Unlock()
	return locator
}
