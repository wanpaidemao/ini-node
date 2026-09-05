// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package blockchain

import (
	"fmt"
	"runtime/debug"
	"time"

	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/database"
	"github.com/btcsuite/btcd/wire/v2"
)

// headerFlushBatchSize is the number of headers to accumulate in the dirty set
// before the block index is flushed to the database during header sync.
// Batching bounds the amount of work re-downloaded after a restart to at most
// this many headers.
const headerFlushBatchSize = 20000

// blockFlushBatchSize is the number of blocks to connect before flushing the
// block index to the database during block sync.  Batching bounds the amount
// of work re-processed after a restart to at most this many blocks.
const blockFlushBatchSize = 1000

// freeOSMemoryMinInterval is the minimum time that must elapse between
// debug.FreeOSMemory calls.  During header sync a batch flush can complete in
// well under this interval, so without coalescing the forced GC would run far
// more often than necessary.
const freeOSMemoryMinInterval = 15 * time.Second

// freeOSMemory runs a garbage collection and returns the reclaimed heap arena
// memory to the operating system.  It is invoked after each batched header
// flush during the initial sync, when the windowed block index allocates and
// releases the bulk of its block nodes; without it the Go heap arena retains
// the high-water mark and RSS stays far above the live heap.  The call runs
// asynchronously so the batch flush does not block on the STW, and the result
// is coalesced to once per freeOSMemoryMinInterval.
//
// This function MUST be called with the chain lock held (for writes).
func (b *BlockChain) freeOSMemory() {
	now := time.Now()
	if now.Sub(b.lastFreeOSMemory) < freeOSMemoryMinInterval {
		return
	}
	b.lastFreeOSMemory = now
	go debug.FreeOSMemory()
}

