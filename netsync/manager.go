// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package netsync

import (
	"math/rand"
	"net"
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
	maxBlockRequestWindow = 8192

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

	// blockInFlightTarget is the number of blocks each participating peer in
	// the parallel block download keeps in flight at a time.  A single
	// buildBlockRequest fills the peer's window up to this target instead of
	// draining its whole slice at once, so a slow peer keeps getting topped
	// up by blkDownload instead of sitting idle behind a full in-flight set
	// that never drains.  Draining an entire slice at once starved every peer
	// but the fastest one, which alone re-claimed the new frontier slices and
	// reduced the parallel download to a single peer.
	blockInFlightTarget = 200
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
}

// blockSyncState tracks the parallel (multi-peer) initial block download.  It
// is only accessed from the blockHandler goroutine and is non-nil while blocks
// are being fetched from several peers simultaneously.
type blockSyncState struct {
	nextAssign  int32                         // next height to hand out to a peer
	target      int32                         // highest header height to reach
	slices      map[int32]*blockSlice         // assigned slices by start height
	peerSlice   map[*peerpkg.Peer]*blockSlice // slice currently assigned to each peer
	sliceLen    int32                         // max height span handed to a peer at once
	lastReissue time.Time                     // last time a stale slice was reissued
	lastProgress map[*peerpkg.Peer]time.Time  // last time each peer delivered a block
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

	// blockSync is the set of peers taking part in a parallel initial block
	// download.  Each participating peer is handed a disjoint slice of the
	// header chain and tops itself back up as it drains.  It is only touched
	// from the blockHandler goroutine.
	blockSync []*peerpkg.Peer

	// blockSyncState tracks the per-peer disjoint height slices of an
	// in-progress parallel initial block download.  It is nil while no
	// parallel block download is running and only touched from the
	// blockHandler goroutine.
	blockSyncState *blockSyncState

	// The following fields are used for the initial block download mode.
	ibdMode bool

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
// responsible for choosing non-overlapping start/end heights.
func (sm *SyncManager) launchHeaderRange(peer *peerpkg.Peer, start int32) {
	hs := sm.headerSync
	rng := &headerRange{
		start:      start,
		peer:       peer,
		assignedAt: time.Now(),
	}
	hs.ranges[start] = rng
	hs.peerRange[peer] = rng

	peer.PushGetHeadersMsg(sm.headerLocator(start-1), &zeroHash)
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

	sm.launchHeaderRange(peer, start)

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
	// stopped producing data can be detected and freed by the stall handler.
	if bs := sm.blockSyncState; bs != nil {
		bs.lastProgress[peer] = time.Now()
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
	// blocks immediately.  After that, there is nothing more to do.
	if !sm.ibdMode {
		if err := sm.chain.FlushUtxoCache(blockchain.FlushPeriodic); err != nil {
			log.Errorf("Error while flushing the blockchain cache: %v", err)
		}
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
	if !frontInFlight && bestHeight+1 <= windowEnd {
		// The front slice is the critical one: whoever holds it controls
		// how fast the tip advances.  A peer that has just been freed for
		// stalling must not immediately re-claim it, or it would just
		// freeze the download again; prefer a peer that has recently shown
		// it can deliver.
		if last, ok := bs.lastProgress[peer]; !ok ||
			time.Since(last) < blockSliceStallTimeout {
			start = bestHeight + 1
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

		iv := wire.NewInvVect(wire.InvTypeBlock, hash)
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

			// Always request plain block inventory.  Sugarchain has no
			// segregated witness, and its peers do not serve witness-block
			// inventory types; a getdata populated with InvTypeWitnessBlock is
			// silently ignored by them even though they advertise the legacy
			// witness service flag.
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

		have, err := sm.chain.HaveBlock(hash)
		if err != nil || !have {
			return
		}

		_, behaviorFlags := sm.checkHeadersList(hash)
		if _, err := sm.chain.ResumeBlockConnect(hash, behaviorFlags); err != nil {
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

	// Ignore duplicate responses for a range that has already been served.
	if rng.received {
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
	if expected, err := sm.chain.HeaderHashByHeight(rng.start - 1); err == nil {
		if !headers[0].PrevBlock.IsEqual(expected) {
			log.Warnf("Peer %v returned headers that do not extend the "+
				"range starting at %d -- ignoring stale response",
				peer.Addr(), rng.start)
			return
		}
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
		if front == nil || !front.received {
			break
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
				log.Warnf("Received block header from peer %v "+
					"failed header verification -- disconnecting: %v",
					front.peer.Addr(), err)
				front.peer.Disconnect()
				sm.abortHeaderSync()
				return
			}
			sm.progressLogger.SetLastLogTime(time.Now())
		}

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

	// Hand off to the block download once every header through the tallest
	// participating peer has been applied.
	if hs.nextHeight > hs.target {
		sm.finishHeaderSync()
	}
}

// finishHeaderSync signals that the parallel header download has caught up to
// the connected peers' tips and the initial block download can proceed with
// fetching the blocks themselves.
func (sm *SyncManager) finishHeaderSync() {
	hs := sm.headerSync
	if hs == nil {
		return
	}
	sm.headerSync = nil

	bestHash, bestHeight := sm.chain.BestHeader()
	log.Infof("downloaded headers to %v(%v) in %d parallel "+
		"slices -- now fetching blocks", bestHash, bestHeight, len(hs.peers))

	// Hand the block download off to all of the peers that served headers so
	// the initial block download is also performed in parallel.  Each peer is
	// handed a disjoint slice of the header chain (see assignBlockSlice) so
	// multiple peers fetch different heights simultaneously instead of one
	// peer claiming the entire request window ahead of the tip.  If we end up
	// with no peers the normal new-peer/stall handling will restart the sync.
	sm.ibdMode = true
	sm.blockSync = hs.peers
	if len(sm.blockSync) > maxHeaderSyncPeers {
		sm.blockSync = sm.blockSync[:maxHeaderSyncPeers]
	}
	if len(sm.blockSync) == 0 {
		log.Infof("No peers remaining for parallel block download")
		return
	}

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
	bestHeight = sm.chain.BestSnapshot().Height
	_, bestHeaderHeight := sm.chain.BestHeader()
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
		// Already in hand; it will be applied momentarily.
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

			case processBlockMsg:
				_, isOrphan, err := sm.chain.ProcessBlock(
					msg.block, msg.flags)
				if err != nil {
					msg.reply <- processBlockResponse{
						isOrphan: false,
						err:      err,
					}
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
		peerNotifier:    config.PeerNotifier,
		chain:           config.Chain,
		txMemPool:       config.TxMemPool,
		chainParams:     config.ChainParams,
		rejectedTxns:    make(map[chainhash.Hash]struct{}),
		requestedTxns:   make(map[chainhash.Hash]struct{}),
		requestedBlocks: make(map[chainhash.Hash]struct{}),
		peerStates:      make(map[*peerpkg.Peer]*peerSyncState),
		progressLogger:  newBlockProgressLogger("Processed", log),
		msgChan:         make(chan interface{}, config.MaxPeers*3),
		quit:            make(chan struct{}),
		feeEstimator:    config.FeeEstimator,
	}

	if config.DisableCheckpoints {
		log.Info("Checkpoints are disabled")
	}

	sm.chain.Subscribe(sm.handleBlockchainNotification)

	return &sm, nil
}
