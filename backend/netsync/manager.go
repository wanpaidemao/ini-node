// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package netsync

import (
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/database"
	"github.com/btcsuite/btcd/mempool"
	peerpkg "github.com/btcsuite/btcd/peer"
	"github.com/btcsuite/btcd/wire/v2"
)

const (
	// minInFlightBlocks is the minimum number of blocks that should be
	// in the request queue for the initial block download mode before
	// requesting more.
	minInFlightBlocks = 10

	// maxRejectedTxns is the maximum number of rejected transactions
	// hashes to store in memory.
	maxRejectedTxns = 1000

	// maxRequestedBlocks is the maximum number of requested block
	// hashes to store in memory.
	maxRequestedBlocks = wire.MaxInvPerMsg

	// maxBlockRequestWindow is the maximum number of blocks that the
	// initial block download may have requested ahead of the currently
	// connected best chain height.
	//
	// Without this bound, buildBlockRequest walks the entire (multi-million
	// block) header chain and requests every missing block in one go.  The
	// parallel download then spreads those requests across disjoint slices
	// spanning the whole chain, so blocks arrive dozens of thousands of
	// heights out of order and flood the orphan pool; the pool evicts the
	// low-height blocks needed to advance the tip and the download stalls.
	// Capping the request horizon keeps every requested block close enough
	// to the tip that it connects (draining its orphaned descendants) long
	// before the pool fills.  The bound is generous so each parallel slice
	// stays multiple times the per-peer in-flight throttle deep ahead of the
	// tip, letting many peers prefetch simultaneously even on a slow window.
	maxBlockRequestWindow = 8000

	// maxRequestedTxns is the maximum number of requested transactions
	// hashes to store in memory.
	maxRequestedTxns = wire.MaxInvPerMsg

	// maxStallDuration is the time after which we will disconnect our
	// current sync peer if we haven't made progress.
	maxStallDuration = 3 * time.Minute

	// stallSampleInterval the interval at which we will check to see if our
	// sync has stalled.
	stallSampleInterval = 30 * time.Second

	// maxHeaderSyncPeers is the maximum number of peers that will be used in
	// parallel when downloading headers during the initial block download.
	maxHeaderSyncPeers = 8

	// utxoFlushInterval is how often the UTXO cache is flushed while in
	// initial block download mode.  It mirrors the blockchain package's
	// periodic flush interval so that an unclean shutdown during a (long)
	// IBD leaves the consistent UTXO state no farther than roughly this much
	// data behind the chain tip, avoiding a huge reconstruction on restart.
	utxoFlushInterval = 5 * time.Minute

	// headerRangeStallTimeout is the amount of time an in-flight header
	// range is allowed to remain outstanding before it is re-issued to a
	// different peer.  This lets a slow or unresponsive peer be bypassed
	// without relying on the (much longer) overall sync stall timeout.
	// The timeout is kept short because every response is verified to
	// connect to the exact height it was asked for, so re-issuing a slice
	// to another peer is always safe even while the previous request for it
	// is still in flight.
	headerRangeStallTimeout = 6 * time.Second

	// blockSliceStallTimeout is the amount of time an in-flight block slice
	// is allowed to remain without any of its blocks being requested by the
	// peer before it is re-issued to a different peer.  It is longer than
	// headerRangeStallTimeout because block responses are large and the
	// per-slice window is bounded, so a peer needs more time to stream it.
	// The re-issue clears the slice's hashes from the global request pool
	// first, so the taking-over peer actually re-requests them.
	blockSliceStallTimeout = 30 * time.Second

	// syncProgressLogInterval is the interval at which an INFO progress line
	// is logged while the node is behind the chain (initial block download
	// or catch-up sync).  It reports the current height, the per-interval
	// delta, the average blocks/sec, the share of the chain synced and an
	// ETA based on the best known header height.
	syncProgressLogInterval = time.Minute

	// blockInFlightTarget is the number of blocks each participating peer in
	// the parallel block download keeps in flight at a time.  A single
	// buildBlockRequest fills the peer's window up to this target instead of
	// draining its whole slice at once, so a slow peer keeps getting topped
	// up by blkDownload instead of sitting idle behind a full in-flight set
	// that never drains.  Draining an entire slice at once starved every peer
	// but the fastest one, which alone re-claimed the new frontier slices and
	// reduced the parallel download to a single peer.
	blockInFlightTarget = 200

	// headerLeadLimit caps how far the applied header tip may lead the
	// connected best chain before the parallel header download pauses and
	// lets blocks catch up.  Header processing is far cheaper than block
	// processing, so without this cap the header tip races ahead of the
	// blocks indefinitely (observed: ~42k-68k lead).  A lead beyond
	// maxBlockRequestWindow (8000) serves no purpose -- the block request
	// frontier never goes further than bestChain+8000 -- while a large lead
	// pushes the receive-side prev check (HeaderHashByHeight(start-1)) out
	// of the in-memory header window and into the DB cold-read path, whose
	// reference frame is where stale height rows (P8/P9) live.  The cap is
	// enforced at the single getheaders dispatch point (launchHeaderRange,
	// which both fresh assignments and re-issues go through), so the header
	// download keeps the lead just below this value: whenever the lead is
	// below the cap a new range is dispatched immediately to push it back
	// up, and once it reaches the cap dispatching pauses -- the header tip
	// is kept ahead of the blocks by design, but never races arbitrarily
	// far, and the receive-side prev check always stays in-memory.
	headerLeadLimit = 42000

	// blockUnavailableTimeout is how long the block download may remain stuck
	// at a single height before the header chain is suspected of being
	// fabricated or forked and rolled back.  A real chain's blocks are served
	// by the network; a forged chain's blocks either do not exist on any peer
	// or do not link to the applied tip, so the download would spin forever.
	// The window is deliberately much larger than blockSliceStallTimeout so a
	// merely slow peer is never misjudged.  It is, however, well below the old
	// 10 minutes: the strong divergence detectors (best-tip-vs-header chain,
	// peer-height, and the front-unreachable votes) typically fire first, but
	// this block-side timer is the last-resort fallback for a lone/competing
	// miner whose own block was orphaned -- leaving it at 10 minutes made a
	// mining-loss stall last ~10 minutes before the node even attempted to
	// recover.  At 90s (and with the fabricated blocks hard-deleted on rollback)
	// a lost block race heals in ~1.5 minutes instead.
	blockUnavailableTimeout = 90 * time.Second

	// maxFabricatedRollbacks caps how many times the fabricated-header-chain
	// rollback may target the same height before it is refused.  The rollback
	// persists invalid-ancestor marks into the block index, so an unbounded
	// rollback loop (e.g. a watchdog misjudging a merely slow peer, or peers
	// re-serving a bogus chain that keeps failing to re-apply) escalates from
	// a stall into permanent index pollution.  Once the cap is hit the
	// rollback is refused and an error is logged (rate-limited) so the
	// operator can intervene instead of the node silently chewing its index.
	maxFabricatedRollbacks = 5

	// rollbackRefusalLogInterval rate-limits the error logged while the
	// rollback cap is being enforced, so a hot retry loop (the header front
	// failing to apply on every blockHandler pass) cannot flood the log.
	rollbackRefusalLogInterval = time.Minute
)

// zeroHash is the zero value hash (all zeros).  It is defined as a convenience.
var zeroHash chainhash.Hash

// newPeerMsg signifies a newly connected peer to the block handler.
type newPeerMsg struct {
	peer *peerpkg.Peer
}

// blockMsg packages a bitcoin block message and the peer it came from together
// so the block handler has access to that information.
type blockMsg struct {
	block *btcutil.Block
	peer  *peerpkg.Peer
	reply chan struct{}
}

// invMsg packages a bitcoin inv message and the peer it came from together
// so the block handler has access to that information.
type invMsg struct {
	inv  *wire.MsgInv
	peer *peerpkg.Peer
}

// headersMsg packages a bitcoin headers message and the peer it came from
// together so the block handler has access to that information.
type headersMsg struct {
	headers *wire.MsgHeaders
	peer    *peerpkg.Peer
}

// notFoundMsg packages a bitcoin notfound message and the peer it came from
// together so the block handler has access to that information.
type notFoundMsg struct {
	notFound *wire.MsgNotFound
	peer     *peerpkg.Peer
}

// donePeerMsg signifies a newly disconnected peer to the block handler.
type donePeerMsg struct {
	peer *peerpkg.Peer
}

// txMsg packages a bitcoin tx message and the peer it came from together
// so the block handler has access to that information.
type txMsg struct {
	tx    *btcutil.Tx
	peer  *peerpkg.Peer
	reply chan struct{}
}

// getSyncPeerMsg is a message type to be sent across the message channel for
// retrieving the current sync peer.
type getSyncPeerMsg struct {
	reply chan int32
}

// processBlockResponse is a response sent to the reply channel of a
// processBlockMsg.
type processBlockResponse struct {
	isOrphan bool
	err      error
}

// processBlockMsg is a message type to be sent across the message channel
// for requested a block is processed.  Note this call differs from blockMsg
// above in that blockMsg is intended for blocks that came from peers and have
// extra handling whereas this message essentially is just a concurrent safe
// way to call ProcessBlock on the internal block chain instance.
type processBlockMsg struct {
	block *btcutil.Block
	flags blockchain.BehaviorFlags
	reply chan processBlockResponse
}

// isCurrentMsg is a message type to be sent across the message channel for
// requesting whether or not the sync manager believes it is synced with the
// currently connected peers.
type isCurrentMsg struct {
	reply chan bool
}

// getSyncStatusMsg is a message type to be sent across the message channel
// for requesting a snapshot of the in-progress parallel initial download
// (per-peer header ranges / block slices) for the RPC layer.
type getSyncStatusMsg struct {
	reply chan *SyncStatus
}

// pauseMsg is a message type to be sent across the message channel for
// pausing the sync manager.  This effectively provides the caller with
// exclusive access over the manager until a receive is performed on the
// unpause channel.
type pauseMsg struct {
	unpause <-chan struct{}
}

// headerNode is used as a node in a list of headers that are linked together
// between checkpoints.
type headerNode struct {
	height int32
	hash   *chainhash.Hash
}

// peerSyncState stores additional information that the SyncManager tracks
// about a peer.
type peerSyncState struct {
	syncCandidate   bool
	requestQueue    []*wire.InvVect
	requestedTxns   map[chainhash.Hash]struct{}
	requestedBlocks map[chainhash.Hash]struct{}
}

// headerRange represents a contiguous slice of block headers that has been
// requested from a single peer during the initial header download.  Ranges are
// applied to the header chain in order, so a range that has been received but
// is not yet the front of the download is buffered until all preceding ranges
// have been processed.
type headerRange struct {
	start      int32
	peer       *peerpkg.Peer
	headers    []*wire.BlockHeader
	received   bool
	assignedAt time.Time
	// applied is the height up to which this range's headers have actually
	// been applied to the chain — real progress tracked server-side as the
	// batch applies, so the UI can show genuine progress even when a whole
	// 2000-header slice downloads between two polls.
	applied int32

	// C2 front double-confirmation: the front range (the one that decides the
	// direction of the header chain) is corroborated by a second independent
	// peer before it is applied, so a misattributed front (fed through a stale
	// height row, P8) cannot enter the index on a single unanimous response.
	// firstHash/firstPeer record the first corroborating response; confirmed
	// is set once a second independent peer agrees (or immediately when fewer
	// than two peers are present, degrading to a single vote with C1's fast
	// rollback covering a wrong chain).  Non-front ranges leave confirmed
	// true and are applied on the single response as before.
	firstHash *chainhash.Hash
	firstPeer *peerpkg.Peer
	confirmed bool
}

// HeaderRecentRange is a lightweight record of one completed parallel header
// download window: the contiguous [start, end) range a single peer fetched.
// Only a short history is kept, purely so operators can see how the header
// chain was chunked across peers.
type HeaderRecentRange struct {
	Start      int32
	End        int32
	Peer       string
	AssignedAt time.Time
}

// headerSyncState tracks a parallel (multi-peer) initial header download.  It
// is only accessed from the blockHandler goroutine and is non-nil while header
// headers are being fetched from several peers simultaneously.
type headerSyncState struct {
	peers       []*peerpkg.Peer                // participating peers, capped
	target      int32                          // highest advertised tip to reach
	nextHeight  int32                          // next header height we need to apply
	nextAssign  int32                          // next height to hand out to a peer
	ranges      map[int32]*headerRange         // in-flight/received ranges by start
	peerRange   map[*peerpkg.Peer]*headerRange // single range assigned per peer
	sliceLen    int32                          // max headers per getheaders batch
	lastReissue time.Time                      // last time a stale range was reissued

	// leadPaused is true while header assignment is suppressed by the
	// header-lead cap (the header tip is already far enough ahead of the
	// block frontier).  Exposed to the UI as header_paused so a stalled
	// next_assign with received=true leftovers reads as "paused", not as
	// fresh completions.
	leadPaused bool
}

// blockSlice represents the contiguous range of block heights assigned to a
// single peer during a parallel initial block download.  Slices handed to
// different peers never overlap: the assignment frontier only ever advances
// and the front of an assigned slice is capped by the next already-assigned
// slice (see nextSliceBeyond), so each peer fetches a disjoint slice of the
// chain in parallel.
type blockSlice struct {
	start      int32
	end        int32
	peer       *peerpkg.Peer
	assignedAt time.Time
	// received is the highest height in [start, end) for which a block has
	// actually been delivered by the peer (i.e. its true download progress).
	// It starts at start-1 and only advances inside handleBlockMsg, so the UI
	// can show every slice advancing in parallel instead of only the one the
	// connected chain tip currently happens to sit in.
	received int32
}

// blockSyncState tracks the parallel (multi-peer) initial block download.  It
// is only accessed from the blockHandler goroutine and is non-nil while blocks
// are being fetched from several peers simultaneously.
type blockSyncState struct {
	nextAssign   int32                         // next height to hand out to a peer
	target       int32                         // highest header height to reach
	slices       map[int32]*blockSlice         // assigned slices by start height
	peerSlice    map[*peerpkg.Peer]*blockSlice // slice currently assigned to each peer
	sliceLen     int32                         // max height span handed to a peer at once
	lastReissue  time.Time                     // last time a stale slice was reissued
	lastProgress map[*peerpkg.Peer]time.Time   // last time each peer delivered a block

	// frontMissingSince/frontMissingHeight record the first moment the front
	// block (the one right above the connected tip) was observed to be
	// unrequested -- its request was freed by a stall re-issue or a dropped
	// peer and nobody has claimed it since.  The front-slice guard normally
	// only lets a recently-delivering peer claim the front; once the front has
	// been unrequested for a full stall window this marker lets any peer claim
	// it, breaking the permanent deadlock where no peer qualifies and no block
	// ever arrives to re-trigger the download.
	frontMissingSince  time.Time
	frontMissingHeight int32
}

// PeerSyncStatus describes one peer's role in an in-progress parallel initial
// download.  It is an immutable snapshot built inside the blockHandler
// goroutine for the RPC layer.
type PeerSyncStatus struct {
	ID            int32  `json:"id"`
	Addr          string `json:"addr"`
	SyncNode      bool   `json:"sync_node"`
	SyncCandidate bool   `json:"sync_candidate"`
	CurrentHeight int32  `json:"current_height"`
	// Block slice currently assigned to the peer.  Start == End means no
	// block slice is currently assigned (or no parallel block download is
	// running).
	SliceStart      int32 `json:"slice_start"`
	SliceEnd        int32 `json:"slice_end"`
	SliceAssignedAt int64 `json:"slice_assigned_at"`
	SliceReceived   int32 `json:"slice_received"`
	// Header range currently assigned during a parallel header download.
	// Start == End means none is assigned.
	HeaderRangeStart      int32 `json:"header_range_start"`
	HeaderRangeEnd        int32 `json:"header_range_end"`
	HeaderRangeReceived   bool  `json:"header_range_received"`
	HeaderRangeApplied    int32 `json:"header_range_applied"`
	HeaderRangeAssignedAt int64 `json:"header_range_assigned_at"`
	// In-flight blocks this peer has been asked for but not yet delivered.
	InFlightBlocks int `json:"in_flight_blocks"`
	// Last time this peer delivered a block (unix seconds, 0 if never).
	LastBlockAt int64 `json:"last_block_at"`
}

// SyncStatus is an immutable snapshot of the sync manager's parallel initial
// download state, built inside the blockHandler goroutine and handed to the
// RPC layer.
type SyncStatus struct {
	// Current mirrors the sync manager's current() result.
	Current bool `json:"current"`
	// IBD is true while the node is in initial block download mode.
	IBD bool `json:"ibd"`
	// BestChainHeight is the connected best chain height.
	BestChainHeight int32 `json:"best_chain_height"`
	// HeaderTip is the highest known header height.
	HeaderTip int32 `json:"header_tip"`
	// HeaderTarget is the highest height a parallel header download is
	// driving toward (0 when no header download is in progress).
	HeaderTarget int32 `json:"header_target"`
	// HeaderNextAssign is the next height the header download will hand to a
	// peer (0 when no header download is in progress).
	HeaderNextAssign int32 `json:"header_next_assign"`
	// BlockTarget is the highest height the parallel block download is
	// driving toward (0 when no block download is in progress).
	BlockTarget int32 `json:"block_target"`
	// BlockNextAssign is the next height the block download will hand to a
	// peer (0 when no block download is in progress).
	BlockNextAssign int32 `json:"block_next_assign"`
	// BlockWindow is the request horizon (maxBlockRequestWindow) ahead of the
	// connected chain that the parallel block download may prefetch.
	BlockWindow int32 `json:"block_window"`
	// HeaderSliceLen is the per-peer header batch size of the most recent
	// parallel header download (0 when none).
	HeaderSliceLen int32 `json:"header_slice_len"`
	// HeaderRecentRanges lists the most recent completed header download
	// windows as [start, end) ranges with the peer that fetched each.
	HeaderRecentRanges []HeaderRecentRange `json:"header_recent_ranges"`
	// HeaderPaused reports that header assignment is currently suppressed by
	// the header-lead cap (the header tip is far enough ahead of the block
	// frontier); the UI shows "paused" instead of reading stale received
	// ranges as fresh completions.
	HeaderPaused bool `json:"header_paused"`
	// Peers lists every connected peer with its sync role and assigned work.
	Peers []PeerSyncStatus `json:"peers"`
}

// limitAdd is a helper function for maps that require a maximum limit by
// evicting a random value if adding the new value would cause it to
// overflow the maximum allowed.
func limitAdd(m map[chainhash.Hash]struct{}, hash chainhash.Hash, limit int) {
	if len(m)+1 > limit {
		// Remove a random entry from the map.  For most compilers, Go's
		// range statement iterates starting at a random item although
		// that is not 100% guaranteed by the spec.  The iteration order
		// is not important here because an adversary would have to be
		// able to pull off preimage attacks on the hashing function in
		// order to target eviction of specific entries anyways.
		for txHash := range m {
			delete(m, txHash)
			break
		}
	}
	m[hash] = struct{}{}
}