// maybeAcceptBlock potentially accepts a block into the block chain and, if
// accepted, returns whether or not it is on the main chain.  It performs
// several validation checks which depend on its position within the block chain
// before adding it.  The block is expected to have already gone through
// ProcessBlock before calling this function with it.
//
// The flags are also passed to checkBlockContext and connectBestChain.  See
// their documentation for how the flags modify their behavior.
//
// This function MUST be called with the chain state lock held (for writes).
func (b *BlockChain) maybeAcceptBlock(block *btcutil.Block, flags BehaviorFlags) (bool, error) {
	// The height of this block is one more than the referenced previous
	// block.
	prevHash := &block.MsgBlock().Header.PrevBlock
	prevNode := b.index.LookupNode(prevHash)
	if prevNode == nil {
		// TEMP DEBUG: parent not in memory index -> block becomes orphan.
		log.Warnf("TEMP-DBG orphan-or-unknown-parent hash=%s prev=%s "+
			"(parent not in memory index)", block.Hash(), prevHash)
		str := fmt.Sprintf("previous block %s is unknown", prevHash)
		return false, ruleError(ErrPreviousBlockUnknown, str)
	} else if !b.bestChain.Contains(prevNode) &&
		b.index.NodeStatus(prevNode).KnownInvalid() {
		// TEMP DEBUG: stale invalid-ancestor flag (spurious rollback).
		log.Warnf("TEMP-DBG invalid-ancestor hash=%s prev=%s height=%d",
			block.Hash(), prevHash, prevNode.height)
		// A block that is on the best chain cannot be invalid: connecting
		// it validated all of its data, and InvalidateBlock reorgs a
		// genuinely invalidated block off the chain.  An invalid flag on
		// an in-chain ancestor is therefore a stale mark left by a
		// spurious rollback (a previous session misclassified an
		// incomplete witness-less block payload as a fabricated chain and
		// persisted InvalidateHeaderChain statuses).  Ignore it here so
		// the ancestor's children are not rejected forever; the stale flag
		// itself is cleared when the block is reconnected.
		str := fmt.Sprintf("previous block %s is known to be invalid", prevHash)
		return false, ruleError(ErrInvalidAncestorBlock, str)
	}

	blockHeight := prevNode.height + 1
	block.SetHeight(blockHeight)

	// A miner-submitted block must land on the network-projected header chain
	// (when that chain already covers this height) before it may become a
	// best-chain tip.  This prevents a locally-mined block that no peer accepts
	// from displacing the real main-chain tip (observed: local block a23e7e62
	// vs network main-chain 44060189 b345517e).
	if flags&BFMinerSubmit == BFMinerSubmit {
		// The mined block must extend a parent that is itself on the
		// network-confirmed header chain.  This closes the timing blind spot
		// where the header chain has not yet synced past the mined height (so
		// NodeByHeight below returns nil and the block would otherwise be
		// accepted): right after a fabricated-chain rollback the previous
		// locally-mined siblings are removed from the header chain, and the
		// miner must wait for peers to re-confirm the parent before minting
		// another block on it.  Without this guard a lone/competing miner
		// keeps re-minting the same-height sibling on the discarded parent,
		// re-fabricating the tip every ~10 minutes (observed: block download
		// stuck at the mined height forever).
		// 挖出的块必须extend一个自身已在(网络确认的)header 链上的父块。这堵上
		// 了"header 链尚未同步到挖矿高度、NodeByHeight 返回 nil 因而该块被接受"
		// 的时序盲点:伪造链回滚后,先前本地挖出的兄弟块已从 header 链移除,矿工
		// 必须等对等点重新确认父块后才能在其上继续挖,否则单独/竞争的矿工会反复
		// 在废弃父块上挖同一高度的兄弟块,每约 10 分钟重新伪造一次 tip。
		if !b.bestHeader.Contains(prevNode) {
			str := fmt.Sprintf("miner-submitted block %v builds on parent %v "+
				"which is not on the network-confirmed header chain -- "+
				"waiting for re-sync", block.Hash(), prevHash)
			return false, ruleError(ErrMinedBlockNotOnMainChain, str)
		}

		if headerNode := b.bestHeader.NodeByHeight(blockHeight); headerNode != nil &&
			!headerNode.hash.IsEqual(block.Hash()) {
			str := fmt.Sprintf("miner-submitted block %v is not on the "+
				"network main chain (header chain has %v at height %d)",
				block.Hash(), headerNode.hash, blockHeight)
			return false, ruleError(ErrMinedBlockNotOnMainChain, str)
		}
	}

	// The block must pass all of the validation rules which depend on the
	// position of the block within the block chain.
	err := b.checkBlockContext(block, prevNode, flags)
	if err != nil {
		return false, err
	}

	// Insert the block into the database if it's not already there.  Even
	// though it is possible the block will ultimately fail to connect, it
	// has already passed all proof-of-work and validity tests which means
	// it would be prohibitively expensive for an attacker to fill up the
	// disk with a bunch of blocks that fail to connect.  This is necessary
	// since it allows block download to be decoupled from the much more
	// expensive connection logic.  It also has some other nice properties
	// such as making blocks that never become part of the main chain or
	// blocks that fail to connect available for further analysis.
	err = b.db.Update(func(dbTx database.Tx) error {
		if err := dbStoreBlock(dbTx, block); err != nil {
			return err
		}

		// Advance the persisted block-download cursor when this block is the
		// furthest block data stored so far, so a restart can resume the block
		// download from the highest locally-available block.
		if b.blockDownload == nil || blockHeight > int32(b.blockDownload.height) {
			b.blockDownload = &bestBlockDownloadState{
				hash:   *block.Hash(),
				height: uint32(blockHeight),
			}
			return dbPutBestBlockDownloadState(dbTx, block.Hash(), blockHeight)
		}
		return nil
	})
	if err != nil {
		return false, err
	}

	// Create a new block node for the block and add it to the node index. Even
	// if the block ultimately gets connected to the main chain, it starts out
	// on a side chain.
	//
	// If a header-only node already exists (from maybeAcceptBlockHeader),
	// upgrade its status rather than creating a new node.  Creating a new
	// node would overwrite the index entry, orphaning the pointer held by
	// bestHeader's chainView and breaking Contains checks.
	newNode := b.index.LookupNode(block.Hash())
	if newNode != nil {
		b.index.SetStatusFlags(newNode, statusDataStored)

		// A header-only node may have had its parent pointer severed by a
		// window eviction that ran while the block body was still in flight
		// (evictWindow cuts parent/ancestor pointers below the boundary).
		// The parent is guaranteed to be in memory now -- prevNode was
		// resolved at the top of this function -- so re-link the chain before
		// connecting.  Otherwise any difficulty walk that starts at this node
		// (for example when its own child is validated) stops at the severed
		// link and reports the PowLimit as the expected difficulty, falsely
		// rejecting a valid block.  This mirrors the parent wiring that
		// newBlockNode performs for a freshly created node.
		if newNode.parent == nil && prevNode != nil {
			newNode.parent = prevNode
			newNode.buildAncestor()
		}
	} else {
		blockHeader := &block.MsgBlock().Header
		newNode = newBlockNode(blockHeader, prevNode)
		newNode.status = statusDataStored | statusHeaderStored
		b.index.AddNode(newNode)
	}
	err = b.index.flushToDB(false)
	if err != nil {
		return false, err
	}

	// Connect the passed block to the chain while respecting proper chain
	// selection according to the chain with the most proof of work.  This
	// also handles validation of the transaction scripts.
	isMainChain, err := b.connectBestChain(newNode, block, flags)
	if err != nil {
		return false, err
	}

	// The block data has now been fully validated and connected, which
	// proves the header is genuine.  Clear any invalid flag the node is
	// carrying: it can only have been set by a spurious rollback -- a
	// previous session misclassified an incomplete (witness-less) block
	// payload as a fabricated chain, then InvalidateHeaderChain marked the
	// valid headers above it invalid and persisted that state.  Without
	// clearing it here, the block's own children are rejected with
	// "previous block ... is known to be invalid" forever even though the
	// ancestor has since been connected successfully.
	b.index.UnsetStatusFlags(newNode, statusInvalidAncestor)
	if writeErr := b.index.flushToDB(false); writeErr != nil {
		return false, writeErr
	}

	// Notify the caller that the new block was accepted into the block
	// chain.  The caller would typically want to react by relaying the
	// inventory to other peers.
	func() {
		b.chainLock.Unlock()
		defer b.chainLock.Lock()
		b.sendNotification(NTBlockAccepted, block)
	}()

	return isMainChain, nil
}

// ResumeBlockConnect connects a block whose data is already stored in the
// database but which was not connected to the best chain, typically because the
// node restarted between the store and connect steps of a previous session.
// Blocks that were never connected must be driven through the connection logic
// explicitly: ProcessBlock refuses them (blockExists reports them as already
// present), so without this a resumed download would stall on local data.
//
// It is a no-op that returns true when the block is already on the best chain.
//
// This function is safe for concurrent access.
func (b *BlockChain) ResumeBlockConnect(hash *chainhash.Hash, flags BehaviorFlags) (bool, error) {
	b.chainLock.Lock()
	defer b.chainLock.Unlock()

	// Nothing to do when the block is already connected and its node is
	// still retained in the in-memory header window.  Evicted connected nodes
	// cannot be cheaply distinguished from stored-but-unconnected ones (the
	// cold height index tracks the best header chain, not the connected
	// chain), but the resume loop only targets blocks above the current tip,
	// which are never connected, so this guard is a safety net rather than
	// the primary path.
	if node := b.index.LookupNode(hash); node != nil && b.bestChain.Contains(node) {
		return true, nil
	}

	// Load the stored block payload and run it through the same acceptance
	// path used for a freshly downloaded block.  The data is already on disk,
	// so the store step simply overwrites it, and the existing node entry (if
	// any) is upgraded with the data-present status before connecting.
	block, err := b.FetchBlockByHash(hash)
	if err != nil {
		return false, err
	}
	return b.maybeAcceptBlock(block, flags)
}