// SyncManager is used to communicate block related messages with peers. The
// SyncManager is started as by executing Start() in a goroutine. Once started,
// it selects peers to sync from and starts the initial block download. Once the
// chain is in sync, the SyncManager handles incoming block and header
// notifications and relays announcements of new blocks to peers.
type SyncManager struct {
	peerNotifier   PeerNotifier
	started        int32
	shutdown       int32
	chain          *blockchain.BlockChain
	txMemPool      *mempool.TxPool
	chainParams    *chaincfg.Params
	progressLogger *blockProgressLogger
	msgChan        chan interface{}
	wg             sync.WaitGroup
	quit           chan struct{}

	// These fields should only be accessed from the blockHandler thread
	rejectedTxns     map[chainhash.Hash]struct{}
	requestedTxns    map[chainhash.Hash]struct{}
	requestedBlocks  map[chainhash.Hash]struct{}
	syncPeer         *peerpkg.Peer
	peerStates       map[*peerpkg.Peer]*peerSyncState
	lastProgressTime time.Time

	// headerSync is non-nil while the initial header download is being
	// performed across several peers in parallel.  It is only touched from
	// the blockHandler goroutine.
	headerSync *headerSyncState

	// headerRecent keeps the last few completed parallel header download
	// windows (which peer fetched which [start, end) range) after the header
	// download itself has finished, so an operator can still see how the
	// header chain was chunked across peers.  Only touched from the
	// blockHandler goroutine.
	headerRecent []HeaderRecentRange

	// headerSliceLen is the per-peer header batch size of the most recent
	// parallel header download.  It survives headerSync being torn down so the
	// UI can show how many windows the header chain was split into.  Only
	// touched from the blockHandler goroutine.
	headerSliceLen int32

	// blockSync is the set of peers taking part in a parallel initial block
	// download.  Each participating peer is handed a disjoint slice of the
	// header chain and tops itself back up as it drains.  It is only touched
	// from the blockHandler goroutine.
	blockSync []*peerpkg.Peer

	// blockSyncState tracks the per-peer disjoint height slices of an
	// in-progress parallel initial block download.  It is nil while no
	// parallel block download is running and only touched from the
	// blockHandler goroutine.  It is created either by
	// startParallelBlockDownload (once the applied header tip leads the
	// connected best chain by blockSyncStartLead, or when the header download
	// finishes) or by startSync when the node already has all headers.
	blockSyncState *blockSyncState

	// blockSyncStartLead is the height margin the applied header tip must
	// lead the connected best chain by before the parallel block download is
	// started while the header download is still running.  Zero disables the
	// overlap.  Only touched from the blockHandler goroutine.
	blockSyncStartLead int32

	// blockMissingSince/blockMissingHeight record the block download being
	// stuck: the height at which the download was last observed to advance and
	// the time it has been stuck there.  They are reset whenever the front
	// advances, and are used by the stall handler to detect a fabricated or
	// forked header chain whose blocks no peer can serve.  The check keys on
	// the front height not advancing, NOT on the front hash being present in
	// requestedBlocks: a deadlock can free the front request entirely (all
	// slices released, no peer delivering), leaving requestedBlocks empty
	// while the front never advances -- exactly the case that must still be
	// detected.  Only touched from the blockHandler goroutine.
	blockMissingSince  time.Time
	blockMissingHeight int32

	// The following fields are used for the initial block download mode.
	ibdMode bool

	// fabricatedRollbackCount tracks how many times the fabricated-header
	// chain rollback has targeted fabricatedRollbackHeight in a row, and
	// lastRollbackRefusalAt rate-limits the refusal error log while the cap
	// is being enforced.  A different (higher) rollback height resets the
	// count, since reaching a new height means the download made progress
	// after the previous rollback.  Only touched from the blockHandler
	// goroutine.
	fabricatedRollbackCount int
	fabricatedRollbackHeight int32
	lastRollbackRefusalAt    time.Time

	// fabricatedRollbackDepth is how many extra blocks below the best tip the
	// rollback has dug.  When the best chain tip itself is a locally-mined
	// block that no peer's main chain contains (or the fork runs deeper than
	// one block), a rollback to best.Height-1 fails to extend again and the
	// front keeps failing: each repeated rollback at the same height deepens
	// the cut by one more block until the download resumes from a height the
	// network actually shares.  Only touched from the blockHandler goroutine.
	fabricatedRollbackDepth int32

	// suspiciousHeaders remembers the first-header hashes of header ranges
	// that failed to apply and were rolled back as fabricated or forked, so a
	// peer that keeps re-serving the same bogus chain is detected immediately.
	// Bounded to avoid unbounded growth.  Only touched from the blockHandler
	// goroutine.
	suspiciousHeaders map[chainhash.Hash]struct{}

	// frontUnreachable counts, per front range start height, how many distinct
	// peers have returned a response that does not extend the range (the
	// receive-side prev check failed).  When two distinct peers both fail to
	// extend the front, the header chain at that height is almost certainly
	// fabricated or forked -- roll back early instead of waiting for the slow
	// 10-minute block-side timer (P4).  Only touched from the blockHandler
	// goroutine.
	frontUnreachable map[int32]map[string]time.Time

	// lastUtxoFlush is the last time the UTXO cache was asked to flush.
	// It is used to trigger periodic flushes even while in initial block
	// download mode so that an unclean shutdown does not fall far behind the
	// chain tip and force a long reconstruction on the next start.
	lastUtxoFlush time.Time

	// lastSyncProgressHeight and lastSyncProgressTime track the previous
	// sample for the periodic sync progress log.
	lastSyncProgressHeight int32
	lastSyncProgressTime   time.Time

	// An optional fee estimator.
	feeEstimator *mempool.FeeEstimator
}

// findNextHeaderCheckpoint returns the next checkpoint after the passed height.
// It returns nil when there is not one either because the height is already
// later than the final checkpoint or some other reason such as disabled
// checkpoints.
func (sm *SyncManager) findNextHeaderCheckpoint(height int32) *chaincfg.Checkpoint {
	checkpoints := sm.chain.Checkpoints()
	if len(checkpoints) == 0 {
		return nil
	}

	// There is no next checkpoint if the height is already after the final
	// checkpoint.
	finalCheckpoint := &checkpoints[len(checkpoints)-1]
	if height >= finalCheckpoint.Height {
		return nil
	}

	// Find the next checkpoint.
	nextCheckpoint := finalCheckpoint
	for i := len(checkpoints) - 2; i >= 0; i-- {
		if height >= checkpoints[i].Height {
			break
		}
		nextCheckpoint = &checkpoints[i]
	}
	return nextCheckpoint
}

// fetchHigherPeers returns all the peers that are at a higher block than the
// given height.  The peers that are not sync candidates are omitted from the
// returned list.
func (sm *SyncManager) fetchHigherPeers(height int32) []*peerpkg.Peer {
	higherPeers := make([]*peerpkg.Peer, 0, len(sm.peerStates))
	for peer, state := range sm.peerStates {
		if !state.syncCandidate {
			continue
		}

		if peer.LastBlock() <= height {
			continue
		}

		higherPeers = append(higherPeers, peer)
	}

	return higherPeers
}

// isInIBDMode returns true if there's more blocks needed to be downloaded to
// catch up to the latest chain tip.
func (sm *SyncManager) isInIBDMode() bool {
	best := sm.chain.BestSnapshot()
	higherPeers := sm.fetchHigherPeers(best.Height)
	if sm.chain.IsCurrent() && len(higherPeers) == 0 {
		return false
	}

	return true
}

// peerHost returns the host portion of the peer's address.  It is used to keep
// the parallel header download from treating multiple connections to the same
// remote node as distinct peers (btcd can transiently dial the same scarce
// address several times when the network has very few reachable nodes).
func peerHost(p *peerpkg.Peer) string {
	host, _, err := net.SplitHostPort(p.Addr())
	if err != nil {
		return p.Addr()
	}
	return host
}