// maybeAcceptBlockHeader potentially accepts the header to the block index and,
// if accepted, returns a bool indicating if the header extended the best chain
// of headers.  It also performs several context independent checks as well as
// those which depend on its position within the header chain.
//
// The flags are passed to CheckBlockHeaderSanity and CheckBlockHeaderContext
// which allow the skipping of PoW check or the check for the block difficulty,
// median time check, and the BIP94 check.
//
// The skipCheckpoint boolean allows skipping of the check for if the header is
// part of the existing checkpoints.
//
// In the case the block header is already known, the associated block node is
// examined to determine if the block is already known to be invalid, in which
// case an appropriate error will be returned.
//
// This function MUST be called with the chain lock held (for writes).
func (b *BlockChain) maybeAcceptBlockHeader(header *wire.BlockHeader,
	flags BehaviorFlags, skipCheckpoint bool) (bool, error) {

	// Orphan headers are not allowed and this function should never be called
	// with the genesis block.
	prevHash := &header.PrevBlock
	prevNode := b.index.LookupNode(prevHash)
	if prevNode == nil {
		str := fmt.Sprintf("previous block %s is not known", prevHash)
		return false, ruleError(ErrPreviousBlockUnknown, str)
	}

	// This header is invalid if its previous node is invalid.  A node that
	// is on the best chain cannot be invalid: connecting it validated all of
	// its data, and InvalidateBlock reorgs a genuinely invalidated block off
	// the chain.  An invalid flag on an in-chain ancestor is therefore a
	// stale mark left by a spurious rollback (a previous session
	// misclassified an incomplete witness-less block payload as a fabricated
	// chain and persisted InvalidateHeaderChain statuses), so it must not
	// block the ancestors' children from being re-accepted.
	if !b.bestChain.Contains(prevNode) &&
		b.index.NodeStatus(prevNode).KnownInvalid() {
		str := fmt.Sprintf(
			"previous block %s is known to be invalid", prevHash)
		return false, ruleError(ErrInvalidAncestorBlock, str)
	}

	// Avoid validating the header again if its validation status is already
	// known.  Invalid headers are never added to the block index, so if there
	// is an entry for the block hash, the header itself is known to be valid.
	hash := header.BlockHash()
	node := b.index.LookupNode(&hash)
	if node != nil {
		nodeStatus := b.index.NodeStatus(node)
		if nodeStatus&statusValidateFailed != 0 {
			// A validate-failed flag on a node whose parent is already on
			// the best chain is a stale mark from a spurious rollback
			// (previous sessions misclassified an incomplete witness-less
			// block payload as a fabricated chain).  The header itself is
			// being re-submitted by the download, so clear the flag and
			// let it re-validate instead of rejecting forever.
			//
			// The parent being on the best header chain counts too:
			// rollbackFabricatedHeaderChain marks the whole header
			// segment above the connected block tip, and during IBD the
			// header chain leads the block chain by millions of heights,
			// so replaying that segment only ever walks parents that are
			// header nodes.  Requiring a connected parent would leave the
			// chain unable to recover past one header above the tip -- a
			// single rollback would deadlock the header download forever.
			if b.bestChain.Contains(prevNode) ||
				b.bestHeader.Contains(prevNode) {
				b.index.UnsetStatusFlags(node, statusValidateFailed)
			} else {
				str := fmt.Sprintf("block %s is known to be invalid", hash)
				return false, ruleError(ErrKnownInvalidBlock, str)
			}
		} else if nodeStatus&statusInvalidAncestor != 0 {
			// Same stale-mark reasoning: an invalid-ancestor flag whose
			// parent is already connected on the best chain (or is part
			// of the best header chain being replayed after a rollback)
			// cannot be real, clear it and re-validate.
			if b.bestChain.Contains(prevNode) ||
				b.bestHeader.Contains(prevNode) {
				b.index.UnsetStatusFlags(node, statusInvalidAncestor)
			} else {
				str := fmt.Sprintf("block %s has an invalid ancestor", hash)
				return false, ruleError(ErrInvalidAncestorBlock, str)
			}
		}

		// If the node is in the bestHeaders chainview, it's in the main chain.
		// If it isn't, then we'll go through the verification process below.
		if b.bestHeader.Contains(node) {
			return true, nil
		}
	}

	// Perform context-free sanity checks on the block header.
	err := CheckBlockHeaderSanity(
		header, b.chainParams.PowLimit, b.timeSource, flags)
	if err != nil {
		return false, err
	}

	// The block must pass all of the validation rules which depend on the
	// position of the block within the block chain.
	err = CheckBlockHeaderContext(header, prevNode, flags, b, skipCheckpoint)
	if err != nil {
		return false, err
	}

	// Create a new block node for the block and add it to the block index.
	//
	// Note that the additional information for the actual transactions and
	// witnesses in the block can't be populated until the full block data is
	// known since that information is not available in the header.
	if node == nil {
		node = newBlockNode(header, prevNode)
		node.status = statusHeaderStored
		b.index.AddNode(node)
	}

	// Check if the header extends the best header tip.
	isMainChain := false
	parentHash := &header.PrevBlock
	if parentHash.IsEqual(&b.bestHeader.Tip().hash) {
		log.Debugf("accepted header %v as the new header tip", node.hash)

		// This header is now the end of the best headers.
		b.bestHeader.SetTip(node)
		isMainChain = true
	} else if node.workSum.Cmp(b.bestHeader.Tip().workSum) <= 0 {
		// We're extending (or creating) a side chain, but the cumulative
		// work for this new side chain is not enough to make it the new chain.
		// Log information about how the header is forking the chain.  The
		// fork may be unresolvable (nil) when it falls below the in-memory
		// header window boundary, in which case just log the header itself.
		fork := b.bestHeader.FindFork(node)
		if fork != nil && fork.hash.IsEqual(parentHash) {
			log.Infof("FORK: BlockHeader %v(%v) forks the chain at block %v(%v) "+
				"but did not have enough work to be the "+
				"main chain", node.hash, node.height, fork.hash, fork.height)
		} else if fork != nil {
			log.Infof("EXTEND FORK: BlockHeader %v(%v) extends a side chain "+
				"which forks the chain at block %v(%v)",
				node.hash, node.height, fork.hash, fork.height)
		} else {
			log.Infof("EXTEND FORK: BlockHeader %v(%v) forks the chain below "+
				"the in-memory header window and did not have enough work "+
				"to be the main chain", node.hash, node.height)
		}
	} else {
		prevTip := b.bestHeader.Tip()
		log.Infof("NEW BEST HEADER CHAIN: BlockHeader %v(%v) is now a longer "+
			"PoW chain than the previous header tip of %v(%v).",
			node.hash, node.height,
			prevTip.hash, prevTip.height)

		b.bestHeader.SetTip(node)
		isMainChain = true
	}

	// Flush the block index to the database periodically rather than on
	// every header.  Header sync can process thousands of headers per
	// second, so a write transaction per header would dominate the sync
	// time.  Batching bounds the headers re-downloaded after a restart to
	// headerFlushBatchSize.
	//
	// The flush runs after bestHeader.SetTip so the header that triggers a
	// flush is already part of the best header chain when the height index
	// rows are written; otherwise its height entry would be skipped (it is
	// guarded by bestHeaderView membership) and the height would never
	// become cold-readable, stalling the block downloader at that height.
	b.headerFlushCount++
	if b.headerFlushCount >= headerFlushBatchSize {
		b.headerFlushCount = 0
		// forceEvict: this batch flush is the only eviction point during a
		// header sync (once per headerFlushBatchSize headers).  Without it the
		// blockFlushBatchSize throttle would defer eviction for millions of
		// headers, letting the in-memory index grow unbounded past the
		// --headerwindow bound.
		err = b.index.flushToDB(true)
		if err != nil {
			return false, err
		}
		// Return the Go heap arena memory back to the OS now that the bulk
		// of the windowed block index has been released.  See freeOSMemory.
		b.freeOSMemory()
	}

	return isMainChain, nil
}