// fetchHeaders starts the initial header download across several peers in
// parallel.  Each participating peer is handed a disjoint, contiguous slice of
// the header chain, and the slices are applied in order once they arrive.  The
// number of peers is capped by maxHeaderSyncPeers.
func (sm *SyncManager) fetchHeaders() {
	_, height := sm.chain.BestHeader()
	higherPeers := sm.fetchHigherPeers(height)
	if len(higherPeers) == 0 {
		log.Warnf("No sync peer candidates available")
		return
	}

	// Randomize the candidate order so we don't always favor the same peers.
	shuffled := make([]*peerpkg.Peer, len(higherPeers))
	for i, j := range rand.Perm(len(higherPeers)) {
		shuffled[i] = higherPeers[j]
	}

	// Deduplicate by host so duplicate connections to the same remote node
	// do not each consume a slice of the parallel download, then cap the
	// number of peers that take part in it.  Peers with an empty address
	// (test mocks) are always kept.
	seen := make(map[string]struct{}, len(shuffled))
	uniq := shuffled[:0]
	for _, p := range shuffled {
		host := peerHost(p)
		if host == "" {
			uniq = append(uniq, p)
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		uniq = append(uniq, p)
	}
	shuffled = uniq
	if len(shuffled) > maxHeaderSyncPeers {
		shuffled = shuffled[:maxHeaderSyncPeers]
	}

	// The download is complete once every header up to the tallest
	// participating peer has been applied to the header chain.
	target := int32(0)
	for _, p := range shuffled {
		if lastBlock := p.LastBlock(); lastBlock > target {
			target = lastBlock
		}
	}
	if target <= height {
		log.Warnf("No sync peer candidates available")
		return
	}

	sm.ibdMode = true
	sm.syncPeer = shuffled[0]
	sm.headerSync = &headerSyncState{
		peers:      shuffled,
		target:     target,
		nextHeight: height + 1,
		nextAssign: height + 1,
		ranges:     make(map[int32]*headerRange),
		peerRange:  make(map[*peerpkg.Peer]*headerRange),
		sliceLen:   wire.MaxBlockHeadersPerMsg,
	}

	log.Infof("Downloading headers for blocks %d to %d in parallel "+
		"from %d peers", height+1, target, len(shuffled))

	for _, p := range shuffled {
		sm.assignHeaderRange(p)
	}
}

// headerLocator returns a single-hash block locator rooted at the passed
// height so that a getheaders request asks the peer for the headers immediately
// after it.  A nil locator (request the whole chain) is only returned for a
// negative height, which never happens during the normal initial block download.
func (sm *SyncManager) headerLocator(height int32) blockchain.BlockLocator {
	if height < 0 {
		return nil
	}
	hash, err := sm.chain.HeaderHashByHeight(height)
	if err != nil {
		return nil
	}
	return blockchain.BlockLocator([]*chainhash.Hash{hash})
}

// launchHeaderRange records a new per-peer header range and issues the
// getheaders request for it.  It is a low-level helper; the caller is
// responsible for choosing non-overlapping start/end heights.  It returns
// true when the range was actually dispatched, and false when the header
// lead cap or the nil headerSync state suppressed the dispatch.
//
// The header lead cap is enforced here, at the single point every getheaders
// request passes through (fresh assignments from assignHeaderRange and
// re-issues from reissueHeaderRange alike), so no path can push the applied
// header tip more than headerLeadLimit ahead of the connected best chain.
// While the lead is below the cap a dispatch goes out immediately to push it
// back up; once it reaches the cap dispatching pauses so blocks can catch up.
func (sm *SyncManager) launchHeaderRange(peer *peerpkg.Peer, start int32) bool {
	hs := sm.headerSync
	if hs == nil {
		return false
	}
	// Cap the header lead over the connected best chain.  A lead beyond
	// maxBlockRequestWindow (8000) serves no purpose -- the block request
	// frontier never goes further than bestChain+8000 -- while a large lead
	// pushes the receive-side prev check (HeaderHashByHeight(start-1)) out
	// of the in-memory header window into the DB cold-read path, the
	// P8/P9 hazard.  Keeping the lead just below headerLeadLimit keeps the
	// header tip ahead of the blocks by design without racing arbitrarily
	// far (observed ~42k-68k lead before the cap).
	_, bestHeaderHeight := sm.chain.BestHeader()
	if bestHeaderHeight-sm.chain.BestSnapshot().Height > headerLeadLimit {
		hs.leadPaused = true
		return false
	}
	hs.leadPaused = false

	rng := &headerRange{
		start:      start,
		peer:       peer,
		assignedAt: time.Now(),
		confirmed:  true, // non-front ranges apply on the single response
	}
	hs.ranges[start] = rng
	hs.peerRange[peer] = rng

	peer.PushGetHeadersMsg(sm.headerLocator(start-1), &zeroHash)
	return true
}

// assignHeaderRange hands the next unassigned, non-overlapping slice of the
// header chain to the given peer.  It prefers to fill a hole at the front of
// the download (left by a peer that served fewer headers than expected) and
// otherwise extends the contiguous frontier.  The slice is capped by the peer's
// advertised tip and the per-message header limit.  It returns true if a range
// was assigned.
func (sm *SyncManager) assignHeaderRange(peer *peerpkg.Peer) bool {
	hs := sm.headerSync
	if hs == nil {
		return false
	}

	// A peer only ever has a single range in flight.
	if _, ok := hs.peerRange[peer]; ok {
		return false
	}

	// Cap the header lead over the connected best chain (see headerLeadLimit
	// above).  Once the applied header tip is far enough ahead to keep the
	// block request frontier (bestChain+8192) fully covered, no further
	// headers are needed; pausing here prevents the header tip from racing
	// arbitrarily far ahead, which would push the receive-side prev check
	// out of the in-memory header window into the DB cold-read path (the
	// P8/P9 hazard).  The block-side guard in assignBlockSlice refuses new
	// block slices when the header lead shrinks below the request window, so
	// the two guards together keep the lead in the band
	// [maxBlockRequestWindow, headerLeadLimit] while both downloads run.
	_, bestHeaderHeight := sm.chain.BestHeader()
	if bestHeaderHeight-sm.chain.BestSnapshot().Height > headerLeadLimit {
		return false
	}

	// C2 front double-send: when the front range is already held by another
	// peer but has not yet been confirmed (its direction decides the header
	// chain, so a misattributed front is the pollution entry point), hand the
	// same front range to this idle peer as well.  Two independent peers then
	// corroborate its first hash before it is applied; a forged or
	// misattributed front cannot gather the second agreeing vote.  Non-front
	// ranges are not double-sent (they keep the single-vote fast path).
	// The re-send stays active even after the first vote arrived (received)
	// as long as the front is still unconfirmed: the second vote can only
	// arrive from a peer that was handed the range, and dropping the re-send
	// once received would stall the front (and with it the whole download).
	if front := hs.ranges[hs.nextHeight]; front != nil &&
		!front.confirmed && hs.nextHeight <= hs.target {
		hs.peerRange[peer] = front
		peer.PushGetHeadersMsg(sm.headerLocator(hs.nextHeight-1), &zeroHash)
		return true
	}

	// Fill a front hole first; otherwise extend the frontier.
	start := hs.nextAssign
	if _, ok := hs.ranges[hs.nextHeight]; !ok && hs.nextHeight <= hs.target {
		start = hs.nextHeight
	}
	if start > hs.target {
		return false
	}

	end := start + hs.sliceLen
	if peerLastBlock := peer.LastBlock(); end > peerLastBlock+1 {
		end = peerLastBlock + 1
	}
	// Never overlap a slice already handed out beyond the start (this can
	// happen when a hole at the front is filled while back slices are still
	// in flight).
	if rng, ok := hs.nextRangeBeyond(start); ok {
		if nextStart := rng.start; nextStart < end {
			end = nextStart
		}
	}
	if end <= start {
		return false
	}

	// The dispatch may be suppressed by the header lead cap (enforced inside
	// launchHeaderRange).  When that happens, do not advance the frontier or
	// report success -- the header tip is already as far ahead as allowed and
	// the caller should leave the frontier untouched until blocks catch up.
	if !sm.launchHeaderRange(peer, start) {
		return false
	}

	// Only advance the contiguous frontier for fresh (non hole-filling)
	// assignments.
	if start == hs.nextAssign {
		hs.nextAssign = end
	}
	return true
}

// reissueHeaderRange re-issues a specific slice to a peer that can serve it
// after a slow or unresponsive peer has stalled on it.  The frontier is not
// modified so a re-issue never duplicates or overlaps other ranges.
func (sm *SyncManager) reissueHeaderRange(peer *peerpkg.Peer, start int32) {
	hs := sm.headerSync
	if hs == nil {
		return
	}
	if _, ok := hs.peerRange[peer]; ok {
		return
	}
	if start > hs.target {
		return
	}

	end := start + hs.sliceLen
	if peerLastBlock := peer.LastBlock(); end > peerLastBlock+1 {
		end = peerLastBlock + 1
	}
	if end <= start {
		return
	}

	// Cap the re-issued slice at the next already-assigned range so it never
	// overlaps ranges handed out beyond it.
	if rng, ok := hs.nextRangeBeyond(start); ok {
		if nextStart := rng.start; nextStart < end {
			end = nextStart
		}
	}
	if end <= start {
		return
	}

	// The dispatch may be suppressed by the header lead cap (enforced inside
	// launchHeaderRange); when that happens there is nothing left to do here.
	sm.launchHeaderRange(peer, start)
}

// nextRangeBeyond returns the already-assigned header range with the smallest
// start height greater than the passed start, if any.
func (hs *headerSyncState) nextRangeBeyond(start int32) (*headerRange, bool) {
	var best *headerRange
	for s, rng := range hs.ranges {
		if s <= start {
			continue
		}
		if best == nil || s < best.start {
			best = rng
		}
	}
	return best, best != nil
}

// startSync will choose the best peer among the available candidate peers to
// download/sync the blockchain from.  When syncing is already running, it
// simply returns.  It also examines the candidates for any which are no longer
// candidates and removes them as needed.
func (sm *SyncManager) startSync() {
	// Return now if we're already syncing.
	if sm.syncPeer != nil {
		return
	}

	// Check to see if we're in the initial block download mode.
	if !sm.isInIBDMode() {
		return
	}

	// If we're in the initial block download mode, check if we have
	// peers that we can download headers from.
	_, bestHeaderHeight := sm.chain.BestHeader()
	higherHeaderPeers := sm.fetchHigherPeers(bestHeaderHeight)
	if len(higherHeaderPeers) != 0 {
		sm.fetchHeaders()

		// Reset the last progress time now that we have a
		// non-nil syncPeer to avoid the stall handler firing
		// before any headers have been received.
		if sm.syncPeer != nil {
			sm.lastProgressTime = time.Now()
		}
		return
	}

	// We don't have any more headers to download at this
	// point so start asking for blocks.
	best := sm.chain.BestSnapshot()
	higherPeers := sm.fetchHigherPeers(best.Height)

	// Pick randomly from the set of peers greater than our
	// block height, falling back to a random peer of the same
	// height if none are greater.
	//
	// TODO(conner): Use a better algorithm to ranking peers based on
	// observed metrics and/or sync in parallel.
	var bestPeer *peerpkg.Peer
	switch {
	case len(higherPeers) > 0:
		bestPeer = higherPeers[rand.Intn(len(higherPeers))]
	}

	if bestPeer == nil {
		log.Warnf("No sync peer candidates available")
		return
	}

	sm.syncPeer = bestPeer
	sm.ibdMode = true

	// Reset the last progress time now that we have a non-nil
	// syncPeer to avoid instantly detecting it as stalled in the
	// event the progress time hasn't been updated recently.
	sm.lastProgressTime = time.Now()

	log.Infof("Syncing to block height %d from peer %v",
		sm.syncPeer.LastBlock(), sm.syncPeer.Addr())

	// Kick off a parallel block download using all of the higher peers
	// (deduplicated by host and capped by blockSyncAddPeer).  Each accepted
	// peer is immediately handed a disjoint slice of blocks to download.
	sm.blockSync = make([]*peerpkg.Peer, 0, len(higherPeers))
	sm.blockSyncAddPeer(bestPeer)
	for _, p := range higherPeers {
		sm.blockSyncAddPeer(p)
	}

	// Resume any block download that was interrupted by a restart before
	// dispatching fresh block requests, so locally-stored blocks are connected
	// instead of being skipped.
	sm.reconnectStoredBlocks()
}

// isSyncCandidate returns whether or not the peer is a candidate to consider
// syncing from.
func (sm *SyncManager) isSyncCandidate(peer *peerpkg.Peer) bool {
	var (
		nodeServices = peer.Services()
		fullNode     = nodeServices.HasFlag(wire.SFNodeNetwork)
		prunedNode   = nodeServices.HasFlag(wire.SFNodeNetworkLimited)
	)

	// We check the node's ability to serve blocks first.
	switch {
	case fullNode:
		// Node is a sync candidate if it has all the blocks.

	case prunedNode:
		// Even if the peer is pruned, if they have the node network
		// limited flag, they are able to serve 2 days worth of blocks
		// from the current tip. Therefore, check if our chaintip is
		// within that range.
		bestHeight := sm.chain.BestSnapshot().Height
		peerLastBlock := peer.LastBlock()

		// bestHeight+1 as we need the peer to serve us the next block,
		// not the one we already have.
		if bestHeight+1 <=
			peerLastBlock-wire.NodeNetworkLimitedBlockThreshold {

			return false
		}

	default:
		// If the peer isn't an archival node, and it's not signaling
		// NODE_NETWORK_LIMITED, we can't sync off of this node.
		return false
	}

	// We can skip the deployment requirement for local test networks.
	switch sm.chainParams.Name {
	case chaincfg.RegressionNetParams.Name, chaincfg.SimNetParams.Name:
		// Being able to serve blocks in the range we need is the only
		// requirement for regtest and simnet. Any light clients such as
		// Neutrino would fail above already.
		return true
	}

	// If the segwit soft-fork package has activated, then the peer must
	// also be upgraded.
	segwitActive, err := sm.chain.IsDeploymentActive(
		chaincfg.DeploymentSegwit,
	)
	if err != nil {
		log.Errorf("Unable to query for segwit soft-fork state: %v",
			err)
	}

	if segwitActive && !peer.IsWitnessEnabled() {
		return false
	}

	// Candidate if all checks passed.
	return true
}

// handleNewPeerMsg deals with new peers that have signalled they may
// be considered as a sync peer (they have already successfully negotiated).  It
// also starts syncing if needed.  It is invoked from the syncHandler goroutine.
func (sm *SyncManager) handleNewPeerMsg(peer *peerpkg.Peer) {
	// Ignore if in the process of shutting down.
	if atomic.LoadInt32(&sm.shutdown) != 0 {
		return
	}

	log.Infof("New valid peer %s (%s)", peer, peer.UserAgent())

	// Initialize the peer state.
	isSyncCandidate := sm.isSyncCandidate(peer)
	sm.peerStates[peer] = &peerSyncState{
		syncCandidate:   isSyncCandidate,
		requestedTxns:   make(map[chainhash.Hash]struct{}),
		requestedBlocks: make(map[chainhash.Hash]struct{}),
	}

	// Start syncing by choosing the best candidate if needed.
	if isSyncCandidate && sm.syncPeer == nil {
		sm.startSync()
	}

	// If a parallel header download is already running, fold this new peer in
	// as soon as it connects so the download ramps up to the maximum number of
	// participating peers rather than waiting for the next header round.
	if isSyncCandidate && sm.headerSync != nil {
		sm.headerSyncAddPeer(peer)
	}

	// Similarly, if a parallel initial block download is already running, fold
	// the new peer in so additional peers can serve block slices.
	if isSyncCandidate && sm.ibdMode && sm.blockSync != nil {
		sm.blockSyncAddPeer(peer)
	}
}

// headerSyncAddPeer folds a newly-connected sync candidate into an in-progress
// parallel header download, granting it a slice of the header chain to serve.
// The set of participating peers grows toward maxHeaderSyncPeers as peers
// connect, and a peer that is taller than the current frontier widens the target
// the download is working toward.
func (sm *SyncManager) headerSyncAddPeer(peer *peerpkg.Peer) {
	hs := sm.headerSync
	if hs == nil {
		return
	}

	// Only accept peers that advertise headers we don't already have.
	_, bestHeaderHeight := sm.chain.BestHeader()
	if peer.LastBlock() <= bestHeaderHeight {
		return
	}

	// Don't exceed the parallel-download limit or admit the same peer twice.
	if len(hs.peers) >= maxHeaderSyncPeers {
		return
	}
	host := peerHost(peer)
	for _, p := range hs.peers {
		if p == peer {
			return
		}
		if host != "" && peerHost(p) == host {
			return
		}
	}

	// Widen the target if this peer is taller than the current one.
	if lastBlock := peer.LastBlock(); lastBlock > hs.target {
		hs.target = lastBlock
	}

	hs.peers = append(hs.peers, peer)
	sm.assignHeaderRange(peer)

	log.Infof("Added peer %v to the parallel header download (%d peers active)",
		peer.Addr(), len(hs.peers))
}

// blockSyncAddPeer folds a newly-connected sync candidate into an in-progress
// parallel initial block download and immediately dispatches an initial block
// request to it so it starts contributing right away.  The set of participating
// peers grows toward maxHeaderSyncPeers as peers connect.
func (sm *SyncManager) blockSyncAddPeer(peer *peerpkg.Peer) {
	if sm.blockSync == nil || peer == nil {
		return
	}

	if len(sm.blockSync) >= maxHeaderSyncPeers {
		return
	}
	host := peerHost(peer)
	for _, p := range sm.blockSync {
		if p == peer {
			return
		}
		if host != "" && peerHost(p) == host {
			return
		}
	}

	sm.blockSync = append(sm.blockSync, peer)
	sm.fetchHeaderBlocks(peer)

	log.Infof("Added peer %v to the parallel block download (%d peers active)",
		peer.Addr(), len(sm.blockSync))
}

// foldExistingSyncPeers extends the parallel block download set with any peers
// that are already connected and are sync candidates but did not take part in
// the most recent header round.  finishHeaderSync rebuilds the participating
// set from the header-round peers alone, which can be as small as one peer when
// a small header catch-up restarts the download on top of a running sync.  The
// set normally only grows when a brand new peer connects (handleNewPeerMsg),
// so without re-folding those already-connected peers would stay idle and the
// download would run on a fraction of the available parallelism.
func (sm *SyncManager) foldExistingSyncPeers() {
	if sm.blockSync == nil {
		return
	}
	for peer, state := range sm.peerStates {
		if len(sm.blockSync) >= maxHeaderSyncPeers {
			return
		}
		if peer == nil || state == nil || !state.syncCandidate {
			continue
		}
		if !peer.Connected() {
			continue
		}

		// Dedup by pointer and host, mirroring blockSyncAddPeer so multiple
		// connections to the same host count once.
		host := peerHost(peer)
		dup := false
		for _, p := range sm.blockSync {
			if p == peer {
				dup = true
				break
			}
			if host != "" && peerHost(p) == host {
				dup = true
				break
			}
		}
		if dup {
			continue
		}

		sm.blockSync = append(sm.blockSync, peer)
		log.Infof("Added peer %v to the parallel block download (%d peers active)",
			peer.Addr(), len(sm.blockSync))
	}
}

// blkDownload tops up every participating block-download peer that has drained
// below the minimum in-flight threshold.  Each peer owns a disjoint height slice
// (see assignBlockSlice), so different peers request different parts of the
// chain in parallel; once a peer's slice is fully requested it is released and
// the next top-up hands the peer the next slice.
func (sm *SyncManager) blkDownload() {
	// First connect any locally-stored blocks that were left behind by a
	// previous session, so a resumed download does not stall on data it
	// already has.
	sm.reconnectStoredBlocks()

	for _, p := range sm.blockSync {
		if p == nil {
			continue
		}
		state := sm.peerStates[p]
		if state == nil || len(state.requestedBlocks) >= minInFlightBlocks {
			continue
		}
		sm.fetchHeaderBlocks(p)
	}
}

// handleStallSample will switch to a new sync peer if the current one has
// stalled. This is detected when by comparing the last progress timestamp with
// the current time, and disconnecting the peer if we stalled before reaching
// their highest advertised block.
func (sm *SyncManager) handleStallSample() {
	if atomic.LoadInt32(&sm.shutdown) != 0 {
		return
	}

	// First re-issue any header range that has been in flight too long so a
	// slow peer cannot stall the parallel header download.
	if sm.headerSync != nil {
		sm.reissueStaleHeaderRanges()
	}

	// Likewise re-issue any block slice that has been in flight too long so
	// a slow peer cannot stall the parallel block download.
	if sm.blockSyncState != nil {
		sm.reissueStaleBlockSlices()
		sm.reissueStalledBlockPeers()

		// Top up every drained peer even when no block has arrived to
		// trigger blkDownload.  blkDownload normally runs on block receipt,
		// so a download that stalled with no in-flight requests (every
		// slice released, the front request freed, and no peer delivering)
		// would otherwise sit idle forever: nothing arrives to re-trigger
		// it.  Running it here lets assignBlockSlice re-issue the freed
		// front (see its missing-front fallback) and restart the download.
		sm.blkDownload()
	}

	// Fast rollback for an unreachable header front: when two distinct
	// peers have both returned responses that do not extend the front range,
	// the header chain at that height is almost certainly fabricated or
	// forked.  Roll back immediately instead of waiting for the slow
	// 10-minute block-side timer (P4), so a pollution cycle costs ~1-2
	// minutes instead of 20+.
	if sm.headerSync != nil {
		frontStart := sm.headerSync.nextHeight
		// A peer that returns headers failing the prev-connection test at the
		// front is a strong signal the local chain has forked below the front
		// (e.g. a locally-mined block persisted as best-chain tip that no
		// peer's main chain contains).  Every honest peer's header at
		// frontStart links to the real main chain, which differs from the
		// local fork.  Requiring at least two distinct do-not-extend votes
		// filters the single lagging/forked peer false positive that a lone
		// divergent peer would otherwise trigger; a sparse network that can
		// never reach two votes still falls back to the P4 block-side timer,
		// so the rollback cannot be skipped forever.
		// 对等点返回的 header 在 front 处未通过 prev 连接校验,是本地链在
		// front 之下已分叉的强信号(如本地挖出并持久化成 best-chain tip、
		// 但对等点主链上没有的块)。诚实对等点在 frontStart 的 header 都
		// 连向真实主链,与本地分叉不同。要求至少两个不同对等点的
		// do-not-extend 票,过滤单个滞后/分叉对等点造成的误报;始终凑不齐
		// 两票的稀疏网络仍会回退到 P4 块侧定时器,回滚不可能被永久跳过。
		if peers := sm.frontUnreachable[frontStart]; len(peers) >= 2 {
			log.Warnf("Front header range %d unreachable from %d distinct "+
				"peers -- fabricated/forked header chain, rolling back early",
				frontStart, len(peers))
			delete(sm.frontUnreachable, frontStart)
			sm.rollbackFabricatedHeaderChain()
		}
	}

	// Detect a best-chain tip that is not the main-chain block at its own
	// height.  A locally-mined block that no peer's chain contains can be
	// persisted as the best-chain tip (and best-tip snapshot): after a
	// restart the connected chain then diverges from the header chain at
	// that height, and every main-chain block above it arrives as an orphan
	// forever -- the block download stalls at the divergent tip while
	// headers keep advancing (observed: best-chain tip a23e7e62 vs
	// main-chain 44060189 b345517e, 2838 orphans, 0.00 bl/s).  The DB
	// height index still points at the real main-chain hash, so compare the
	// tip against it and roll back immediately instead of waiting for the
	// 10-minute P4 timer.
	// 检测 best chain tip 不是其自身高度上的主链块。本地挖出、无对等点主链
	// 包含的块可能被持久化为 best-chain tip(及 best-tip 快照):重启后连接链
	// 在该高度与 header 链分叉,其上所有主链块永远以孤儿到达——block 下载在
	// 分歧 tip 处卡死而 header 持续前进(实测:best-chain tip a23e7e62 vs
	// 主链 44060189 b345517e,2838 个孤儿,0.00 bl/s)。DB 高度索引仍指向真实
	// 主链 hash,因此把 tip 与它比较,不一致立即回滚,而不是等 10 分钟 P4。
	if sm.headerSync != nil {
		bestTip := sm.chain.BestSnapshot()
		if dbHash, err := sm.chain.MainChainHashByHeight(bestTip.Height); err == nil &&
			!dbHash.IsEqual(&bestTip.Hash) {
			log.Warnf("Best chain tip %v (height %d) is not the main-chain "+
				"block %v -- fabricated/forked tip persisted, rolling back "+
				"early", bestTip.Hash, bestTip.Height, dbHash)
			sm.rollbackFabricatedHeaderChain()
		}
	}

	// Second divergence detector, for the case where the DB height index was
	// itself rebuilt from the polluted chain (so the check above sees equal
	// hashes and cannot fire): compare the best-chain tip height against the
	// height advertised by the sync peer.  The peer's advertised height comes
	// from the network and cannot be polluted by a local fork, so when the
	// local tip sits far below it AND the block download has not advanced for
	// a short while, the local tip is almost certainly not on the real main
	// chain -- roll back early instead of waiting for the 10-minute P4 timer.
	// 第二重分叉检测,针对 DB 高度索引本身被污染链重建的情况(上面按 hash 的
	// 检测会因两值相等而无法触发):把 best chain tip 高度与同步对等点通告的
	// 高度比较。对等点通告高度来自网络,不可能被本地分叉污染,因此当本地 tip
	// 远低于它、且 block 下载短暂停滞时,本地 tip 几乎肯定不在真实主链上——
	// 提前回滚,而不是等 10 分钟 P4。
	if sm.headerSync != nil && sm.syncPeer != nil {
		bestTip := sm.chain.BestSnapshot()
		peerHeight := sm.syncPeer.LastBlock()
		if peerHeight < sm.syncPeer.StartingHeight() {
			peerHeight = sm.syncPeer.StartingHeight()
		}
		// Only fire once the download has demonstrably stalled (the front
		// height has not moved for over a minute), so a normal catch-up that
		// is merely behind the peer is not mistaken for a fork.
		// 仅当下载确实停滞(front 高度超过一分钟未推进)才触发,避免把正常
		// 追赶(只是落后于对等点)误判为分叉。
		stalled := bestTip.Height == sm.blockMissingHeight &&
			time.Since(sm.blockMissingSince) > time.Minute
		if peerHeight > bestTip.Height && stalled {
			log.Warnf("Best chain tip (height %d) is %d blocks behind the "+
				"sync peer (height %d) with the download stalled -- "+
				"fabricated/forked tip suspected, rolling back early",
				bestTip.Height, peerHeight-bestTip.Height, peerHeight)
			sm.rollbackFabricatedHeaderChain()
		}
	}

	// Third divergence detector (P1-1): the header chain is the network's
	// projection and cannot be polluted by a local fork, so compare the
	// best-chain tip against the header chain at the same height.  When the
	// header chain has advanced past the tip AND the tip is a different
	// block than the header chain has at that height, the local tip is a
	// fabricated/forked block occupying the best chain (observed: a23e7e62
	// vs header-chain b345517e at 44060189).  Roll back to the fork point
	// immediately -- this is the most direct "network wins" signal, stronger
	// than the peer-height heuristic because it compares hashes, not just
	// heights.  It complements the DB height-index check (which can be
	// polluted) and fires even when no peer has been selected yet.
	// 第三重分叉检测(P1-1):header 链是网络的投影,不可能被本地分叉污染,因此
	// 把 best chain tip 与 header 链同高度块比较。当 header 链已越过 tip、且
	// 该高度上 tip 与 header 链块不同,本地 tip 就是占据 best chain 的伪造/
	// 分叉块(实测:44060189 处 a23e7e62 vs header 链 b345517e)。立即回滚到
	// 分叉点——这是最直接的"网络胜出"信号,比 peer 高度启发式更强(比较
	// hash 而非仅高度)。它补充 DB 高度索引检测(可能被污染),且即使尚未选中
	// 对等点也能触发。
	if sm.headerSync != nil && sm.chain.HeaderChainDiverged() {
		forkHeight := sm.chain.BestChainHeaderForkHeight()
		log.Warnf("Best chain tip diverges from the header chain at height "+
			"%d (fork at %d) -- fabricated/forked tip, rolling back to fork",
			sm.chain.BestSnapshot().Height, forkHeight)
		sm.rollbackToForkPoint(forkHeight)
	}

	// Detect a fabricated or forked header chain: the block download has not
	// advanced for blockUnavailableTimeout.  A real chain's blocks are served
	// by the network; a forged chain's blocks either do not exist on any peer
	// or do not link to the applied tip, so the download would spin forever
	// (observed: a header index polluted with a real block at the wrong height
	// froze the block download at 340774 while headers kept advancing).  Roll
	// the header chain back and resync instead of stalling indefinitely.
	//
	// The check keys on the front height not advancing, NOT on the front hash
	// being present in requestedBlocks: a deadlock can free the front request
	// entirely (all slices released, no peer delivering), leaving requestedBlocks
	// empty while the front never advances -- exactly the case that must still
	// be detected.
	if sm.blockSyncState != nil {
		best := sm.chain.BestSnapshot().Height
		if best != sm.blockMissingHeight {
			// The front advanced (or this is the first sample); restart the
			// stuck timer at the current height.
			sm.blockMissingHeight = best
			sm.blockMissingSince = time.Now()
		} else if time.Since(sm.blockMissingSince) > blockUnavailableTimeout {
			log.Warnf("Block download stuck at height %d for %v -- suspected "+
				"fabricated/forked header chain, rolling back",
				best, blockUnavailableTimeout)
			sm.rollbackFabricatedHeaderChain()
		}
	}

	// If we don't have an active sync peer, exit early.
	if sm.syncPeer == nil {
		return
	}

	// If the stall timeout has not elapsed, exit early.
	if time.Since(sm.lastProgressTime) <= maxStallDuration {
		return
	}

	// Check to see that the peer's sync state exists.
	state, exists := sm.peerStates[sm.syncPeer]
	if !exists {
		return
	}

	sm.clearRequestedState(state)

	disconnectSyncPeer := sm.shouldDCStalledSyncPeer()
	sm.updateSyncPeer(disconnectSyncPeer)
}

// logSyncProgress logs a one line INFO progress report of the block download.
// It only logs while the node is behind the chain (not current), so the output
// is quiet once the node reaches the tip.  The line mirrors what an operator
// watching sync wants every minute: current height, the per-interval delta,
// the average blocks/sec, the share of the chain synced and an ETA based on
// the best known header height.
func (sm *SyncManager) logSyncProgress() {
	if atomic.LoadInt32(&sm.shutdown) != 0 {
		return
	}

	// Only report while we are behind the chain.
	if sm.current() {
		sm.lastSyncProgressHeight = 0
		sm.lastSyncProgressTime = time.Time{}
		return
	}

	height := sm.chain.BestSnapshot().Height
	_, tipHeight := sm.chain.BestHeader()
	if tipHeight < height {
		tipHeight = height
	}

	now := time.Now()
	if sm.lastSyncProgressTime.IsZero() {
		sm.lastSyncProgressTime = now
		sm.lastSyncProgressHeight = height
		return
	}

	elapsed := now.Sub(sm.lastSyncProgressTime).Seconds()
	if elapsed <= 0 {
		elapsed = 1
	}
	delta := height - sm.lastSyncProgressHeight
	rate := float64(delta) / elapsed

	var pct, etaH float64
	if tipHeight > 0 {
		pct = float64(height) / float64(tipHeight) * 100
	}
	if rate > 0 && tipHeight > height {
		etaH = float64(tipHeight-height) / rate / 3600
	}

	parts := []string{
		fmt.Sprintf("height=%d", height),
		fmt.Sprintf("%+d in %.0fs", delta, elapsed),
		fmt.Sprintf("%.2f bl/s", rate),
	}
	if tipHeight > 0 {
		parts = append(parts, fmt.Sprintf("synced=%.5f%%", pct))
	}
	if etaH > 0 {
		parts = append(parts, fmt.Sprintf("ETA=%.1fh", etaH))
	} else {
		parts = append(parts, "stalled or at tip")
	}

	log.Infof("Sync progress: %s", strings.Join(parts, "  "))

	sm.lastSyncProgressTime = now
	sm.lastSyncProgressHeight = height
}

// shouldDCStalledSyncPeer determines whether or not we should disconnect a
// stalled sync peer. If the peer has stalled and its reported height is greater
// than our own best height, we will disconnect it. Otherwise, we will keep the
// peer connected in case we are already at tip.
func (sm *SyncManager) shouldDCStalledSyncPeer() bool {
	lastBlock := sm.syncPeer.LastBlock()
	startHeight := sm.syncPeer.StartingHeight()

	var peerHeight int32
	if lastBlock > startHeight {
		peerHeight = lastBlock
	} else {
		peerHeight = startHeight
	}

	// If we've stalled out yet the sync peer reports having more blocks for
	// us we will disconnect them. This allows us at tip to not disconnect
	// peers when we are equal or they temporarily lag behind us.
	best := sm.chain.BestSnapshot()
	return peerHeight > best.Height
}

// handleDonePeerMsg deals with peers that have signalled they are done.  It
// removes the peer as a candidate for syncing and in the case where it was
// the current sync peer, attempts to select a new best peer to sync from.  It
// is invoked from the syncHandler goroutine.
func (sm *SyncManager) handleDonePeerMsg(peer *peerpkg.Peer) {
	state, exists := sm.peerStates[peer]
	if !exists {
		log.Warnf("Received done peer message for unknown peer %s", peer)
		return
	}

	// Remove the peer from the list of candidate peers.
	delete(sm.peerStates, peer)

	log.Infof("Lost peer %s", peer)

	sm.clearRequestedState(state)

	// If the peer was taking part in a parallel header download, free its
	// range and remove it from the set of participating peers so its slice
	// can be re-issued to a remaining peer.
	if sm.headerSync != nil {
		sm.dropHeaderPeer(peer)
	}

	// If the peer was taking part in a parallel block download, drop it from
	// the participating set and release the slice it owned so its heights are
	// re-issued to a remaining peer.  clearRequestedState above already freed
	// all of its in-flight blocks back to the global requestedBlocks map.
	if sm.blockSync != nil {
		sm.releaseBlockSlice(peer)
		for i, p := range sm.blockSync {
			if p == peer {
				sm.blockSync = append(sm.blockSync[:i], sm.blockSync[i+1:]...)
				break
			}
		}
	}

	if peer == sm.syncPeer {
		// Update the sync peer. The server has already disconnected the
		// peer before signaling to the sync manager.
		sm.updateSyncPeer(false)
	}
}

// clearRequestedState wipes all expected transactions and blocks from the sync
// manager's requested maps that were requested under a peer's sync state, This
// allows them to be rerequested by a subsequent sync peer.
func (sm *SyncManager) clearRequestedState(state *peerSyncState) {
	// Remove requested transactions from the global map so that they will
	// be fetched from elsewhere next time we get an inv.
	for txHash := range state.requestedTxns {
		delete(sm.requestedTxns, txHash)
	}

	// Remove requested blocks from the global map so that they will be
	// fetched from elsewhere next time we get an inv.
	// TODO: we could possibly here check which peers have these blocks
	// and request them now to speed things up a little.
	for blockHash := range state.requestedBlocks {
		delete(sm.requestedBlocks, blockHash)
	}
}

// updateSyncPeer choose a new sync peer to replace the current one. If
// dcSyncPeer is true, this method will also disconnect the current sync peer.
// If we are in header first mode, any header state related to prefetching is
// also reset in preparation for the next sync peer.
func (sm *SyncManager) updateSyncPeer(dcSyncPeer bool) {
	log.Debugf("Updating sync peer, no progress for: %v",
		time.Since(sm.lastProgressTime))

	// First, disconnect the current sync peer if requested.
	if dcSyncPeer {
		sm.syncPeer.Disconnect()
	}

	sm.syncPeer = nil
	sm.startSync()
}

// handleTxMsg handles transaction messages from all peers.
func (sm *SyncManager) handleTxMsg(tmsg *txMsg) {
	peer := tmsg.peer
	state, exists := sm.peerStates[peer]
	if !exists {
		log.Warnf("Received tx message from unknown peer %s", peer)
		return
	}

	// NOTE:  BitcoinJ, and possibly other wallets, don't follow the spec of
	// sending an inventory message and allowing the remote peer to decide
	// whether or not they want to request the transaction via a getdata
	// message.  Unfortunately, the reference implementation permits
	// unrequested data, so it has allowed wallets that don't follow the
	// spec to proliferate.  While this is not ideal, there is no check here
	// to disconnect peers for sending unsolicited transactions to provide
	// interoperability.
	txHash := tmsg.tx.Hash()

	// Ignore transactions that we have already rejected.  Do not
	// send a reject message here because if the transaction was already
	// rejected, the transaction was unsolicited.
	if _, exists = sm.rejectedTxns[*txHash]; exists {
		log.Debugf("Ignoring unsolicited previously rejected "+
			"transaction %v from %s", txHash, peer)
		return
	}

	// Process the transaction to include validation, insertion in the
	// memory pool, orphan handling, etc.
	acceptedTxs, err := sm.txMemPool.ProcessTransaction(tmsg.tx,
		true, true, mempool.Tag(peer.ID()))

	// Remove transaction from request maps. Either the mempool/chain
	// already knows about it and as such we shouldn't have any more
	// instances of trying to fetch it, or we failed to insert and thus
	// we'll retry next time we get an inv.
	delete(state.requestedTxns, *txHash)
	delete(sm.requestedTxns, *txHash)

	if err != nil {
		// Do not request this transaction again until a new block
		// has been processed.
		limitAdd(sm.rejectedTxns, *txHash, maxRejectedTxns)

		// When the error is a rule error, it means the transaction was
		// simply rejected as opposed to something actually going wrong,
		// so log it as such.  Otherwise, something really did go wrong,
		// so log it as an actual error.
		if _, ok := err.(mempool.RuleError); ok {
			log.Debugf("Rejected transaction %v from %s: %v",
				txHash, peer, err)
		} else {
			log.Errorf("Failed to process transaction %v: %v",
				txHash, err)
		}

		// Convert the error into an appropriate reject message and
		// send it.
		code, reason := mempool.ErrToRejectErr(err)
		peer.PushRejectMsg(wire.CmdTx, code, reason, txHash, false)
		return
	}

	sm.peerNotifier.AnnounceNewTransactions(acceptedTxs)
}

// current returns true if we believe we are synced with our peers, false if we
// still have blocks to check
func (sm *SyncManager) current() bool {
	if !sm.chain.IsCurrent() {
		return false
	}

	// if blockChain thinks we are current and we have no syncPeer it
	// is probably right.
	if sm.syncPeer == nil {
		return true
	}

	// No matter what chain thinks, if we are below the block we are syncing
	// to we are not current.
	if sm.chain.BestSnapshot().Height < sm.syncPeer.LastBlock() {
		return false
	}
	return true
}

// checkHeadersList checks if the sync manager is in the initial block download
// mode and returns if the given block hash is a checkpointed block and the
// behavior flags for this block.  If the block is still under the checkpoint,
// then it's given the fast-add flag.
func (sm *SyncManager) checkHeadersList(blockHash *chainhash.Hash) (
	bool, blockchain.BehaviorFlags) {

	// Always return false and BFNone if we're not in ibd mode.
	if !sm.ibdMode {
		return false, blockchain.BFNone
	}

	isCheckpointBlock := false
	behaviorFlags := blockchain.BFNone

	// If we don't already know this is a valid header, return false and
	// BFNone.
	if !sm.chain.IsValidHeader(blockHash) {
		return false, blockchain.BFNone
	}

	height, err := sm.chain.HeaderHeightByHash(*blockHash)
	if err != nil {
		return false, blockchain.BFNone
	}

	// Since findNextHeaderCheckpoint returns the next checkpoint after the
	// passed height, we do a -1 to include the current block.
	checkpoint := sm.findNextHeaderCheckpoint(height - 1)
	if checkpoint == nil {
		return false, blockchain.BFNone
	}

	behaviorFlags |= blockchain.BFFastAdd
	if blockHash.IsEqual(checkpoint.Hash) {
		isCheckpointBlock = true
	}

	return isCheckpointBlock, behaviorFlags
}

// handleBlockMsg handles block messages from all peers.
func (sm *SyncManager) handleBlockMsg(bmsg *blockMsg) {
	peer := bmsg.peer
	state, exists := sm.peerStates[peer]
	if !exists {
		log.Warnf("Received block message from unknown peer %s", peer)
		return
	}

	// If we didn't ask for this block then the peer is misbehaving.
	blockHash := bmsg.block.Hash()
	if _, exists = state.requestedBlocks[*blockHash]; !exists {
		// The regression test intentionally sends some blocks twice
		// to test duplicate block insertion fails.  Don't disconnect
		// the peer or ignore the block when we're in regression test
		// mode in this case so the chain code is actually fed the
		// duplicate blocks.
		if sm.chainParams.Name != chaincfg.RegressionNetParams.Name {
			log.Warnf("Got unrequested block %v from %s -- "+
				"disconnecting", blockHash, peer.Addr())
			peer.Disconnect()
			return
		}
	}

	// Check if the block is eligible for less validation since the headers
	// have already been verified to link together and are valid up to the
	// next checkpoint.
	isCheckpointBlock, behaviorFlags := sm.checkHeadersList(blockHash)

	// Remove block from request maps. Either chain will know about it and
	// so we shouldn't have any more instances of trying to fetch it, or we
	// will fail the insert and thus we'll retry next time we get an inv.
	delete(state.requestedBlocks, *blockHash)
	delete(sm.requestedBlocks, *blockHash)

	// Record the peer's delivery so a parallel block-download peer that has
	// stopped producing data can be detected and freed by the stall handler,
	// and advance the peer's slice download progress so the RPC layer can
	// report how far each peer has actually fetched within its slice.
	if bs := sm.blockSyncState; bs != nil {
		bs.lastProgress[peer] = time.Now()

		// The delivered block's height: since version-2 blocks serialize
		// their height into the coinbase, extract it so progress is counted
		// even when the block is waiting for its parent to connect (it is a
		// genuine download, not a best-chain connection).
		var height int32
		hdr := bmsg.block.MsgBlock().Header
		if blockchain.ShouldHaveSerializedBlockHeight(&hdr) {
			if txs := bmsg.block.Transactions(); len(txs) > 0 {
				if h, herr := blockchain.ExtractCoinbaseHeight(txs[0]); herr == nil {
					height = int32(h)
				}
			}
		}
		if height <= 0 {
			height = bmsg.block.Height()
		}
		if height > 0 {
			if sl, ok := bs.peerSlice[peer]; ok && sl != nil &&
				height >= sl.start && height < sl.end && height > sl.received {
				sl.received = height
			}
		}
	}

	// Process the block to include validation, best chain selection, orphan
	// handling, etc.
	_, isOrphan, err := sm.chain.ProcessBlock(bmsg.block, behaviorFlags)
	if err != nil {
		// When the error is a rule error, it means the block was simply
		// rejected as opposed to something actually going wrong, so log
		// it as such.  Otherwise, something really did go wrong, so log
		// it as an actual error.
		if _, ok := err.(blockchain.RuleError); ok {
			log.Infof("Rejected block %v from %s: %v", blockHash,
				peer, err)
		} else {
			log.Errorf("Failed to process block %v: %v",
				blockHash, err)
		}
		if dbErr, ok := err.(database.Error); ok && dbErr.ErrorCode ==
			database.ErrCorruption {
			panic(dbErr)
		}

		// Convert the error into an appropriate reject message and
		// send it.
		code, reason := mempool.ErrToRejectErr(err)
		peer.PushRejectMsg(wire.CmdBlock, code, reason, blockHash, false)
		return
	}

	// Meta-data about the new block this peer is reporting. We use this
	// below to update this peer's latest block height and the heights of
	// other peers based on their last announced block hash. This allows us
	// to dynamically update the block heights of peers, avoiding stale
	// heights when looking for a new sync peer. Upon acceptance of a block
	// or recognition of an orphan, we also use this information to update
	// the block heights over other peers who's invs may have been ignored
	// if we are actively syncing while the chain is not yet current or
	// who may have lost the lock announcement race.
	var heightUpdate int32
	var blkHashUpdate *chainhash.Hash

	// Request the parents for the orphan block from the peer that sent it.
	if isOrphan {
		// We've just received an orphan block from a peer. In order
		// to update the height of the peer, we try to extract the
		// block height from the scriptSig of the coinbase transaction.
		// Extraction is only attempted if the block's version is
		// high enough (ver 2+).
		header := &bmsg.block.MsgBlock().Header
		if blockchain.ShouldHaveSerializedBlockHeight(header) {
			coinbaseTx := bmsg.block.Transactions()[0]
			cbHeight, err := blockchain.ExtractCoinbaseHeight(coinbaseTx)
			if err != nil {
				log.Warnf("Unable to extract height from "+
					"coinbase tx: %v", err)
			} else {
				log.Debugf("Extracted height of %v from "+
					"orphan block", cbHeight)
				heightUpdate = cbHeight
				blkHashUpdate = blockHash
			}
		}

		orphanRoot := sm.chain.GetOrphanRoot(blockHash)
		locator, err := sm.chain.LatestBlockLocator()
		if err != nil {
			log.Warnf("Failed to get block locator for the "+
				"latest block: %v", err)
		} else {
			peer.PushGetBlocksMsg(locator, orphanRoot)
		}
	} else {
		// Any participating block-download peer (or the sync peer outside
		// of it) counts as progress so the stall handler doesn't mistake a
		// healthy parallel download for a stalled sync peer.
		if sm.ibdMode && sm.blockSync != nil {
			for _, p := range sm.blockSync {
				if p == peer {
					sm.lastProgressTime = time.Now()
					break
				}
			}
		} else if peer == sm.syncPeer {
			sm.lastProgressTime = time.Now()
		}

		// When the block is not an orphan, log information about it and
		// update the chain state.
		sm.progressLogger.LogBlockHeight(bmsg.block, sm.chain)

		// Update this peer's latest block height, for future
		// potential sync node candidacy.
		best := sm.chain.BestSnapshot()
		heightUpdate = best.Height
		blkHashUpdate = &best.Hash

		// Clear the rejected transactions.
		sm.rejectedTxns = make(map[chainhash.Hash]struct{})
	}

	// Update the block height for this peer. But only send a message to
	// the server for updating peer heights if this is an orphan or our
	// chain is "current". This avoids sending a spammy amount of messages
	// if we're syncing the chain from scratch.
	if blkHashUpdate != nil && heightUpdate != 0 {
		peer.UpdateLastBlockHeight(heightUpdate)
		if isOrphan || sm.current() {
			go sm.peerNotifier.UpdatePeerHeights(blkHashUpdate, heightUpdate,
				peer)
		}
	}

	// If we are not in the initial block download mode, it's a good time to
	// periodically flush the blockchain cache because we don't expect new
	// blocks immediately.  While in initial block download mode, also flush
	// periodically so the consistent UTXO state does not fall far behind the
	// chain tip, otherwise an unclean shutdown forces a long reconstruction
	// on the next start.
	if !sm.ibdMode || time.Since(sm.lastUtxoFlush) >= utxoFlushInterval {
		if err := sm.chain.FlushUtxoCache(blockchain.FlushPeriodic); err != nil {
			log.Errorf("Error while flushing the blockchain cache: %v", err)
		}
		sm.lastUtxoFlush = time.Now()
	}
	if !sm.ibdMode {
		return
	}

	// If we're on a checkpointed block, check if we still have checkpoints
	// to let the user know if we're switching to normal mode.
	if isCheckpointBlock {
		log.Infof("Continuing IBD, on checkpoint block %v(%v)",
			bmsg.block.Hash(), bmsg.block.Height())
		nextCheckpoint := sm.findNextHeaderCheckpoint(bmsg.block.Height())
		if nextCheckpoint == nil {
			log.Infof("Reached the final checkpoint -- " +
				"switching to normal mode")
		}
	}

	// Fetch more blocks if we're still not caught up to the best header.
	// blkDownload tops up any participating peer whose in-flight request count
	// has drained below minInFlightBlocks.  This must not be gated on the
	// delivering peer's own request count: the sync peer typically keeps a full
	// in-flight window, so its block arrivals would never let blkDownload run
	// and the remaining peers would be handed a single slice at startSync and
	// then starved, leaving the parallel download running on one peer.
	_, lastHeight := sm.chain.BestHeader()
	if bmsg.block.Height() < lastHeight {
		// Top up the delivering peer and any other drained peer in the
		// parallel block download.
		sm.blkDownload()
		return
	}

	if bmsg.block.Height() >= lastHeight {
		log.Infof("Finished the initial block download and "+
			"caught up to block %v(%v) -- now listening to blocks.",
			bmsg.block.Hash(), bmsg.block.Height())
		sm.ibdMode = false
	}
}

// fetchHeaderBlocks creates and sends a request to the given peer for the next
// list of blocks to be downloaded based on the current list of headers.
func (sm *SyncManager) fetchHeaderBlocks(peer *peerpkg.Peer) {
	if peer == nil {
		log.Warnf("fetchHeaderBlocks called with a nil peer")
		return
	}

	gdmsg := sm.buildBlockRequest(peer)
	if len(gdmsg.InvList) > 0 {
		peer.QueueMessage(gdmsg, nil)
	}
}

// assignBlockSlice hands the next unassigned, non-overlapping slice of the
// block chain to the given peer, if it does not already have one.  It prefers
// to fill a hole at the front of the download (left by a dropped or stalled
// peer) and otherwise extends the contiguous frontier.  The slice is capped by
// the bounded request window ahead of the connected best chain and by the next
// already-assigned slice so slices never overlap.  The assignment frontier only
// ever advances; released heights are already in flight or connected and are
// never handed out again.  It returns true if a slice was assigned.
func (sm *SyncManager) assignBlockSlice(peer *peerpkg.Peer) bool {
	bs := sm.blockSyncState
	if bs == nil || peer == nil {
		return false
	}

	// A peer only ever has a single slice in flight.
	if _, ok := bs.peerSlice[peer]; ok {
		return false
	}

	// The slice frontier must not advance past the request window ahead of
	// the connected best chain, and never past the best header.
	bestHeight := sm.chain.BestSnapshot().Height
	windowEnd := bestHeight + maxBlockRequestWindow
	_, bestHeaderHeight := sm.chain.BestHeader()
	if windowEnd > bestHeaderHeight {
		windowEnd = bestHeaderHeight
	}

	// While the header download is still running, keep a safety margin
	// between the block request frontier and the applied header tip so a
	// burst of heavy block processing cannot let blocks catch up to and
	// starve the header download.  The frontier is already capped at the
	// header tip below; this guard additionally refuses to hand out new
	// slices once the margin shrinks below the request window, letting the
	// (much cheaper) header processing catch up first.  Once headers finish
	// (headerSync is torn down) the guard no longer applies and the final
	// catch-up proceeds normally.
	if sm.headerSync != nil && bestHeaderHeight-bestHeight < maxBlockRequestWindow {
		return false
	}

	// Never hand out heights we have already connected.
	if bs.nextAssign <= bestHeight {
		bs.nextAssign = bestHeight + 1
	}

	// Fill a front hole first; otherwise extend the frontier.  A hole
	// exists when the first height after the connected tip has not already
	// been requested by any peer (e.g. a dropped or stalled peer owned it
	// and its request was freed).  If the front height is already in flight
	// the frontier simply extends past it; the front slice may have been
	// released by buildBlockRequest once its heights were all requested
	// while the block messages themselves are still in flight.
	start := bs.nextAssign
	frontHash, err := sm.chain.HeaderHashByHeight(bestHeight + 1)
	frontInFlight := err == nil
	if frontInFlight {
		_, frontInFlight = sm.requestedBlocks[*frontHash]
	}
	if frontInFlight {
		// A request for the front block is in flight again, so clear the
		// missing-front marker; any subsequent gap is measured from scratch.
		bs.frontMissingSince = time.Time{}
		bs.frontMissingHeight = 0
	} else if bestHeight+1 <= windowEnd {
		// The front slice is the critical one: whoever holds it controls
		// how fast the tip advances.  A peer that has just been freed for
		// stalling must not immediately re-claim it, or it would just
		// freeze the download again; prefer a peer that has recently shown
		// it can deliver.
		//
		// However, if the front block has been unrequested for a full stall
		// window (its request was freed and no peer has claimed it since),
		// the "recently delivering" policy has no candidate and the download
		// deadlocks permanently: blkDownload only runs on block receipt, so
		// with no slice ever assigned no block ever arrives and nothing ever
		// re-requests the front.  In that case let any peer claim the front
		// so the request goes out again and the stall machinery can observe
		// (and if needed re-issue) it.
		if bs.frontMissingHeight != bestHeight+1 {
			bs.frontMissingHeight = bestHeight + 1
			bs.frontMissingSince = time.Now()
		}
		frontStuck := time.Since(bs.frontMissingSince) >= blockSliceStallTimeout
		if last, ok := bs.lastProgress[peer]; !ok ||
			time.Since(last) < blockSliceStallTimeout || frontStuck {
			start = bestHeight + 1
			if frontStuck {
				// Reset the peer's progress clock so a subsequent stall is
				// measured from this claim, not from an old delivery.
				bs.lastProgress[peer] = time.Now()
				log.Warnf("Block download dispatch: front block %d unrequested "+
					"for %v -- handing front slice to peer %s (stall fallback)",
					bestHeight+1, blockSliceStallTimeout, peer.Addr())
			}
		} else {
			return false
		}
	}
	if start > windowEnd {
		return false
	}

	// Slices are half-open [start, end): end is the first height NOT
	// included.  The request window is capped at windowEnd (an inclusive
	// highest height), so a slice truncated by the window must end one past
	// it to still cover that height.
	end := start + bs.sliceLen
	if end > windowEnd+1 {
		end = windowEnd + 1
	}
	// Never overlap a slice already handed out beyond the start.
	if sl, ok := bs.nextSliceBeyond(start); ok {
		if nextStart := sl.start; nextStart < end {
			end = nextStart
		}
	}
	if end <= start {
		return false
	}

	sl := &blockSlice{
		start:      start,
		end:        end,
		peer:       peer,
		assignedAt: time.Now(),
		received:   start - 1,
	}
	bs.slices[start] = sl
	bs.peerSlice[peer] = sl

	// Only advance the contiguous frontier for fresh (non hole-filling)
	// assignments.
	if start == bs.nextAssign {
		bs.nextAssign = end
	}
	return true
}

// nextSliceBeyond returns the already-assigned block slice with the smallest
// start height greater than the passed start, if any.
func (bs *blockSyncState) nextSliceBeyond(start int32) (*blockSlice, bool) {
	var best *blockSlice
	for s, sl := range bs.slices {
		if s <= start {
			continue
		}
		if best == nil || s < best.start {
			best = sl
		}
	}
	return best, best != nil
}

// releaseBlockSlice frees a peer's slice so the next top-up can hand it a fresh
// slice.  The assignment frontier is deliberately not modified: released
// heights are either already connected or still in flight to the peer, so
// handing them out again would just produce an empty request for the next peer
// (every height would be skipped as already requested) and starve it.
func (sm *SyncManager) releaseBlockSlice(peer *peerpkg.Peer) {
	bs := sm.blockSyncState
	if bs == nil || peer == nil {
		return
	}
	if sl, ok := bs.peerSlice[peer]; ok {
		delete(bs.slices, sl.start)
		delete(bs.peerSlice, peer)
	}
}

// buildBlockRequest builds a getdata message for blocks that need to be
// downloaded based on the current list of headers.
//
// Start fetching from the fork point between the best chain and
// the best header chain rather than from the best chain height.
// When the best header chain has diverged (e.g. due to a reorg),
// blocks between the fork point and the current height on the new
// chain are different and must also be downloaded.
//
// In a parallel download each peer owns a disjoint slice of the chain (see
// assignBlockSlice); this method only walks the heights inside that slice.  A
// slice is released once every height in it has been requested (in flight or
// already present), so the peer claims the next slice on the next top-up.
func (sm *SyncManager) buildBlockRequest(peer *peerpkg.Peer) *wire.MsgGetData {
	// Return early if the peer is nil.
	if peer == nil {
		return wire.NewMsgGetDataSizeHint(0)
	}

	_, bestHeaderHeight := sm.chain.BestHeader()
	forkHeight := sm.chain.BestChainHeaderForkHeight()
	if bestHeaderHeight < forkHeight {
		// Should never happen but we're guarding against the uint cast
		// that happens below.
		return wire.NewMsgGetDataSizeHint(0)
	}

	// Only request blocks within a bounded window ahead of the connected
	// best chain.  Requesting the whole remaining header chain at once
	// floods the orphan pool with out-of-order blocks and stalls the
	// download, so the request horizon advances as blocks connect.
	bestHeight := sm.chain.BestSnapshot().Height
	requestEnd := bestHeight + maxBlockRequestWindow
	if requestEnd > bestHeaderHeight {
		requestEnd = bestHeaderHeight
	}

	length := requestEnd - forkHeight
	gdmsg := wire.NewMsgGetDataSizeHint(uint(length))
	numRequested := 0

	// Determine the request start.  In a parallel download the peer only
	// fetches within its assigned slice; otherwise fall back to the
	// persisted block-download cursor.
	startHeight := forkHeight + 1
	inSlice := false
	if bs := sm.blockSyncState; bs != nil {
		sl := bs.peerSlice[peer]
		if sl == nil {
			if !sm.assignBlockSlice(peer) {
				return gdmsg
			}
			sl = bs.peerSlice[peer]
		}
		startHeight = sl.start
		// Slices are half-open [start, end) so the loop below (which is
		// inclusive of requestEnd) must stop one before the slice end.
		if sl.end-1 < requestEnd {
			requestEnd = sl.end - 1
		}
		inSlice = true
	} else {
		cursorHash, cursorHeight := sm.chain.BestDownloadState()
		if cursorHeight > forkHeight && cursorHeight <= bestHeaderHeight {
			if headerHash, err := sm.chain.HeaderHashByHeight(cursorHeight); err == nil &&
				*headerHash == cursorHash {
				startHeight = cursorHeight + 1
			}
		}
	}

	completed := true
	for h := startHeight; h <= requestEnd; h++ {
		hash, err := sm.chain.HeaderHashByHeight(h)
		if err != nil {
			log.Warnf("error while fetching the block hash for height %v -- %v",
				h, err)
			completed = false
			break
		}

		// Request full witness data for blocks when the peer supports it.
		// Sugarchain activates SegWit at genesis (chaincfg DeploymentSegwit
		// height 0), so the chain contains P2WPKH/P2WSH outputs whose inputs
		// MUST carry witness data.  A plain InvTypeBlock response omits the
		// witness field, and the block then fails script validation
		// ("witness program must have clean stack"), which the resume path
		// misreads as a fabricated chain and rolls back forever.  The
		// parallel download must mirror handleInvMsg, which already switches
		// to InvTypeWitnessBlock for witness-enabled peers.
		invType := wire.InvTypeBlock
		if peer.IsWitnessEnabled() {
			invType = wire.InvTypeWitnessBlock
		}
		iv := wire.NewInvVect(invType, hash)
		haveInv, err := sm.haveInventory(iv)
		if err != nil {
			log.Warnf("Unexpected failure when checking for "+
				"existing inventory during header block "+
				"fetch: %v", err)
		}
		if !haveInv {
			// Skip blocks that are already in-flight to avoid
			// sending duplicate getdata requests. Duplicates
			// cause the peer to send the block twice; the second
			// copy arrives after the first has been processed and
			// removed from requestedBlocks, triggering an
			// "unrequested block" disconnect.
			if _, exists := sm.requestedBlocks[*hash]; exists {
				continue
			}

			peerState := sm.peerStates[peer]

			sm.requestedBlocks[*hash] = struct{}{}
			peerState.requestedBlocks[*hash] = struct{}{}

			gdmsg.AddInvVect(iv)
			numRequested++
		}

		if numRequested >= blockInFlightTarget && h < requestEnd {
			completed = false
			break
		}
	}

	// Once every height in the slice has been requested, release it so the
	// next top-up hands this peer the next slice.  In-flight blocks keep
	// draining in the background while a fresh slice is requested.
	if inSlice && completed {
		sm.releaseBlockSlice(peer)
	}
	return gdmsg
}

// reconnectStoredBlocks connects blocks that were already downloaded and stored
// in the database but never connected to the best chain (e.g. a restart
// interrupted the download) through the connection logic.  Without this, the
// downloader would treat those blocks as already present -- both skipping them
// here and never requesting them again -- and the sync would stall even though
// the data is local.  Blocks are connected in height order from the current
// tip, so each block's parent is the previously connected block.
func (sm *SyncManager) reconnectStoredBlocks() {
	_, bestHeaderHeight := sm.chain.BestHeader()
	for {
		nextHeight := sm.chain.BestSnapshot().Height + 1
		if nextHeight > bestHeaderHeight {
			return
		}

		hash, err := sm.chain.HeaderHashByHeight(nextHeight)
		if err != nil {
			return
		}

		// Only attempt to reconnect blocks whose data is actually on disk.
		// HaveBlock would also report in-memory orphan blocks as present, but
		// an orphan's payload is never written to the DB, so ResumeBlockConnect
		// would fail with "block ... does not exist" and a polluted height row
		// pointing at a recently-fetched orphan would falsely trigger a
		// "local data inconsistent" rollback (P11).  Orphans connect through
		// the normal download flow once their parent arrives.
		stored, err := sm.chain.BlockStored(hash)
		if err != nil || !stored {
			return
		}

		_, behaviorFlags := sm.checkHeadersList(hash)
		if _, err := sm.chain.ResumeBlockConnect(hash, behaviorFlags); err != nil {
			// A rule error means the stored block's DATA is invalid, not
			// the header chain.  The classic case: blocks downloaded with
			// plain inventory (InvTypeBlock) in a SegWit-activated chain
			// lack the witness data their P2WPKH/P2WSH inputs require, so
			// they can never be connected -- the header is valid, only the
			// payload is incomplete.  Deleting the bad payload lets the
			// download re-fetch the block with witness data (buildBlockRequest
			// now requests InvTypeWitnessBlock from witness-enabled peers).
			// Rolling the header chain back instead would mark valid headers
			// invalid and restart the download into the same failure,
			// looping forever (observed: 100+ rollbacks at one height).
			var ruleErr blockchain.RuleError
			if errors.As(err, &ruleErr) {
				// A BIP0030 overwrite error means the stored block's own
				// outputs are already present in the local UTXO set.  This
				// is not a data problem: the header hash at this height is
				// fixed, so re-downloading the block can never clear it.
				// It is stale residue left by a previous session that
				// rolled the header chain back without undoing the UTXO
				// state (see PurgeUtxosAboveHeight).  Repair the state
				// instead of looping on delete-and-redownload forever.
				if ruleErr.ErrorCode == blockchain.ErrOverwriteTx {
					tipHeight := sm.chain.BestSnapshot().Height
					purged, perr := sm.chain.PurgeUtxosAboveHeight(tipHeight)
					if perr != nil {
						log.Warnf("Failed to purge stale UTXOs above "+
							"height %d: %v", tipHeight, perr)
					} else if purged > 0 {
						// The residue that made this block's BIP0030 check
						// fail is gone; retry connecting the same stored
						// block instead of deleting it.
						log.Warnf("Purged %d stale UTXO entries above "+
							"height %d; retrying block %v (height %d)",
							purged, tipHeight, hash, nextHeight)
						if _, retryErr := sm.chain.ResumeBlockConnect(
							hash, behaviorFlags); retryErr == nil {
							continue
						} else {
							log.Warnf("Stored block %v (height %d) still "+
								"failed to connect after UTXO purge: %v",
								hash, nextHeight, retryErr)
							err = retryErr
							if !errors.As(err, &ruleErr) {
								sm.rollbackFabricatedHeaderChain()
								return
							}
						}
					}
					// purged == 0: no residue above the tip, so the
					// overwrite is genuine.  Fall through to the standard
					// delete-and-redownload path below.
				}

				log.Warnf("Stored block %v (height %d) failed to connect "+
					"with a rule error: %v -- deleting incomplete block "+
					"data and re-downloading", hash, nextHeight, err)
				if rerr := sm.chain.RemoveBlockData(hash); rerr != nil {
					log.Warnf("Failed to remove block data for %v: %v",
						hash, rerr)
				}
				return
			}

			// A non-rule error means the on-disk block data is genuinely
			// inconsistent with the header chain (e.g. a previous session
			// downloaded blocks for a fabricated or forked header chain, or
			// the header index itself was polluted with a real block at the
			// wrong height).  Silently returning would leave the download
			// stuck at this height forever: the stored data is still "have"
			// so it is never re-requested, and nothing else re-issues it.
			// Roll the header chain back to the last confirmed connected
			// height so the bogus segment is invalidated and the download
			// restarts from there.
			log.Warnf("Stored block %v (height %d) failed to connect: %v -- "+
				"local data inconsistent with header chain, rolling back",
				hash, nextHeight, err)
			sm.rollbackFabricatedHeaderChain()
			return
		}
	}
}

// handleHeadersMsg handles block header messages from all peers.  Headers are
// requested when performing the ibd and are propagated by peers once the ibd
// is complete.
func (sm *SyncManager) handleHeadersMsg(hmsg *headersMsg) {
	peer := hmsg.peer
	_, exists := sm.peerStates[peer]
	if !exists {
		log.Warnf("Received headers message from unknown peer %s", peer)
		return
	}

	// Nothing to do for an empty headers message.
	msg := hmsg.headers
	numHeaders := len(msg.Headers)

	// During a parallel (multi-peer) header download, route responses to the
	// per-range handler which reorders and applies them in chain order.
	if sm.headerSync != nil {
		sm.handleParallelHeadersMsg(peer, msg.Headers)
		return
	}

	// Nothing to do for an empty headers message.
	if numHeaders == 0 {
		return
	}

	// Mirror the reference umami behavior (sugarchain PR #122): during
	// initial block download the expensive PoW check is skipped when
	// processing headers.  Full PoW validation happens later when the
	// corresponding blocks are downloaded and processed.
	headerFlags := blockchain.BFNoPoWCheck
	if !sm.ibdMode {
		headerFlags = blockchain.BFNone
	}

	for _, blockHeader := range msg.Headers {
		_, err := sm.chain.ProcessBlockHeader(
			blockHeader, headerFlags, false,
		)
		if err != nil {
			log.Warnf("Received block header from peer %v "+
				"failed header verification -- disconnecting: %v",
				peer.Addr(), err)
			peer.Disconnect()
			return
		}

		sm.progressLogger.SetLastLogTime(time.Now())
	}

	bestHash, bestHeight := sm.chain.BestHeader()
	if sm.ibdMode {
		if sm.syncPeer == nil {
			// Return if we've disconnected from the syncPeer.
			return
		}

		// Update the last progress time to prevent the stall handler
		// from disconnecting the sync peer during header download.
		if peer == sm.syncPeer {
			sm.lastProgressTime = time.Now()
		}

		if bestHeight < sm.syncPeer.LastBlock() {
			locator := blockchain.BlockLocator([]*chainhash.Hash{&bestHash})
			sm.syncPeer.PushGetHeadersMsg(locator, &zeroHash)
			return
		}
	}

	bestHeaderHash, bestHeaderHeight := sm.chain.BestHeader()
	log.Infof("downloaded headers to %v(%v) from peer %v "+
		"-- now fetching blocks",
		bestHeaderHash, bestHeaderHeight, hmsg.peer.String())
	sm.fetchHeaderBlocks(peer)
}

// handleParallelHeadersMsg handles a headers message that arrives while the
// initial header download is being performed across several peers.  The
// response is matched to the sending peer's assigned range and, once the front
// of the download becomes contiguous, the headers are applied to the chain in
// order.
func (sm *SyncManager) handleParallelHeadersMsg(peer *peerpkg.Peer,
	headers []*wire.BlockHeader) {

	hs := sm.headerSync
	if hs == nil {
		return
	}

	rng := hs.peerRange[peer]
	if rng == nil {
		// The peer is no longer assigned a range (its range was consumed,
		// reassigned, or the peer is not part of the download).  Ignore
		// stray or duplicate responses.
		return
	}

	// Ignore duplicate responses for a range that has already been served --
	// EXCEPT the front range still waiting on its C2 second vote.  Once the
	// first response arrived (received=true) but the range is not yet
	// confirmed, an independent peer's response is exactly the second vote
	// the front needs; dropping it here would leave the front unconfirmed
	// forever (the C2 double-send in assignHeaderRange stops handing the
	// front out once received, and processReadyHeaderRanges refuses to apply
	// an unconfirmed front), stalling the whole parallel header download.
	if rng.received && !(rng.start == hs.nextHeight && !rng.confirmed) {
		return
	}

	// An empty response means the peer cannot serve the requested slice (its
	// actual tip is below the requested height).  Drop it from the download
	// so the slice can be re-issued to a peer that can serve it.
	if len(headers) == 0 {
		log.Warnf("Peer %v returned no headers for the range starting "+
			"at %d -- removing it from the header download",
			peer.Addr(), rng.start)
		sm.dropHeaderPeer(peer)
		return
	}

	// Verify the response actually extends the exact height this range began
	// at.  A peer can legitimately respond to an earlier request it was
	// issued before its range was reassigned, in which case (with the short
	// stall timeout and aggressive re-issuing) the response is a stale
	// duplicate that must be ignored rather than attributed to the new range.
	//
	// The reference frame for the prev test is the applied header chain.  When
	// the range begins above the applied tip -- which is the norm during the
	// parallel download, where the receive frontier can lead the applied
	// (front-serial) tip by far more than the in-memory header window (up to
	// ~68k headers observed after a rollback) -- HeaderHashByHeight(start-1)
	// falls back to the DB height index, whose reference frame may be stale
	// (P8: rollback does not clear the old height rows) or absent (a legal
	// leading range that has simply not been applied yet).  In that state the
	// response cannot be verified against the applied chain, but it is also
	// NOT a proven misattribution: refusing it would drop legitimate leading
	// ranges (and re-issuing them would re-hit the same condition), while
	// accepting it blindly would let a stale-height row pass (P9).  The range
	// is therefore buffered unverified and applied only when the front
	// reaches start-1, at which point processReadyHeaderRanges re-verifies
	// the connection against the clean applied chain (see R4: misattached
	// ranges are detected there and discarded).
	_, applied := sm.chain.BestHeader()
	if rng.start-1 > applied {
		// The reference frame is not applied yet, so the response cannot be
		// verified against the applied chain here.  Buffer it unverified and
		// let the front apply it later -- but record the first corroborating
		// hash so a misattributed range (a forged/forked segment whose prev
		// chain is self-consistent, e.g. real 1974053's block 4c83da06
		// arriving at 1919363, offset by 54690) is detected when a second
		// independent peer responds: it cannot gather the agreeing second
		// vote.  With fewer than two peers the range degrades to a single
		// vote and C1's fast rollback covers a wrong chain.
		h := headers[0].BlockHash()
		if rng.firstHash == nil {
			rng.firstHash = &h
			rng.firstPeer = peer
			rng.headers = headers
			if len(hs.peers) < 2 {
				rng.confirmed = true
			}
			rng.received = true
			sm.processReadyHeaderRanges()
			return
		}
		if rng.firstPeer == peer {
			return // same peer duplicate
		}
		if h != *rng.firstHash {
			// Second independent peer disagrees: misattributed range.
			log.Warnf("Header range at height %d: peer %v disagrees with %v "+
				"(hash %v vs %v) -- keeping unconfirmed",
				rng.start, peer.Addr(), rng.firstPeer.Addr(), h, *rng.firstHash)
			if sm.frontUnreachable == nil {
				sm.frontUnreachable = make(map[int32]map[string]time.Time)
			}
			peers := sm.frontUnreachable[rng.start]
			if peers == nil {
				peers = make(map[string]time.Time)
				sm.frontUnreachable[rng.start] = peers
			}
			peers[peer.Addr()] = time.Now()
			return
		}
		rng.headers = headers
		rng.received = true
		rng.confirmed = true
		sm.processReadyHeaderRanges()
		return
	}

	// The applied chain resolves start-1 (in-window or via a clean cold
	// read): the response must actually extend that exact height.  A peer
	// can legitimately respond to an earlier request it was issued before
	// its range was reassigned, in which case (with the short stall timeout
	// and aggressive re-issuing) the response is a stale duplicate that must
	// be ignored rather than attributed to the new range.  The check MUST
	// also reject the response when the applied chain's hash at the previous
	// height cannot be resolved (HeaderHashByHeight error).  The
	// prev-connection test is the only thing standing between a well-formed
	// self-consistent chain segment (real PoW, contiguous prev links) and a
	// misattributed one: a peer serving a chain whose heights are offset from
	// the real chain (observed: a whole [6760004..6760006] segment accepted at
	// [6706690..6706692], offset by 53314) passes every other check, so
	// skipping the prev test when the lookup fails lets the offset segment
	// into the header index and pollutes the height→hash mapping.
	expected, err := sm.chain.HeaderHashByHeight(rng.start - 1)
	if err != nil || !headers[0].PrevBlock.IsEqual(expected) {
		log.Warnf("Peer %v returned headers that do not extend the "+
			"range starting at %d (or could not be verified) -- ignoring "+
			"response", peer.Addr(), rng.start)

		// Record that this peer could not extend the front.  When enough
		// distinct peers all fail to extend the same front range, the header
		// chain at that height is almost certainly fabricated or forked; the
		// stall handler rolls the chain back early instead of waiting for the
		// slow 10-minute block-side timer (P4).
		if hs := sm.headerSync; hs != nil && rng.start == hs.nextHeight {
			if sm.frontUnreachable == nil {
				sm.frontUnreachable = make(map[int32]map[string]time.Time)
			}
			peers := sm.frontUnreachable[rng.start]
			if peers == nil {
				peers = make(map[string]time.Time)
				sm.frontUnreachable[rng.start] = peers
			}
			peers[peer.Addr()] = time.Now()
			// Prune stale entries so the map cannot grow without bound.
			for addr, t := range peers {
				if time.Since(t) > 5*time.Minute {
					delete(peers, addr)
				}
			}
		}
		return
	}

	// C2 front double-confirmation: the front range decides the direction of
	// the header chain, so it is corroborated by a second independent peer
	// before being applied.  The first response records its first-header
	// hash; a second independent peer returning the same hash confirms the
	// range.  A forged or misattributed front (fed through a stale height
	// row, P8) cannot gather the second agreeing vote.  With fewer than two
	// peers present the front degrades to a single vote -- C1's fast
	// rollback then covers a wrong chain.  Non-front ranges keep the
	// single-vote fast path (confirmed stays true).
	if rng.start == hs.nextHeight && hs.nextHeight <= hs.target {
		if rng.firstHash == nil {
			// First corroborating response.
			h := headers[0].BlockHash()
			rng.firstHash = &h
			rng.firstPeer = peer
			rng.headers = headers
			if len(hs.peers) < 2 {
				// Sparse network: degrade to a single vote.
				rng.confirmed = true
			}
			rng.received = true
			sm.processReadyHeaderRanges()
			return
		}
		if rng.firstPeer == peer {
			// The same peer responded again; ignore the duplicate.
			return
		}
		h := headers[0].BlockHash()
		if h != *rng.firstHash {
			// Second independent peer disagrees with the first: the front is
			// likely misattributed.  Record the disagreement (C1: enough
			// distinct disagreeing peers triggers a fast rollback) and keep
			// the range unconfirmed.
			log.Warnf("Front header range at height %d: peer %v disagrees "+
				"with %v (hash %v vs %v) -- keeping unconfirmed",
				rng.start, peer.Addr(), rng.firstPeer.Addr(), h, *rng.firstHash)
			if sm.frontUnreachable == nil {
				sm.frontUnreachable = make(map[int32]map[string]time.Time)
			}
			peers := sm.frontUnreachable[rng.start]
			if peers == nil {
				peers = make(map[string]time.Time)
				sm.frontUnreachable[rng.start] = peers
			}
			peers[peer.Addr()] = time.Now()
			return
		}
		rng.headers = headers
		rng.received = true
		rng.confirmed = true
		sm.processReadyHeaderRanges()
		return
	}

	rng.headers = headers
	rng.received = true

	sm.processReadyHeaderRanges()
}

// processReadyHeaderRanges applies every completed header range at the front of
// the download to the header chain, frees the sending peers, and hands them the
// next slice.  Once the download reaches the tallest participating peer, it
// hands off to the initial block download.
func (sm *SyncManager) processReadyHeaderRanges() {
	hs := sm.headerSync
	if hs == nil {
		return
	}

	for {
		front := hs.ranges[hs.nextHeight]
		// A range is applied only once received AND (for the front, whose
		// direction decides the header chain) confirmed by a second
		// independent peer (C2).  Non-front ranges are confirmed by
		// construction, so this only gates the front's double vote.
		if front == nil || !front.received || !front.confirmed {
			break
		}

		// Strict prev-connection check against the applied chain.  The
		// range's first header must extend the exact applied height
		// (front.start-1 is applied by definition here).  A misattributed
		// range -- e.g. real 1974053's block 4c83da06 arriving at 1919363,
		// offset by 54690 -- has a self-consistent prev chain that passes
		// every earlier check, so this final connection test is what catches
		// it: its prev does not equal the applied chain's hash at start-1.
		// Discard the range and re-issue it rather than polluting the index.
		if len(front.headers) > 0 {
			if expected, err := sm.chain.HeaderHashByHeight(front.start - 1); err != nil ||
				!front.headers[0].PrevBlock.IsEqual(expected) {

				log.Warnf("Header range at height %d does not extend the "+
					"applied chain at %d (or could not be verified) -- "+
					"discarding and re-issuing", front.start, front.start-1)
				// Record that this peer could not extend the front, exactly
				// as handleParallelHeadersMsg does, so a front that keeps
				// failing here (e.g. the local best chain has diverged from
				// the peers' real main chain and the applied height maps to
				// the wrong hash) accumulates enough votes to trigger the
				// early rollback in handleStallSample.  Without this the
				// range is discarded and re-issued forever and the header
				// download deadlocks.
				if front.start == hs.nextHeight {
					if sm.frontUnreachable == nil {
						sm.frontUnreachable = make(map[int32]map[string]time.Time)
					}
					peers := sm.frontUnreachable[front.start]
					if peers == nil {
						peers = make(map[string]time.Time)
						sm.frontUnreachable[front.start] = peers
					}
					peers[front.peer.Addr()] = time.Now()
					for addr, t := range peers {
						if time.Since(t) > 5*time.Minute {
							delete(peers, addr)
						}
					}
				}
				delete(hs.ranges, front.start)
				delete(hs.peerRange, front.peer)
				sm.reissueHeaderRange(front.peer, front.start)
				continue
			}
		}

		// Mirror the reference umami behavior (sugarchain PR #122): during
		// initial block download the expensive PoW check is skipped when
		// processing headers.  Full PoW validation happens later when the
		// corresponding blocks are downloaded and processed.
		headerFlags := blockchain.BFNoPoWCheck
		if !sm.ibdMode {
			headerFlags = blockchain.BFNone
		}

		for _, blockHeader := range front.headers {
			_, err := sm.chain.ProcessBlockHeader(
				blockHeader, headerFlags, false)
			if err != nil {
				// A range whose predecessor header has not been
				// applied yet cannot be applied: during the parallel
				// download a re-issue may deliver a later range before
				// its predecessor, and the predecessor's own range may
				// still be in flight (observed: peer returned headers
				// for height 6868747 whose previous block d1d33b74 is
				// the not-yet-applied header at 6868746).  This is a
				// transient ordering race, not a peer fault -- the
				// predecessor will be applied when its own range
				// arrives.  Leave the range in place and let the next
				// processReadyHeaderRanges pass retry it; the re-issue
				// machinery keeps the predecessor moving.  Disconnecting
				// the peer and aborting the whole header download here
				// would restart from scratch and hit the same race
				// forever (observed: repeated connect/disconnect loop).
				var ruleErr blockchain.RuleError
				if errors.As(err, &ruleErr) &&
					ruleErr.ErrorCode == blockchain.ErrPreviousBlockUnknown {
					log.Warnf("Header range at height %d cannot be "+
						"applied yet (previous block not known): %v -- "+
						"waiting for predecessor", front.start, err)
					return
				}

				// Any other rule violation (bad difficulty, invalid
				// header, etc.) means this range does not fit the
				// applied chain.  Do NOT blame the responding peer:
				// when the local header index was polluted (P8 stale
				// height rows feeding misattributed ranges) the peer
				// carrying the real chain is the one whose response
				// fails, and disconnecting it accelerates peer loss
				// (P3).  Record the range's first hash as suspicious,
				// roll the header chain back so the polluted segment
				// is discarded, and let the fresh download proceed --
				// the peer stays connected.
				if errors.As(err, &ruleErr) {
					if hash, e2 := sm.chain.HeaderHashByHeight(front.start); e2 == nil {
						if sm.suspiciousHeaders == nil {
							sm.suspiciousHeaders = make(map[chainhash.Hash]struct{})
						}
						if len(sm.suspiciousHeaders) < 100 {
							sm.suspiciousHeaders[*hash] = struct{}{}
						}
					}
					log.Warnf("Header range at height %d failed to "+
						"apply: %v -- rolling back header chain "+
						"(peer %v stays connected)", front.start, err,
						front.peer.Addr())
					sm.rollbackFabricatedHeaderChain()
					return
				}

				// A non-rule internal error is a real fault: drop the
				// peer and restart the header download.
				log.Warnf("Received block header from peer %v "+
					"failed header verification -- disconnecting: %v",
					front.peer.Addr(), err)
				front.peer.Disconnect()
				sm.abortHeaderSync()
				return
			}
			front.applied++
			sm.progressLogger.SetLastLogTime(time.Now())
		}

		sm.recordHeaderWindow(front.start, front.start+int32(len(front.headers)), front.peer)

		delete(hs.ranges, front.start)
		delete(hs.peerRange, front.peer)
		hs.nextHeight = front.start + int32(len(front.headers))
		sm.lastProgressTime = time.Now()

		// The peer whose slice we consumed picks up the next slice.
		sm.assignHeaderRange(front.peer)
	}

	// Top up any idle peers so the download continues in parallel.
	for _, p := range hs.peers {
		sm.assignHeaderRange(p)
	}

	// Guard against a hole or stalled slice at the front of the download.
	sm.reissueFrontRange()

	// Start the block download early, while headers are still being
	// downloaded, once the applied header tip has built up enough of a lead
	// (see maybeStartBlockSync).  The block request frontier stays bounded by
	// the header tip, so the two downloads proceed concurrently and safely.
	sm.maybeStartBlockSync()

	// Hand off to the block download once every header through the tallest
	// participating peer has been applied.
	if hs.nextHeight > hs.target {
		sm.finishHeaderSync()
	}
}

// recordHeaderWindow logs one completed parallel header download window so the
// most recent ones stay visible after the header download itself has finished.
func (sm *SyncManager) recordHeaderWindow(start, end int32, peer *peerpkg.Peer) {
	if peer == nil {
		return
	}
	sm.headerRecent = append(sm.headerRecent, HeaderRecentRange{
		Start:      start,
		End:        end,
		Peer:       peer.Addr(),
		AssignedAt: time.Now(),
	})
	const maxRecentHeaderWindows = 16
	if len(sm.headerRecent) > maxRecentHeaderWindows {
		sm.headerRecent = sm.headerRecent[len(sm.headerRecent)-maxRecentHeaderWindows:]
	}
}

// finishHeaderSync signals that the parallel header download has caught up to
// the connected peers' tips.  If a parallel block download has not already been
// started (see maybeStartBlockSync), it is started now from the peers that
// served headers; otherwise the already-running block download is left in place
// so its in-flight slices are not lost.
func (sm *SyncManager) finishHeaderSync() {
	hs := sm.headerSync
	if hs == nil {
		return
	}
	sm.headerSliceLen = hs.sliceLen
	sm.headerSync = nil

	bestHash, bestHeight := sm.chain.BestHeader()
	log.Infof("downloaded headers to %v(%v) in %d parallel "+
		"slices -- now fetching blocks", bestHash, bestHeight, len(hs.peers))

	// A block download may already be running if it was started while the
	// header download was still in progress.  Keep it: tearing it down would
	// lose every in-flight slice and the remaining peers' progress.
	if sm.blockSyncState != nil {
		log.Infof("Block download already running with %d peers; "+
			"leaving it in place", len(sm.blockSync))
		return
	}

	// Hand the block download off to all of the peers that served headers so
	// the initial block download is also performed in parallel.  If we end up
	// with no peers the normal new-peer/stall handling will restart the sync.
	sm.startParallelBlockDownload(hs.peers, bestHeight)
}

// startParallelBlockDownload sets up the parallel (multi-peer) initial block
// download state and dispatches an initial request to every participating peer.
// It is shared by finishHeaderSync (the header download is complete) and
// maybeStartBlockSync (the header download is still running but has built up
// enough of a lead).  The block request frontier is always capped by the
// current best header height (see assignBlockSlice/buildBlockRequest), so the
// block download can never request heights whose headers have not been applied
// yet, no matter when it starts.
func (sm *SyncManager) startParallelBlockDownload(peers []*peerpkg.Peer, bestHeaderHeight int32) {
	sm.ibdMode = true
	sm.blockSync = peers
	if len(sm.blockSync) > maxHeaderSyncPeers {
		sm.blockSync = sm.blockSync[:maxHeaderSyncPeers]
	}
	if len(sm.blockSync) == 0 {
		log.Infof("No peers remaining for parallel block download")
		return
	}

	// Fold in any already-connected sync candidates that did not serve the
	// final header round.  A header catch-up on top of a running download can
	// restart this function with only one or two header peers; without this the
	// remaining connected peers would never rejoin the block download (the set
	// only grows from newPeer) and parallelism would collapse to that couple of
	// peers until fresh connections arrived.
	sm.foldExistingSyncPeers()

	// Carve the download into disjoint per-peer slices.  The whole download
	// is capped at maxBlockRequestWindow heights ahead of the tip (the same
	// bound the single-peer path used) so blocks stay close enough to connect
	// before the orphan pool fills; slicing that window across the peers lets
	// each peer stream a different part of it in parallel.
	//
	// The slice length is fixed by the maximum number of participating peers
	// rather than the current count.  If it were derived from the live peer
	// count, a set that temporarily shrinks to a single peer would hand that
	// one peer the entire request window; every peer that then connects
	// would find the window already claimed and never obtain its own slice,
	// leaving the "parallel" download permanently single-peer until the next
	// header sync restarts it.  A fixed slice keeps the window sub-divided
	// into maxHeaderSyncPeers pieces at all times, so peers that join midway
	// always claim the slice left open ahead of the current frontier.
	bestHeight := sm.chain.BestSnapshot().Height
	sliceLen := int32(maxBlockRequestWindow) / maxHeaderSyncPeers
	if sliceLen < 1 {
		sliceLen = 1
	}
	sm.blockSyncState = &blockSyncState{
		nextAssign:   bestHeight + 1,
		target:       bestHeaderHeight,
		slices:       make(map[int32]*blockSlice),
		peerSlice:    make(map[*peerpkg.Peer]*blockSlice),
		sliceLen:     sliceLen,
		lastProgress: make(map[*peerpkg.Peer]time.Time),
	}

	// Use the first participating peer as the sync peer so stall handling and
	// progress tracking keep working, then dispatch an initial request to every
	// participating peer at once.
	sm.syncPeer = sm.blockSync[0]
	sm.lastProgressTime = time.Now()
	for _, p := range sm.blockSync {
		sm.fetchHeaderBlocks(p)
	}
	log.Infof("Starting parallel block download from %d peers "+
		"(slice window %d heights/peer)", len(sm.blockSync), sliceLen)

	// Connect any blocks that were downloaded and stored by a previous
	// session before dispatching fresh requests, so the download resumes from
	// where it left off instead of re-fetching or stalling on local data.
	sm.reconnectStoredBlocks()
}

// maybeStartBlockSync starts the parallel block download while the header
// download is still running, once the applied header tip leads the connected
// best chain by blockSyncStartLead heights.  It is called as header batches are
// applied (see processReadyHeaderRanges).  Because the block request frontier
// is bounded by the current best header height, starting early cannot request
// blocks whose headers are not applied yet; the header download keeps running
// and simply stays ahead.  It is a no-op when the overlap is disabled
// (blockSyncStartLead <= 0), when no header download is running, or when the
// block download has already started.
func (sm *SyncManager) maybeStartBlockSync() {
	if sm.blockSyncStartLead <= 0 {
		return
	}
	hs := sm.headerSync
	if hs == nil || sm.blockSyncState != nil {
		return
	}

	_, bestHeaderHeight := sm.chain.BestHeader()
	bestHeight := sm.chain.BestSnapshot().Height
	if bestHeaderHeight-bestHeight < sm.blockSyncStartLead {
		return
	}

	log.Infof("Header tip leads connected chain by %d (>= %d) -- "+
		"starting parallel block download while headers continue",
		bestHeaderHeight-bestHeight, sm.blockSyncStartLead)
	sm.startParallelBlockDownload(hs.peers, bestHeaderHeight)
}

// dropHeaderPeer removes a peer from an in-progress parallel header download,
// freeing any range it owned so the slice can be re-issued to another peer.  If
// no peers remain, the header download is aborted so it can be restarted when a
// suitable peer arrives.
func (sm *SyncManager) dropHeaderPeer(peer *peerpkg.Peer) {
	hs := sm.headerSync
	if hs == nil {
		return
	}

	if rng := hs.peerRange[peer]; rng != nil {
		delete(hs.ranges, rng.start)
		delete(hs.peerRange, peer)
	}

	for i, p := range hs.peers {
		if p == peer {
			hs.peers = append(hs.peers[:i], hs.peers[i+1:]...)
			break
		}
	}

	// The download only needs to reach the tallest remaining peer.
	target := int32(0)
	for _, p := range hs.peers {
		if lastBlock := p.LastBlock(); lastBlock > target {
			target = lastBlock
		}
	}
	hs.target = target

	// If the peer was also taking part in a parallel block download (which
	// can happen while the header and block downloads overlap), free its
	// block slice and drop it from the participating set so its heights are
	// re-issued to a remaining peer.
	if sm.blockSync != nil {
		sm.releaseBlockSlice(peer)
		for i, p := range sm.blockSync {
			if p == peer {
				sm.blockSync = append(sm.blockSync[:i], sm.blockSync[i+1:]...)
				break
			}
		}
	}

	// If nobody is left to serve headers, tear the download down and let the
	// normal flow restart it (e.g. when a new peer connects).
	if len(hs.peers) == 0 {
		sm.headerSync = nil
		return
	}

	// Hand the freed slot to a remaining peer and make sure the front of the
	// download is covered even if every remaining peer is busy.
	for _, p := range hs.peers {
		sm.assignHeaderRange(p)
	}
	sm.reissueFrontRange()
}

// abortHeaderSync tears down the parallel header download (the offending peer
// has already been disconnected) and restarts a fresh header download from the
// current best header height using the remaining peers.
func (sm *SyncManager) abortHeaderSync() {
	if sm.headerSync == nil {
		return
	}
	sm.headerSync = nil
	sm.ibdMode = true

	_, bestHeaderHeight := sm.chain.BestHeader()
	if len(sm.fetchHigherPeers(bestHeaderHeight)) > 0 {
		sm.fetchHeaders()
	}
}

// rollbackFabricatedHeaderChain rolls the header chain back to the last
// connected block when the block download has been stuck at one height for
// blockUnavailableTimeout.  A real chain's blocks are served by the network,
// so an unservable front block means the header chain is fabricated or forked
// (observed: a header index polluted with a real block at the wrong height
// froze the block download while headers kept advancing).  The bogus segment
// is invalidated, all in-flight header/block state is discarded, and the sync
// restarts from the confirmed height.
func (sm *SyncManager) rollbackFabricatedHeaderChain() {
	// 1. Determine the rollback height.  The best chain tip itself can be the
	// bogus block (a locally-mined block persisted as best-chain tip that no
	// peer's main chain contains): rolling back to best.Height would keep that
	// block as the confirmed root and the header front would fail to extend it
	// again.  Roll back one block below the tip so the offending tip (and any
	// headers above it) is invalidated and the download resumes from the last
	// block the network actually shares.  When the fork runs deeper than one
	// block, the repeated rollbacks at the same height deepen the cut by one
	// more block each time (fabricatedRollbackDepth) until the front can
	// extend again.
	// 1. 确定回滚高度。best chain tip 本身可能就是伪造块(本地挖出并持久化成
	// best-chain tip、但对等点主链上没有的块):回滚到 best.Height 会把这个块
	// 保留为确认根,header front 又会因为无法扩展它而再次失败。回滚到 tip 之
	// 下一块,让有问题的 tip(及其上的 header)失效,从网络真正共享的最后一个
	// 块继续下载。当分叉深于一块时,同一高度的重复回滚会每次加深一刀
	// (fabricatedRollbackDepth),直到 front 能再次扩展。
	best := sm.chain.BestSnapshot()
	// The rollback target without any accumulated depth: one block below the
	// tip, so the offending tip itself is invalidated.  The progress check
	// below uses this un-cut target, so a tip that advanced past the last
	// rollback point resets the deepening even when depth > 0, instead of
	// requiring the tip to first advance `depth` extra blocks.
	// 不含累积深度的回滚目标:tip 下一块,让有问题的 tip 本身失效。下面的
	// 进展判定用这个未加深的 target,因此只要 tip 越过上次回滚点就重置
	// 加深——即使 depth > 0 也立即重置,而不是要求 tip 先额外前进 depth 块。
	target := best.Height - 1
	if target < 0 {
		target = 0
	}
	rollbackHeight := target - sm.fabricatedRollbackDepth
	if rollbackHeight < 0 {
		rollbackHeight = 0
	}
	// The tip advanced past the last rollback point: the previous rollback
	// worked and the download made progress, so any deepening accumulated
	// during the stall no longer applies.  Reset it or the next stall would
	// cut far deeper than needed.
	// tip 已前进超过上次回滚点:上次回滚生效、下载取得进展,卡死期间累积的
	// 加深不再适用。重置它,否则下次卡死会切得过深。
	if target > sm.fabricatedRollbackHeight {
		sm.fabricatedRollbackDepth = 0
		sm.fabricatedRollbackCount = 0
		rollbackHeight = target
	} else {
		// No progress since the last rollback: the same (or a deeper)
		// height is being targeted again.  Deepen the cut by one block so a
		// locally-mined tip that no peer shares (or a deeper fork) is
		// eventually cut through, but refuse after maxFabricatedRollbacks
		// consecutive attempts so a pathological cut that can never reach a
		// shared height cannot chew the index forever.  The counter is NOT
		// reset here: the refusal must be able to fire.
		// 距上次回滚无进展:再次命中同一(或更深)高度。把切口加深一块,让
		// 本地挖出、无对等点共享的 tip(或更深的分叉)最终被切穿,但连续
		// maxFabricatedRollbacks 次无进展后拒绝,防止永远切不到共享高度的
		// 病态切口永久啃咬索引。此处**不**重置计数:熔断必须能触发。
		sm.fabricatedRollbackCount++
		if sm.fabricatedRollbackCount > maxFabricatedRollbacks {
			if time.Since(sm.lastRollbackRefusalAt) >=
				rollbackRefusalLogInterval {
				sm.lastRollbackRefusalAt = time.Now()
				log.Errorf("Refusing to roll back fabricated header "+
					"chain to height %d again (%d rollbacks without "+
					"progress) -- the rollback is not solving the "+
					"problem; suspect the block-side watchdog or the "+
					"served chain itself, and investigate manually",
					target, sm.fabricatedRollbackCount)
			}
			// Leave the chain state untouched and let the stall
			// handler keep the node operational; an operator can
			// intervene instead of the node silently chewing its
			// index.
			// 保持链状态不变,由卡死处理器维持节点运行;由操作员
			// 介入,而不是节点默默啃咬索引。
			return
		}
		sm.fabricatedRollbackDepth++
		rollbackHeight = target - sm.fabricatedRollbackDepth
		if rollbackHeight < 0 {
			rollbackHeight = 0
		}
		log.Warnf("Deepening fabricated-header rollback to height %d "+
			"(depth %d)", rollbackHeight, sm.fabricatedRollbackDepth)
	}
	sm.fabricatedRollbackHeight = rollbackHeight

	// 2. Invalidate every header above the rollback height and rebuild the
	// best-header view from the confirmed block.
	if err := sm.chain.InvalidateHeaderChain(rollbackHeight); err != nil {
		log.Errorf("Failed to roll back fabricated header chain: %v", err)
		return
	}
	log.Warnf("Rolled back fabricated header chain to height %d -- "+
		"restarting header/block download", rollbackHeight)

	// 3-4. Tear down in-flight download state and restart from the rebuilt
	// best-header tip.
	sm.resetDownloadState()
}

// resetDownloadState tears down all in-flight download state and restarts the
// sync from the current best-header tip.  It is shared by the explicit
// fork-point rollback and the generic fabricated-header-chain rollback.
func (sm *SyncManager) resetDownloadState() {
	sm.headerSync = nil
	sm.blockSync = nil
	sm.blockSyncState = nil
	sm.blockMissingSince = time.Time{}
	sm.blockMissingHeight = 0
	sm.requestedBlocks = make(map[chainhash.Hash]struct{})
	for _, state := range sm.peerStates {
		state.requestedBlocks = make(map[chainhash.Hash]struct{})
	}
	sm.syncPeer = nil
	sm.ibdMode = true
	sm.startSync()
}

// rollbackToForkPoint rolls the header chain back to an explicit fork height in
// one step, mirroring umami's ActivateBestChain: the best-chain tip is known to
// diverge from the (network-projected) header chain at forkHeight, so there is
// no need to deepen the cut one block at a time through repeated stall timeouts.
// It reuses InvalidateHeaderChain + the shared download-state reset, but jumps
// straight to the fork instead of walking down from the tip.
func (sm *SyncManager) rollbackToForkPoint(forkHeight int32) {
	if err := sm.chain.InvalidateHeaderChain(forkHeight); err != nil {
		log.Errorf("Failed to roll back header chain to fork height %d: %v",
			forkHeight, err)
		return
	}
	log.Warnf("Rolled back header chain to fork point %d -- "+
		"restarting header/block download", forkHeight)
	sm.resetDownloadState()
}

// reissueStaleHeaderRanges reassigns any in-flight header range that has not
// completed within headerRangeStallTimeout to a different idle peer so a slow
// or unresponsive peer cannot stall the parallel header download.
func (sm *SyncManager) reissueStaleHeaderRanges() {
	hs := sm.headerSync
	if hs == nil {
		return
	}

	// Always try to keep the front of the download moving first.
	sm.reissueFrontRange()

	now := time.Now()
	for start, rng := range hs.ranges {
		if rng.received || now.Sub(rng.assignedAt) < headerRangeStallTimeout {
			continue
		}

		for _, p := range hs.peers {
			if p == rng.peer {
				continue
			}
			if _, ok := hs.peerRange[p]; ok {
				continue
			}

			log.Warnf("Re-issuing header range at height %d from peer %v "+
				"to peer %v after it stalled", start, rng.peer.Addr(),
				p.Addr())
			delete(hs.ranges, start)
			sm.reissueHeaderRange(p, start)
			break
		}
	}
}

// reissueStaleBlockSlices reassigns any in-flight block slice that has not
// made progress within blockSliceStallTimeout to a different idle peer so a
// slow or unresponsive peer cannot stall the parallel block download.  The
// slice's heights are first freed from the global request pool so the taking
// over peer actually re-requests them instead of skipping them as already
// in flight.
func (sm *SyncManager) reissueStaleBlockSlices() {
	bs := sm.blockSyncState
	if bs == nil {
		return
	}

	now := time.Now()
	for start, sl := range bs.slices {
		// Only re-issue a slice whose holder has *stopped delivering* blocks.
		// Judging by assignment time would misread a large-but-progressing
		// slice (e.g. a 1300+ height slice) as stalled the moment 30s passed
		// even though blocks are still flowing, re-rolling every slice to
		// another peer every 30s and wasting bandwidth on re-downloads.  The
		// progress clock is the last deliverance instead; a peer that keeps
		// delivering keeps its slice until the heights all connect.
		if last, ok := bs.lastProgress[sl.peer]; ok {
			if now.Sub(last) < blockSliceStallTimeout {
				continue
			}
		} else if now.Sub(sl.assignedAt) < blockSliceStallTimeout {
			continue
		}

		// The slice may be entirely connected already; only re-issue heights
		// that are still pending, and prefer an idle peer to take over the
		// slice.
		if _, ok := bs.peerSlice[sl.peer]; !ok {
			continue
		}
		var target *peerpkg.Peer
		for _, p := range sm.blockSync {
			if p == sl.peer {
				continue
			}
			if _, ok := bs.peerSlice[p]; ok {
				continue
			}
			target = p
			break
		}
		if target == nil {
			continue
		}

		// Free the stale slice's hashes from the global request pool so
		// the taking over peer can re-request them.  The old peer's own
		// requestedBlocks is left untouched: if its late response
		// arrives, handleBlockMsg still sees it as requested and simply
		// processes the duplicate (which the chain already has).
		for h := sl.start; h < sl.end; h++ {
			hash, err := sm.chain.HeaderHashByHeight(h)
			if err != nil {
				continue
			}
			delete(sm.requestedBlocks, *hash)
		}

		log.Warnf("Re-issuing block slice [%d,%d] from peer %v to peer %v "+
			"after it stalled", sl.start, sl.end-1, sl.peer.Addr(),
			target.Addr())
		delete(bs.slices, start)
		delete(bs.peerSlice, sl.peer)
		sm.assignBlockSlice(target)
		break
	}
}

// reissueStalledBlockPeers frees a participating block-download peer that has
// not delivered a single block within blockSliceStallTimeout.  Such a peer has
// a slice whose heights were all requested (so the slice was released), but the
// in-flight blocks never arrive, so its requestedBlocks stays full and
// blkDownload never tops it up -- leaving the peer idle forever.  Its in-flight
// set is cleared and a fresh slice is assigned so the download does not lose a
// worker.
func (sm *SyncManager) reissueStalledBlockPeers() {
	bs := sm.blockSyncState
	if bs == nil {
		return
	}
	if bs.lastProgress == nil {
		bs.lastProgress = make(map[*peerpkg.Peer]time.Time)
	}

	now := time.Now()
	for _, p := range sm.blockSync {
		state := sm.peerStates[p]
		if state == nil || len(state.requestedBlocks) == 0 {
			continue
		}
		last, ok := bs.lastProgress[p]
		if !ok {
			// Give a freshly-added peer one grace period to start
			// delivering before it can be cleared.
			bs.lastProgress[p] = now
			continue
		}
		if now.Sub(last) < blockSliceStallTimeout {
			continue
		}

		log.Warnf("Freeing %d in-flight blocks from peer %v after it "+
			"stalled in the parallel block download", len(state.requestedBlocks),
			p.Addr())
		sm.clearRequestedState(state)
		sm.releaseBlockSlice(p)
		sm.fetchHeaderBlocks(p)
		bs.lastProgress[p] = now
	}
}

// reissueFrontRange makes sure the front of the parallel header download is
// covered by an in-flight request.  When the front slice is missing (left by a
// dropped peer) or has been in flight for longer than headerRangeStallTimeout,
// it is handed to another peer.  An idle peer is preferred; if every peer is
// busy with a back slice, the slice from the peer holding the range with the
// greatest start is re-issued instead (its own range is re-fetched later as the
// download advances).
func (sm *SyncManager) reissueFrontRange() {
	hs := sm.headerSync
	if hs == nil || hs.nextHeight > hs.target {
		return
	}

	front := hs.ranges[hs.nextHeight]
	switch {
	case front == nil:
		// The front is a hole; hand it out again.
	case front.received:
		// Already in hand.  It is normally applied momentarily by
		// processReadyHeaderRanges; but if its first header's prev is not
		// connected to the applied chain, it is a misattached range that can
		// never be applied (P10: a stale height row fed by a polluted index
		// let a range pass the receive-side prev check even though its prev
		// belongs to the discarded chain).  Leaving it queued would freeze
		// the header front forever, so detect it here and re-issue it.
		if !sm.headerRangeAppliable(front) {
			log.Warnf("Header range at height %d received but not "+
				"appliable (predecessor not connected) -- re-issuing",
				front.start)
			delete(hs.ranges, front.start)
			delete(hs.peerRange, front.peer)
			sm.reissueHeaderRange(front.peer, front.start)
			return
		}
		return
	case time.Since(front.assignedAt) < headerRangeStallTimeout:
		// Still fresh in flight.
		return
	default:
		// The front has been in flight too long; disown its holder so the
		// slice can be re-issued to a peer that will actually respond.
		log.Warnf("Header range at height %d from peer %v has stalled",
			front.start, front.peer.Addr())
		delete(hs.ranges, front.start)
		delete(hs.peerRange, front.peer)
	}

	// Prefer an idle peer; otherwise steal the slice from the peer holding
	// the range with the greatest start.
	var target *peerpkg.Peer
	for _, p := range hs.peers {
		if _, ok := hs.peerRange[p]; !ok {
			target = p
			break
		}
	}
	if target == nil {
		for _, p := range hs.peers {
			rng := hs.peerRange[p]
			if target == nil || rng.start > hs.peerRange[target].start {
				target = p
			}
		}
		if target != nil {
			rng := hs.peerRange[target]
			log.Warnf("Re-issuing header range at height %d to peer %v "+
				"by taking over its range at height %d", hs.nextHeight,
				target.Addr(), rng.start)
			delete(hs.ranges, rng.start)
			delete(hs.peerRange, target)
		}
	}
	if target == nil {
		return
	}

	sm.reissueHeaderRange(target, hs.nextHeight)
}

// headerRangeAppliable reports whether a received front header range can be
// applied to the header chain: its first header's prev block must already be
// present in the block index (i.e. connected to the applied chain).  During
// the parallel download a misattached range can pass the receive-side prev
// check (a stale height row fed by a polluted index, P8) while its prev
// actually belongs to a discarded chain -- such a range would never be
// appliable and would freeze the header front if left queued (P10).
func (sm *SyncManager) headerRangeAppliable(front *headerRange) bool {
	if front == nil || len(front.headers) == 0 {
		return false
	}
	return sm.chain.IsValidHeader(&front.headers[0].PrevBlock)
}

// handleNotFoundMsg handles notfound messages from all peers.
func (sm *SyncManager) handleNotFoundMsg(nfmsg *notFoundMsg) {
	peer := nfmsg.peer
	state, exists := sm.peerStates[peer]
	if !exists {
		log.Warnf("Received notfound message from unknown peer %s", peer)
		return
	}
	for _, inv := range nfmsg.notFound.InvList {
		// verify the hash was actually announced by the peer
		// before deleting from the global requested maps.
		switch inv.Type {
		case wire.InvTypeWitnessBlock:
			fallthrough
		case wire.InvTypeBlock:
			if _, exists := state.requestedBlocks[inv.Hash]; exists {
				delete(state.requestedBlocks, inv.Hash)
				delete(sm.requestedBlocks, inv.Hash)
			}

		case wire.InvTypeWitnessTx:
			fallthrough
		case wire.InvTypeTx:
			if _, exists := state.requestedTxns[inv.Hash]; exists {
				delete(state.requestedTxns, inv.Hash)
				delete(sm.requestedTxns, inv.Hash)
			}
		}
	}
}

// haveInventory returns whether or not the inventory represented by the passed
// inventory vector is known.  This includes checking all of the various places
// inventory can be when it is in different states such as blocks that are part
// of the main chain, on a side chain, in the orphan pool, and transactions that
// are in the memory pool (either the main pool or orphan pool).
func (sm *SyncManager) haveInventory(invVect *wire.InvVect) (bool, error) {
	switch invVect.Type {
	case wire.InvTypeWitnessBlock:
		fallthrough
	case wire.InvTypeBlock:
		// Ask chain if the block is known to it in any form (main
		// chain, side chain, or orphan).
		return sm.chain.HaveBlock(&invVect.Hash)

	case wire.InvTypeWitnessTx:
		fallthrough
	case wire.InvTypeTx:
		// Ask the transaction memory pool if the transaction is known
		// to it in any form (main pool or orphan).
		if sm.txMemPool.HaveTransaction(&invVect.Hash) {
			return true, nil
		}

		// Check if the transaction exists from the point of view of the
		// end of the main chain.  Note that this is only a best effort
		// since it is expensive to check existence of every output and
		// the only purpose of this check is to avoid downloading
		// already known transactions.  Only the first two outputs are
		// checked because the vast majority of transactions consist of
		// two outputs where one is some form of "pay-to-somebody-else"
		// and the other is a change output.
		prevOut := wire.OutPoint{Hash: invVect.Hash}
		for i := uint32(0); i < 2; i++ {
			prevOut.Index = i
			entry, err := sm.chain.FetchUtxoEntry(prevOut)
			if err != nil {
				return false, err
			}
			if entry != nil && !entry.IsSpent() {
				return true, nil
			}
		}

		return false, nil
	}

	// The requested inventory is an unsupported type, so just claim
	// it is known to avoid requesting it.
	return true, nil
}

// handleInvMsg handles inv messages from all peers.
// We examine the inventory advertised by the remote peer and act accordingly.
func (sm *SyncManager) handleInvMsg(imsg *invMsg) {
	peer := imsg.peer
	state, exists := sm.peerStates[peer]
	if !exists {
		log.Warnf("Received inv message from unknown peer %s", peer)
		return
	}

	// Attempt to find the final block in the inventory list.  There may
	// not be one.
	lastBlock := -1
	invVects := imsg.inv.InvList
	for i := len(invVects) - 1; i >= 0; i-- {
		if invVects[i].Type == wire.InvTypeBlock {
			lastBlock = i
			break
		}
	}

	// If this inv contains a block announcement, and this isn't coming from
	// our current sync peer or we're current, then update the last
	// announced block for this peer. We'll use this information later to
	// update the heights of peers based on blocks we've accepted that they
	// previously announced.
	if lastBlock != -1 && (peer != sm.syncPeer || sm.current()) {
		peer.UpdateLastAnnouncedBlock(&invVects[lastBlock].Hash)
	}

	// Ignore invs from peers that aren't the sync peer if we are not
	// current. Helps prevent fetching a mass of orphans. When syncPeer
	// is nil, accept invs from any peer.
	if sm.syncPeer != nil && peer != sm.syncPeer && !sm.current() {
		return
	}

	// If our chain is current and a peer announces a block we already
	// know of, then update their current block height.
	if lastBlock != -1 && sm.current() {
		blkHeight, err := sm.chain.BlockHeightByHash(&invVects[lastBlock].Hash)
		if err == nil {
			peer.UpdateLastBlockHeight(blkHeight)
		}
	}

	// Request the advertised inventory if we don't already have it.  Also,
	// request parent blocks of orphans if we receive one we already have.
	// Finally, attempt to detect potential stalls due to long side chains
	// we already have and request more blocks to prevent them.
	for i, iv := range invVects {
		// Ignore unsupported inventory types.
		switch iv.Type {
		case wire.InvTypeBlock:
		case wire.InvTypeTx:
		case wire.InvTypeWitnessBlock:
		case wire.InvTypeWitnessTx:
		default:
			continue
		}

		// Add the inventory to the cache of known inventory
		// for the peer.
		peer.AddKnownInventory(iv)

		// Ignore inventory when we're in the initial block download mode.
		if sm.ibdMode {
			continue
		}

		// Request the inventory if we don't already have it.
		haveInv, err := sm.haveInventory(iv)
		if err != nil {
			log.Warnf("Unexpected failure when checking for "+
				"existing inventory during inv message "+
				"processing: %v", err)
			continue
		}
		if !haveInv {
			if iv.Type == wire.InvTypeTx {
				// Skip the transaction if it has already been
				// rejected.
				if _, exists := sm.rejectedTxns[iv.Hash]; exists {
					continue
				}
			}

			// Ignore invs block invs from non-witness enabled
			// peers, as after segwit activation we only want to
			// download from peers that can provide us full witness
			// data for blocks.
			if !peer.IsWitnessEnabled() && iv.Type == wire.InvTypeBlock {
				continue
			}

			// Add it to the request queue.
			state.requestQueue = append(state.requestQueue, iv)
			continue
		}

		if iv.Type == wire.InvTypeBlock {
			// The block is an orphan block that we already have.
			// When the existing orphan was processed, it requested
			// the missing parent blocks.  When this scenario
			// happens, it means there were more blocks missing
			// than are allowed into a single inventory message.  As
			// a result, once this peer requested the final
			// advertised block, the remote peer noticed and is now
			// resending the orphan block as an available block
			// to signal there are more missing blocks that need to
			// be requested.
			if sm.chain.IsKnownOrphan(&iv.Hash) {
				// Request blocks starting at the latest known
				// up to the root of the orphan that just came
				// in.
				orphanRoot := sm.chain.GetOrphanRoot(&iv.Hash)
				locator, err := sm.chain.LatestBlockLocator()
				if err != nil {
					log.Errorf("PEER: Failed to get block "+
						"locator for the latest block: "+
						"%v", err)
					continue
				}
				peer.PushGetBlocksMsg(locator, orphanRoot)
				continue
			}

			// We already have the final block advertised by this
			// inventory message, so force a request for more.  This
			// should only happen if we're on a really long side
			// chain.
			if i == lastBlock {
				// Request blocks after this one up to the
				// final one the remote peer knows about (zero
				// stop hash).
				locator := sm.chain.BlockLocatorFromHash(&iv.Hash)
				peer.PushGetBlocksMsg(locator, &zeroHash)
			}
		}
	}

	// Request as much as possible at once.  Anything that won't fit into
	// the request will be requested on the next inv message.
	numRequested := 0
	gdmsg := wire.NewMsgGetData()
	requestQueue := state.requestQueue
	for len(requestQueue) != 0 {
		iv := requestQueue[0]
		requestQueue[0] = nil
		requestQueue = requestQueue[1:]

		switch iv.Type {
		case wire.InvTypeWitnessBlock:
			fallthrough
		case wire.InvTypeBlock:
			// Request the block if there is not already a pending
			// request.
			if _, exists := sm.requestedBlocks[iv.Hash]; !exists {
				limitAdd(sm.requestedBlocks, iv.Hash, maxRequestedBlocks)
				limitAdd(state.requestedBlocks, iv.Hash, maxRequestedBlocks)

				if peer.IsWitnessEnabled() {
					iv.Type = wire.InvTypeWitnessBlock
				}

				gdmsg.AddInvVect(iv)
				numRequested++
			}

		case wire.InvTypeWitnessTx:
			fallthrough
		case wire.InvTypeTx:
			// Request the transaction if there is not already a
			// pending request.
			if _, exists := sm.requestedTxns[iv.Hash]; !exists {
				limitAdd(sm.requestedTxns, iv.Hash, maxRequestedTxns)
				limitAdd(state.requestedTxns, iv.Hash, maxRequestedTxns)

				// If the peer is capable, request the txn
				// including all witness data.
				if peer.IsWitnessEnabled() {
					iv.Type = wire.InvTypeWitnessTx
				}

				gdmsg.AddInvVect(iv)
				numRequested++
			}
		}

		if numRequested >= wire.MaxInvPerMsg {
			break
		}
	}
	state.requestQueue = requestQueue
	if len(gdmsg.InvList) > 0 {
		peer.QueueMessage(gdmsg, nil)
	}
}

// blockHandler is the main handler for the sync manager.  It must be run as a
// goroutine.  It processes block and inv messages in a separate goroutine
// from the peer handlers so the block (MsgBlock) messages are handled by a
// single thread without needing to lock memory data structures.  This is
// important because the sync manager controls which blocks are needed and how
// the fetching should proceed.
func (sm *SyncManager) blockHandler() {
	stallTicker := time.NewTicker(stallSampleInterval)
	defer stallTicker.Stop()
	progressTicker := time.NewTicker(syncProgressLogInterval)
	defer progressTicker.Stop()

out:
	for {
		select {
		case m := <-sm.msgChan:
			switch msg := m.(type) {
			case *newPeerMsg:
				sm.handleNewPeerMsg(msg.peer)

			case *txMsg:
				sm.handleTxMsg(msg)
				msg.reply <- struct{}{}

			case *blockMsg:
				sm.handleBlockMsg(msg)
				msg.reply <- struct{}{}

			case *invMsg:
				sm.handleInvMsg(msg)

			case *headersMsg:
				sm.handleHeadersMsg(msg)

			case *notFoundMsg:
				sm.handleNotFoundMsg(msg)

			case *donePeerMsg:
				sm.handleDonePeerMsg(msg.peer)

			case getSyncPeerMsg:
				var peerID int32
				if sm.syncPeer != nil {
					peerID = sm.syncPeer.ID()
				}
				msg.reply <- peerID

			case getSyncStatusMsg:
				msg.reply <- sm.syncStatusSnapshot()

			case processBlockMsg:
				_, isOrphan, err := sm.chain.ProcessBlock(
					msg.block, msg.flags)
				if err != nil {
					msg.reply <- processBlockResponse{
						isOrphan: false,
						err:      err,
					}
					continue
				}
				msg.reply <- processBlockResponse{
					isOrphan: isOrphan,
					err:      nil,
				}

			case isCurrentMsg:
				msg.reply <- sm.current()

			case pauseMsg:
				// Wait until the sender unpauses the manager.
				<-msg.unpause

			default:
				log.Warnf("Invalid message type in block "+
					"handler: %T", msg)
			}

		case <-stallTicker.C:
			sm.handleStallSample()

		case <-progressTicker.C:
			sm.logSyncProgress()

		case <-sm.quit:
			break out
		}
	}

	log.Debug("Block handler shutting down: flushing blockchain caches...")
	if err := sm.chain.FlushUtxoCache(blockchain.FlushRequired); err != nil {
		log.Errorf("Error while flushing blockchain caches: %v", err)
	}

	sm.wg.Done()
	log.Trace("Block handler done")
}

// handleBlockchainNotification handles notifications from blockchain.  It does
// things such as request orphan block parents and relay accepted blocks to
// connected peers.
func (sm *SyncManager) handleBlockchainNotification(notification *blockchain.Notification) {
	switch notification.Type {
	// A block has been accepted into the block chain.  Relay it to other
	// peers.
	case blockchain.NTBlockAccepted:
		// Don't relay if we are not current. Other peers that are
		// current should already know about it.
		// TEMP DEBUG: log the relay gate decision / 临时调试:记录 relay 门控决策
		block, _ := notification.Data.(*btcutil.Block)
		if block != nil {
			log.Warnf("TEMP-DBG NTBlockAccepted hash=%s current=%v (relay gate)", block.Hash(), sm.current())
		}
		if !sm.current() {
			return
		}

		block, ok := notification.Data.(*btcutil.Block)
		if !ok {
			log.Warnf("Chain accepted notification is not a block.")
			break
		}

		// Generate the inventory vector and relay it.
		iv := wire.NewInvVect(wire.InvTypeBlock, block.Hash())
		log.Warnf("TEMP-DBG relaying block inv %s", block.Hash())
		sm.peerNotifier.RelayInventory(iv, block.MsgBlock().Header)

	// A block has been connected to the main block chain.
	case blockchain.NTBlockConnected:
		// Don't attempt to update the mempool if we're not current.
		// The mempool is empty and the fee estimator is useless unless
		// we're caught up.
		if !sm.current() {
			return
		}

		block, ok := notification.Data.(*btcutil.Block)
		if !ok {
			log.Warnf("Chain connected notification is not a block.")
			break
		}

		// Remove all of the transactions (except the coinbase) in the
		// connected block from the transaction pool.  Secondly, remove any
		// transactions which are now double spends as a result of these
		// new transactions.  Finally, remove any transaction that is
		// no longer an orphan. Transactions which depend on a confirmed
		// transaction are NOT removed recursively because they are still
		// valid.
		for _, tx := range block.Transactions()[1:] {
			sm.txMemPool.RemoveTransaction(tx, false)
			sm.txMemPool.RemoveDoubleSpends(tx)
			sm.txMemPool.RemoveOrphan(tx)
			sm.peerNotifier.TransactionConfirmed(tx)
			acceptedTxs := sm.txMemPool.ProcessOrphans(tx)
			sm.peerNotifier.AnnounceNewTransactions(acceptedTxs)
		}

		// Register block with the fee estimator, if it exists.
		if sm.feeEstimator != nil {
			err := sm.feeEstimator.RegisterBlock(block)

			// If an error is somehow generated then the fee estimator
			// has entered an invalid state. Since it doesn't know how
			// to recover, create a new one.
			if err != nil {
				sm.feeEstimator = mempool.NewFeeEstimator(
					mempool.DefaultEstimateFeeMaxRollback,
					mempool.DefaultEstimateFeeMinRegisteredBlocks)
			}
		}

	// A block has been disconnected from the main block chain.
	case blockchain.NTBlockDisconnected:
		block, ok := notification.Data.(*btcutil.Block)
		if !ok {
			log.Warnf("Chain disconnected notification is not a block.")
			break
		}

		// Reinsert all of the transactions (except the coinbase) into
		// the transaction pool.
		for _, tx := range block.Transactions()[1:] {
			_, _, err := sm.txMemPool.MaybeAcceptTransaction(tx,
				false, false)
			if err != nil {
				// Remove the transaction and all transactions
				// that depend on it if it wasn't accepted into
				// the transaction pool.
				sm.txMemPool.RemoveTransaction(tx, true)
			}
		}

		// Rollback previous block recorded by the fee estimator.
		if sm.feeEstimator != nil {
			sm.feeEstimator.Rollback(block.Hash())
		}
	}
}

// NewPeer informs the sync manager of a newly active peer.
func (sm *SyncManager) NewPeer(peer *peerpkg.Peer) {
	// Ignore if we are shutting down.
	if atomic.LoadInt32(&sm.shutdown) != 0 {
		return
	}
	sm.msgChan <- &newPeerMsg{peer: peer}
}

// QueueTx adds the passed transaction message and peer to the block handling
// queue. Responds to the done channel argument after the tx message is
// processed.
func (sm *SyncManager) QueueTx(tx *btcutil.Tx, peer *peerpkg.Peer, done chan struct{}) {
	// Don't accept more transactions if we're shutting down.
	if atomic.LoadInt32(&sm.shutdown) != 0 {
		done <- struct{}{}
		return
	}

	sm.msgChan <- &txMsg{tx: tx, peer: peer, reply: done}
}

// QueueBlock adds the passed block message and peer to the block handling
// queue. Responds to the done channel argument after the block message is
// processed.
func (sm *SyncManager) QueueBlock(block *btcutil.Block, peer *peerpkg.Peer, done chan struct{}) {
	// Don't accept more blocks if we're shutting down.
	if atomic.LoadInt32(&sm.shutdown) != 0 {
		done <- struct{}{}
		return
	}

	sm.msgChan <- &blockMsg{block: block, peer: peer, reply: done}
}

// QueueInv adds the passed inv message and peer to the block handling queue.
func (sm *SyncManager) QueueInv(inv *wire.MsgInv, peer *peerpkg.Peer) {
	// No channel handling here because peers do not need to block on inv
	// messages.
	if atomic.LoadInt32(&sm.shutdown) != 0 {
		return
	}

	sm.msgChan <- &invMsg{inv: inv, peer: peer}
}

// QueueHeaders adds the passed headers message and peer to the block handling
// queue.
func (sm *SyncManager) QueueHeaders(headers *wire.MsgHeaders, peer *peerpkg.Peer) {
	// No channel handling here because peers do not need to block on
	// headers messages.
	if atomic.LoadInt32(&sm.shutdown) != 0 {
		return
	}

	sm.msgChan <- &headersMsg{headers: headers, peer: peer}
}

// QueueNotFound adds the passed notfound message and peer to the block handling
// queue.
func (sm *SyncManager) QueueNotFound(notFound *wire.MsgNotFound, peer *peerpkg.Peer) {
	// No channel handling here because peers do not need to block on
	// reject messages.
	if atomic.LoadInt32(&sm.shutdown) != 0 {
		return
	}

	sm.msgChan <- &notFoundMsg{notFound: notFound, peer: peer}
}

// DonePeer informs the blockmanager that a peer has disconnected.
func (sm *SyncManager) DonePeer(peer *peerpkg.Peer) {
	// Ignore if we are shutting down.
	if atomic.LoadInt32(&sm.shutdown) != 0 {
		return
	}

	sm.msgChan <- &donePeerMsg{peer: peer}
}

// Start begins the core block handler which processes block and inv messages.
func (sm *SyncManager) Start() {
	// Already started?
	if atomic.AddInt32(&sm.started, 1) != 1 {
		return
	}

	log.Trace("Starting sync manager")
	sm.wg.Add(1)
	go sm.blockHandler()
}

// Stop gracefully shuts down the sync manager by stopping all asynchronous
// handlers and waiting for them to finish.
func (sm *SyncManager) Stop() error {
	if atomic.AddInt32(&sm.shutdown, 1) != 1 {
		log.Warnf("Sync manager is already in the process of " +
			"shutting down")
		return nil
	}

	log.Infof("Sync manager shutting down")
	close(sm.quit)
	sm.wg.Wait()
	return nil
}

// SyncPeerID returns the ID of the current sync peer, or 0 if there is none.
func (sm *SyncManager) SyncPeerID() int32 {
	reply := make(chan int32)
	sm.msgChan <- getSyncPeerMsg{reply: reply}
	return <-reply
}

// SyncStatus returns a snapshot of the sync manager's parallel initial
// download state, including the header range and block slice assigned to each
// participating peer.  It is safe for concurrent access: the snapshot is built
// inside the blockHandler goroutine so it does not require any locking and does
// not perturb the download path beyond a single extra channel message.
func (sm *SyncManager) SyncStatus() *SyncStatus {
	reply := make(chan *SyncStatus)
	sm.msgChan <- getSyncStatusMsg{reply: reply}
	return <-reply
}

// syncStatusSnapshot builds the per-peer SyncStatus snapshot.  It must be
// called from the blockHandler goroutine.  The cost is O(number of peers):
// it walks peerStates once and consults the assigned-slice/range maps, so it
// adds no measurable work to the download path.
func (sm *SyncManager) syncStatusSnapshot() *SyncStatus {
	_, tip := sm.chain.BestHeader()
	status := &SyncStatus{
		Current:         sm.current(),
		IBD:             sm.ibdMode,
		BestChainHeight: sm.chain.BestSnapshot().Height,
		HeaderTip:       tip,
	}

	if hs := sm.headerSync; hs != nil {
		status.HeaderTarget = hs.target
		status.HeaderNextAssign = hs.nextAssign
		status.HeaderSliceLen = hs.sliceLen
		status.HeaderPaused = hs.leadPaused
	} else {
		status.HeaderSliceLen = sm.headerSliceLen
	}
	status.HeaderRecentRanges = append([]HeaderRecentRange(nil), sm.headerRecent...)
	if bs := sm.blockSyncState; bs != nil {
		status.BlockTarget = bs.target
		status.BlockNextAssign = bs.nextAssign
		status.BlockWindow = maxBlockRequestWindow
	}

	status.Peers = make([]PeerSyncStatus, 0, len(sm.peerStates))
	for peer, state := range sm.peerStates {
		ps := PeerSyncStatus{
			ID:             peer.ID(),
			Addr:           peer.Addr(),
			SyncNode:       sm.syncPeer == peer,
			SyncCandidate:  state.syncCandidate,
			CurrentHeight:  peer.LastBlock(),
			InFlightBlocks: len(state.requestedBlocks),
		}

		if hs := sm.headerSync; hs != nil {
			if hr, ok := hs.peerRange[peer]; ok && hr != nil {
				ps.HeaderRangeStart = hr.start
				ps.HeaderRangeEnd = hr.start + int32(len(hr.headers))
				ps.HeaderRangeReceived = hr.received
				ps.HeaderRangeApplied = hr.applied
				ps.HeaderRangeAssignedAt = hr.assignedAt.Unix()
			}
		}
		if bs := sm.blockSyncState; bs != nil {
			if sl, ok := bs.peerSlice[peer]; ok && sl != nil {
				ps.SliceStart = sl.start
				ps.SliceEnd = sl.end
				ps.SliceAssignedAt = sl.assignedAt.Unix()
				ps.SliceReceived = sl.received
			}
			if last, ok := bs.lastProgress[peer]; ok {
				ps.LastBlockAt = last.Unix()
			}
		}

		status.Peers = append(status.Peers, ps)
	}

	return status
}

// ProcessBlock makes use of ProcessBlock on an internal instance of a block
// chain.
func (sm *SyncManager) ProcessBlock(block *btcutil.Block, flags blockchain.BehaviorFlags) (bool, error) {
	reply := make(chan processBlockResponse, 1)
	sm.msgChan <- processBlockMsg{block: block, flags: flags, reply: reply}
	response := <-reply
	return response.isOrphan, response.err
}

// IsCurrent returns whether or not the sync manager believes it is synced with
// the connected peers.
func (sm *SyncManager) IsCurrent() bool {
	reply := make(chan bool)
	sm.msgChan <- isCurrentMsg{reply: reply}
	return <-reply
}

// Pause pauses the sync manager until the returned channel is closed.
//
// Note that while paused, all peer and block processing is halted.  The
// message sender should avoid pausing the sync manager for long durations.
func (sm *SyncManager) Pause() chan<- struct{} {
	c := make(chan struct{})
	sm.msgChan <- pauseMsg{c}
	return c
}

// New constructs a new SyncManager. Use Start to begin processing asynchronous
// block, tx, and inv updates.
func New(config *Config) (*SyncManager, error) {
	sm := SyncManager{
		peerNotifier:       config.PeerNotifier,
		chain:              config.Chain,
		txMemPool:          config.TxMemPool,
		chainParams:        config.ChainParams,
		rejectedTxns:       make(map[chainhash.Hash]struct{}),
		requestedTxns:      make(map[chainhash.Hash]struct{}),
		requestedBlocks:    make(map[chainhash.Hash]struct{}),
		peerStates:         make(map[*peerpkg.Peer]*peerSyncState),
		suspiciousHeaders:  make(map[chainhash.Hash]struct{}),
		frontUnreachable:   make(map[int32]map[string]time.Time),
		progressLogger:     newBlockProgressLogger("Processed", log),
		msgChan:            make(chan interface{}, config.MaxPeers*3),
		quit:               make(chan struct{}),
		feeEstimator:       config.FeeEstimator,
		blockSyncStartLead: config.BlockSyncStartLead,
	}

	if config.DisableCheckpoints {
		log.Info("Checkpoints are disabled")
	}

	sm.chain.Subscribe(sm.handleBlockchainNotification)

	return &sm, nil
}
