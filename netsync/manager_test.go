package netsync

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/database"
	_ "github.com/btcsuite/btcd/database/ffldb"
	"github.com/btcsuite/btcd/mempool"
	"github.com/btcsuite/btcd/peer"
	"github.com/btcsuite/btcd/pow"
	"github.com/btcsuite/btcd/txscript/v2"
	"github.com/btcsuite/btcd/wire/v2"
	"github.com/stretchr/testify/require"
)

// The package-level log variable is nil by default. Set it to the
// disabled logger so that log calls in the sync manager don't panic.
func init() {
	DisableLog()
}

// noopPeerNotifier is a no-op implementation of PeerNotifier for tests.
type noopPeerNotifier struct{}

func (noopPeerNotifier) AnnounceNewTransactions([]*mempool.TxDesc)            {}
func (noopPeerNotifier) UpdatePeerHeights(*chainhash.Hash, int32, *peer.Peer) {}
func (noopPeerNotifier) RelayInventory(*wire.InvVect, interface{})            {}
func (noopPeerNotifier) TransactionConfirmed(*btcutil.Tx)                     {}

// dbSetup is used to create a new db with the genesis block already inserted.
// It returns a teardown function the caller should invoke when done testing to
// clean up.  The database is stored under t.TempDir() which is automatically
// removed when the test finishes.
func dbSetup(t *testing.T, params *chaincfg.Params) (database.DB, func(), error) {
	dbPath := filepath.Join(t.TempDir(), "ffldb")
	db, err := database.Create("ffldb", dbPath, params.Net)
	if err != nil {
		return nil, nil, fmt.Errorf("error creating db: %v", err)
	}

	teardown := func() {
		db.Close()
	}

	return db, teardown, nil
}

// chainSetup is used to create a new db and chain instance with the genesis
// block already inserted.  In addition to the new chain instance, it returns
// a teardown function the caller should invoke when done testing to clean up.
func chainSetup(t *testing.T, params *chaincfg.Params) (
	*blockchain.BlockChain, func(), error) {

	db, teardown, err := dbSetup(t, params)
	if err != nil {
		return nil, nil, err
	}

	// Copy the chain params to ensure any modifications the tests do to
	// the chain parameters do not affect the global instance.
	paramsCopy := *params

	// Deep-copy deployment starters/enders so that parallel tests don't
	// race on the shared blockClock field written by SynchronizeClock.
	for i := range paramsCopy.Deployments {
		d := &paramsCopy.Deployments[i]
		if s, ok := d.DeploymentStarter.(*chaincfg.MedianTimeDeploymentStarter); ok {
			d.DeploymentStarter = chaincfg.NewMedianTimeDeploymentStarter(
				s.StartTime())
		}
		if e, ok := d.DeploymentEnder.(*chaincfg.MedianTimeDeploymentEnder); ok {
			d.DeploymentEnder = chaincfg.NewMedianTimeDeploymentEnder(
				e.EndTime())
		}
	}

	// Create the main chain instance.
	chain, err := blockchain.New(&blockchain.Config{
		DB:          db,
		Checkpoints: paramsCopy.Checkpoints,
		ChainParams: &paramsCopy,
		TimeSource:  blockchain.NewMedianTime(),
		SigCache:    txscript.NewSigCache(1000),
	})
	if err != nil {
		teardown()
		err := fmt.Errorf("failed to create chain instance: %v", err)
		return nil, nil, err
	}
	return chain, teardown, nil
}

// loadHeaders loads headers from mainnet from 1 to 11.
func loadHeaders(t *testing.T) []*wire.BlockHeader {
	testFile := "blockheaders-mainnet-1-11.txt"
	filename := filepath.Join("testdata/", testFile)

	file, err := os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}

	headers := make([]*wire.BlockHeader, 0, 10)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		b, err := hex.DecodeString(line)
		if err != nil {
			t.Fatalf("failed to read block headers from file %v", testFile)
		}

		r := bytes.NewReader(b)
		header := new(wire.BlockHeader)
		header.Deserialize(r)

		headers = append(headers, header)
	}

	return headers
}

func makeMockSyncManager(t *testing.T,
	params *chaincfg.Params) (*SyncManager, func()) {

	t.Helper()

	chain, tearDown, err := chainSetup(t, params)
	require.NoError(t, err)

	sm, err := New(&Config{
		PeerNotifier: noopPeerNotifier{},
		Chain:        chain,
		ChainParams:  params,
	})
	require.NoError(t, err)

	return sm, tearDown
}

func TestCheckHeadersList(t *testing.T) {
	// Set params to mainnet with a checkpoint at block 11.
	params := chaincfg.MainNetParams
	checkpointHeight := int32(11)
	checkpointHash, err := chainhash.NewHashFromStr(
		"0000000097be56d606cdd9c54b04d4747e957d3608abe69198c661f2add73073")
	if err != nil {
		t.Fatal(err)
	}
	params.Checkpoints = []chaincfg.Checkpoint{
		{
			Height: checkpointHeight,
			Hash:   checkpointHash,
		},
	}

	// Create mock SyncManager.
	sm, tearDown := makeMockSyncManager(t, &params)
	defer tearDown()

	// Setup SyncManager with headers processed.
	headers := loadHeaders(t)
	for _, header := range headers {
		isMainChain, err := sm.chain.ProcessBlockHeader(
			header, blockchain.BFNone, false)
		if err != nil {
			t.Fatal(err)
		}

		if !isMainChain {
			t.Fatalf("expected block header %v to be in the main chain",
				header.BlockHash())
		}
	}

	tests := []struct {
		hash              string
		isCheckpointBlock bool
		behaviorFlags     blockchain.BehaviorFlags
	}{
		{
			hash:              chaincfg.MainNetParams.GenesisHash.String(),
			isCheckpointBlock: false,
			behaviorFlags:     blockchain.BFFastAdd,
		},
		{
			// Block 10.
			hash:              "000000002c05cc2e78923c34df87fd108b22221ac6076c18f3ade378a4d915e9",
			isCheckpointBlock: false,
			behaviorFlags:     blockchain.BFFastAdd,
		},
		{
			// Block 11.
			hash:              "0000000097be56d606cdd9c54b04d4747e957d3608abe69198c661f2add73073",
			isCheckpointBlock: true,
			behaviorFlags:     blockchain.BFFastAdd,
		},
		{
			// Block 12.
			hash:              "0000000027c2488e2510d1acf4369787784fa20ee084c258b58d9fbd43802b5e",
			isCheckpointBlock: false,
			behaviorFlags:     blockchain.BFNone,
		},
	}

	for _, test := range tests {
		hash, err := chainhash.NewHashFromStr(test.hash)
		if err != nil {
			t.Errorf("NewHashFromStr: %v", err)
			continue
		}

		// Make sure that when the ibd mode is off, we always get
		// false and BFNone.
		sm.ibdMode = false
		isCheckpoint, gotFlags := sm.checkHeadersList(hash)
		require.Equal(t, false, isCheckpoint)
		require.Equal(t, blockchain.BFNone, gotFlags)

		// Now check that the test values are correct.
		sm.ibdMode = true
		isCheckpoint, gotFlags = sm.checkHeadersList(hash)
		require.Equal(t, test.isCheckpointBlock, isCheckpoint)
		require.Equal(t, test.behaviorFlags, gotFlags)
	}
}

func TestFetchHigherPeers(t *testing.T) {
	// Create mock SyncManager.
	sm, tearDown := makeMockSyncManager(t, &chaincfg.MainNetParams)
	defer tearDown()

	tests := []struct {
		peerHeights       []int32
		peerSyncCandidate []bool
		height            int32
		expectedCnt       int
	}{
		{
			peerHeights:       []int32{9, 10, 10, 10},
			peerSyncCandidate: []bool{true, true, true, true},
			height:            5,
			expectedCnt:       4,
		},

		{
			peerHeights:       []int32{9, 10, 10, 10},
			peerSyncCandidate: []bool{false, false, true, true},
			height:            5,
			expectedCnt:       2,
		},

		{
			peerHeights:       []int32{1, 100, 100, 100, 100},
			peerSyncCandidate: []bool{true, false, true, true, false},
			height:            100,
			expectedCnt:       0,
		},
	}

	for _, test := range tests {
		// Setup peers.
		sm.peerStates = make(map[*peer.Peer]*peerSyncState)
		for i, height := range test.peerHeights {
			peer := peer.NewInboundPeer(&peer.Config{})
			peer.UpdateLastBlockHeight(height)
			sm.peerStates[peer] = &peerSyncState{
				syncCandidate:   test.peerSyncCandidate[i],
				requestedTxns:   make(map[chainhash.Hash]struct{}),
				requestedBlocks: make(map[chainhash.Hash]struct{}),
			}
		}

		// Fetch higher peers and assert.
		peers := sm.fetchHigherPeers(test.height)
		require.Equal(t, test.expectedCnt, len(peers))

		for _, peer := range peers {
			state, found := sm.peerStates[peer]
			require.True(t, found)
			require.True(t, state.syncCandidate)
		}
	}
}

// mockTimeSource is used to trick the BlockChain instance to think that we're
// in the past.  This is so that we can force it to return true for isCurrent().
type mockTimeSource struct {
	adjustedTime time.Time
}

// AdjustedTime returns the internal adjustedTime.
//
// Part of the MedianTimeSource interface implementation.
func (m *mockTimeSource) AdjustedTime() time.Time {
	return m.adjustedTime
}

// AddTimeSample isn't relevant so we just leave it as emtpy.
//
// Part of the MedianTimeSource interface implementation.
func (m *mockTimeSource) AddTimeSample(id string, timeVal time.Time) {
	// purposely left empty
}

// Offset isn't relevant so we just return 0.
//
// Part of the MedianTimeSource interface implementation.
func (m *mockTimeSource) Offset() time.Duration {
	return 0
}

// TestBuildBlockRequestSkipsInflightBlocks verifies that buildBlockRequest
// does not re-request blocks that are already in sm.requestedBlocks.  When
// the pipeline refill threshold triggers fetchHeaderBlocks while blocks are
// still in-flight, re-requesting them causes the peer to send duplicates.
// The first copy gets processed (removing the hash from requestedBlocks),
// and the second copy then arrives as "unrequested", disconnecting the peer.
func TestBuildBlockRequestSkipsInflightBlocks(t *testing.T) {
	tests := []struct {
		name string
		// inflightHeights are the block heights already in
		// requestedBlocks before calling buildBlockRequest.
		inflightHeights []int32
		// wantRequestedHeights are the block heights that should
		// appear in the returned getdata message.
		wantRequestedHeights []int32
	}{
		{
			name:                 "no blocks in-flight requests all",
			inflightHeights:      nil,
			wantRequestedHeights: []int32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11},
		},
		{
			name:                 "all blocks in-flight requests none",
			inflightHeights:      []int32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11},
			wantRequestedHeights: nil,
		},
		{
			name:                 "first 5 in-flight requests remaining 6",
			inflightHeights:      []int32{1, 2, 3, 4, 5},
			wantRequestedHeights: []int32{6, 7, 8, 9, 10, 11},
		},
		{
			name:                 "last 6 in-flight requests first 5",
			inflightHeights:      []int32{6, 7, 8, 9, 10, 11},
			wantRequestedHeights: []int32{1, 2, 3, 4, 5},
		},
		{
			name:                 "scattered in-flight requests gaps",
			inflightHeights:      []int32{2, 4, 6, 8, 10},
			wantRequestedHeights: []int32{1, 3, 5, 7, 9, 11},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := chaincfg.MainNetParams
			params.Checkpoints = nil
			sm, tearDown := makeMockSyncManager(t, &params)
			defer tearDown()

			// Process headers 1-11 so the header chain is
			// ahead of the block chain.
			headers := loadHeaders(t)
			for _, header := range headers {
				_, err := sm.chain.ProcessBlockHeader(
					header, blockchain.BFNone, false)
				require.NoError(t, err)
			}

			// Set up a disconnected peer as syncPeer.
			syncPeer := peer.NewInboundPeer(&peer.Config{})
			sm.syncPeer = syncPeer
			syncPeerState := &peerSyncState{
				requestedTxns:   make(map[chainhash.Hash]struct{}),
				requestedBlocks: make(map[chainhash.Hash]struct{}),
			}
			sm.peerStates[syncPeer] = syncPeerState

			// Pre-populate in-flight blocks.
			for _, h := range tc.inflightHeights {
				hash, err := sm.chain.HeaderHashByHeight(h)
				require.NoError(t, err)
				sm.requestedBlocks[*hash] = struct{}{}
				syncPeerState.requestedBlocks[*hash] = struct{}{}
			}

			gdmsg := sm.buildBlockRequest(syncPeer)

			// Collect the hashes from the getdata message.
			got := make(map[chainhash.Hash]struct{}, len(gdmsg.InvList))
			for _, iv := range gdmsg.InvList {
				got[iv.Hash] = struct{}{}
			}

			require.Equal(t, len(tc.wantRequestedHeights), len(gdmsg.InvList))
			for _, h := range tc.wantRequestedHeights {
				hash, err := sm.chain.HeaderHashByHeight(h)
				require.NoError(t, err)
				require.Contains(t, got, *hash,
					"block at height %d should be requested", h)
			}
			for _, h := range tc.inflightHeights {
				hash, err := sm.chain.HeaderHashByHeight(h)
				require.NoError(t, err)
				require.NotContains(t, got, *hash,
					"in-flight block at height %d should not be re-requested", h)
			}
		})
	}
}

func TestIsInIBDMode(t *testing.T) {
	tests := []struct {
		peerState  map[*peer.Peer]*peerSyncState
		params     *chaincfg.Params
		timesource *mockTimeSource
		isIBDMode  bool
	}{
		// Is not current, higher peers.
		{
			params: &chaincfg.MainNetParams,
			peerState: func() map[*peer.Peer]*peerSyncState {
				ps := make(map[*peer.Peer]*peerSyncState)
				peer := peer.NewInboundPeer(&peer.Config{})
				peer.UpdateLastBlockHeight(900_000)
				ps[peer] = &peerSyncState{
					syncCandidate:   true,
					requestedTxns:   make(map[chainhash.Hash]struct{}),
					requestedBlocks: make(map[chainhash.Hash]struct{}),
				}
				return ps
			}(),
			timesource: nil,
			isIBDMode:  true,
		},
		// Is not current, no higher peers.
		{
			params: &chaincfg.MainNetParams,
			peerState: func() map[*peer.Peer]*peerSyncState {
				ps := make(map[*peer.Peer]*peerSyncState)
				peer := peer.NewInboundPeer(&peer.Config{})
				peer.UpdateLastBlockHeight(0)
				ps[peer] = &peerSyncState{
					syncCandidate:   true,
					requestedTxns:   make(map[chainhash.Hash]struct{}),
					requestedBlocks: make(map[chainhash.Hash]struct{}),
				}
				return ps
			}(),
			timesource: nil,
			isIBDMode:  true,
		},
		// Is current, higher peers.
		{
			params: func() *chaincfg.Params {
				params := chaincfg.MainNetParams
				params.Checkpoints = nil
				return &params
			}(),
			peerState: func() map[*peer.Peer]*peerSyncState {
				ps := make(map[*peer.Peer]*peerSyncState)
				peer := peer.NewInboundPeer(&peer.Config{})
				peer.UpdateLastBlockHeight(900_000)
				ps[peer] = &peerSyncState{
					syncCandidate:   true,
					requestedTxns:   make(map[chainhash.Hash]struct{}),
					requestedBlocks: make(map[chainhash.Hash]struct{}),
				}
				return ps
			}(),
			timesource: &mockTimeSource{
				chaincfg.MainNetParams.GenesisBlock.Header.Timestamp,
			},
			isIBDMode: true,
		},
		// Is current, no higher peers.
		{
			params: func() *chaincfg.Params {
				params := chaincfg.MainNetParams
				params.Checkpoints = nil
				return &params
			}(),
			peerState: func() map[*peer.Peer]*peerSyncState {
				ps := make(map[*peer.Peer]*peerSyncState)
				peer := peer.NewInboundPeer(&peer.Config{})
				peer.UpdateLastBlockHeight(0)
				ps[peer] = &peerSyncState{
					syncCandidate:   true,
					requestedTxns:   make(map[chainhash.Hash]struct{}),
					requestedBlocks: make(map[chainhash.Hash]struct{}),
				}
				return ps
			}(),
			timesource: &mockTimeSource{
				chaincfg.MainNetParams.GenesisBlock.Header.Timestamp,
			},
			isIBDMode: false,
		},
	}

	for _, test := range tests {
		db, tearDown, err := dbSetup(t, test.params)
		if err != nil {
			tearDown()
			t.Fatal(err)
		}

		timesource := blockchain.NewMedianTime()
		if test.timesource != nil {
			timesource = test.timesource
		}

		// Create the main chain instance.
		chain, err := blockchain.New(&blockchain.Config{
			DB:          db,
			Checkpoints: test.params.Checkpoints,
			ChainParams: test.params,
			TimeSource:  timesource,
			SigCache:    txscript.NewSigCache(1000),
		})
		if err != nil {
			tearDown()
			t.Fatal(err)
		}
		sm, err := New(&Config{
			Chain:       chain,
			ChainParams: test.params,
		})
		if err != nil {
			tearDown()
			t.Fatal(err)
		}

		// Run test and assert.
		sm.peerStates = test.peerState
		got := sm.isInIBDMode()
		require.Equal(t, test.isIBDMode, got)
		tearDown()
	}
}

// createTestCoinbase creates a minimal coinbase transaction for the given
// block height.  The signature script encodes the height to ensure unique
// transaction hashes across blocks.
func createTestCoinbase(height int32, params *chaincfg.Params) *wire.MsgTx {
	tx := wire.NewMsgTx(wire.TxVersion)

	// Encode the height as a minimally encoded script integer and add a trailing
	// OP_0 so the script also satisfies the generic coinbase-length checks.
	sigScript, err := txscript.NewScriptBuilder().
		AddInt64(int64(height)).
		AddInt64(0).
		Script()
	if err != nil {
		panic(fmt.Sprintf("unable to encode coinbase height %d: %v",
			height, err))
	}

	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  chainhash.Hash{},
			Index: wire.MaxPrevOutIndex,
		},
		SignatureScript: sigScript,
		Sequence:        wire.MaxTxInSequenceNum,
	})

	tx.AddTxOut(&wire.TxOut{
		Value:    blockchain.CalcBlockSubsidy(height, params),
		PkScript: []byte{txscript.OP_TRUE},
	})

	return tx
}

// solveTestBlock finds a nonce that satisfies the proof of work for the given
// header.  With regression test parameters the difficulty is minimal and a
// solution is found almost immediately.
func solveTestBlock(header *wire.BlockHeader, params *chaincfg.Params) bool {
	target := blockchain.CompactToBig(params.PowLimitBits)
	for nonce := uint32(0); nonce < math.MaxUint32; nonce++ {
		header.Nonce = nonce
		hash := pow.BlockPoWHash(header)
		if blockchain.HashToBig(&hash).Cmp(target) <= 0 {
			return true
		}
	}

	return false
}

// generateTestBlocks creates count valid blocks chaining from the genesis
// block of the given params.  Each block contains only a coinbase transaction.
func generateTestBlocks(
	t *testing.T, params *chaincfg.Params, count int) []*btcutil.Block {

	t.Helper()

	blocks := make([]*btcutil.Block, 0, count)
	prevHash := params.GenesisHash
	prevTime := params.GenesisBlock.Header.Timestamp

	for h := int32(1); h <= int32(count); h++ {
		cb := createTestCoinbase(h, params)
		merkleRoot := cb.TxHash()

		header := wire.BlockHeader{
			// Regtest enforces the BIP34/65/66 version floor from height 1.
			Version:    4,
			PrevBlock:  *prevHash,
			MerkleRoot: merkleRoot,
			Timestamp:  prevTime.Add(time.Minute),
			Bits:       params.PowLimitBits,
		}
		require.True(t, solveTestBlock(&header, params),
			"failed to solve block at height %d", h)

		msgBlock := &wire.MsgBlock{
			Header:       header,
			Transactions: []*wire.MsgTx{cb},
		}
		block := btcutil.NewBlock(msgBlock)
		blocks = append(blocks, block)

		bh := block.Hash()
		prevHash = bh
		prevTime = header.Timestamp
	}

	return blocks
}

// TestSyncStateMachine exercises the end-to-end IBD sync flow:
//
//	┌→ startSync
//	│      ↓
//	│  fetchHeaders
//	│      ↓
//	│  handleHeadersMsg
//	│      ↓
//	│  fetchHeaderBlocks ←┐
//	│      ↓              │ (refill)
//	│  handleBlockMsg ────┘──→ IBD complete
//	│
//	│  (stall detected at any phase above)
//	│      ↓
//	│  handleStallSample
//	│      ↓
//	└── handleDonePeerMsg
//
// It verifies that header processing transitions to block download, that the
// pipeline refill path in handleBlockMsg is exercised, and that IBD mode is
// properly cleared once the chain catches up to the best header.
//
// The "fresh ibd" case tests a complete sync from genesis: headers are fetched
// and then blocks are downloaded.
//
// The "stall before any headers" and "stall mid header download" cases test
// recovery when the sync peer stalls during header download.  A replacement
// peer delivers the remaining (or all) headers and then all blocks.
//
// The "headers complete peer stalls on blocks" case tests recovery when the
// sync peer delivers all headers but stalls before sending any blocks; a
// replacement peer downloads all blocks.
//
// The "stalled sync peer recovery" case tests recovery mid-block-download: a
// sync peer stops responding after some blocks, handleStallSample detects the
// inactivity, the stalled peer is disconnected, and a replacement peer
// finishes IBD.
//
// The "stall mid headers then stall on blocks" case combines both failure
// modes: one peer stalls during headers (peer 2 takes over and finishes
// headers), then peer 2 stalls during block download (peer 3 finishes blocks).
// This exercises recovery across three distinct peers.
func TestSyncStateMachine(t *testing.T) {
	t.Parallel()

	const testTotalBlocks = 2 * minInFlightBlocks

	tests := []struct {
		name        string
		totalBlocks int

		// stallHeadersAfter, when >= 0, triggers a stall during
		// header download: deliver this many headers, then stall
		// the sync peer and verify a replacement finishes header
		// download.  Set to -1 for no header stall.
		stallHeadersAfter int

		// stallAfter, when >= 0, triggers a stall during block
		// download: deliver all headers, then process this many
		// blocks before stalling.  Set to -1 for no block stall.
		stallAfter int
	}{
		{
			name:              "fresh ibd",
			totalBlocks:       testTotalBlocks,
			stallHeadersAfter: -1,
			stallAfter:        -1,
		},
		{
			name:              "stall before any headers",
			totalBlocks:       testTotalBlocks,
			stallHeadersAfter: 0,
			stallAfter:        -1,
		},
		{
			name:              "stall mid header download",
			totalBlocks:       testTotalBlocks,
			stallHeadersAfter: testTotalBlocks / 2,
			stallAfter:        -1,
		},
		{
			name:              "headers complete peer stalls on blocks",
			totalBlocks:       testTotalBlocks,
			stallHeadersAfter: -1,
			stallAfter:        0,
		},
		{
			name:              "stalled sync peer recovery",
			totalBlocks:       testTotalBlocks,
			stallHeadersAfter: -1,
			stallAfter:        5,
		},
		{
			name:              "stall mid headers then stall on blocks",
			totalBlocks:       testTotalBlocks,
			stallHeadersAfter: testTotalBlocks / 2,
			stallAfter:        5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			params := chaincfg.RegressionNetParams
			params.Checkpoints = nil

			sm, tearDown := makeMockSyncManager(t, &params)
			defer tearDown()

			blocks := generateTestBlocks(t, &params, tc.totalBlocks)

			// Register a sync candidate and call startSync,
			// which activates IBD mode and sends getheaders.
			peer1 := startIBD(t, sm, tc.totalBlocks)

			if tc.stallHeadersAfter >= 0 {
				// Stall during header download;
				// replacement sends remaining headers.
				peer2 := newSyncCandidate(t, sm,
					int32(tc.totalBlocks))
				syncStalledHeaderRecovery(
					t, sm, peer1, peer2,
					blocks, tc.stallHeadersAfter,
					tc.totalBlocks,
				)

				if tc.stallAfter >= 0 {
					peer3 := newSyncCandidate(t, sm,
						int32(tc.totalBlocks))
					syncStalledPeerRecovery(
						t, sm, peer2,
						peer3, blocks,
						tc.stallAfter,
						tc.totalBlocks,
					)
				} else {
					syncProcessBlocks(t, sm,
						peer2, blocks,
						tc.totalBlocks)
				}
			} else {
				syncSendHeaders(t, sm, peer1,
					blocks, tc.totalBlocks)

				if tc.stallAfter >= 0 {
					peer2 := newSyncCandidate(t, sm,
						int32(tc.totalBlocks))
					syncStalledPeerRecovery(
						t, sm, peer1,
						peer2, blocks,
						tc.stallAfter,
						tc.totalBlocks,
					)
				} else {
					syncProcessBlocks(t, sm,
						peer1, blocks,
						tc.totalBlocks)
				}
			}
		})
	}
}

// newSyncCandidate creates and registers a sync-candidate peer at the
// given height without triggering startSync.
func newSyncCandidate(t *testing.T, sm *SyncManager,
	height int32) *peer.Peer {

	t.Helper()

	p := peer.NewInboundPeer(&peer.Config{
		ChainParams: sm.chainParams,
	})
	p.UpdateLastBlockHeight(height)
	sm.peerStates[p] = &peerSyncState{
		syncCandidate:   true,
		requestedTxns:   make(map[chainhash.Hash]struct{}),
		requestedBlocks: make(map[chainhash.Hash]struct{}),
	}
	return p
}

// assertIBDComplete verifies that IBD finished: chain height matches
// totalBlocks, ibdMode is off, and no blocks remain in-flight.
func assertIBDComplete(t *testing.T, sm *SyncManager,
	peerState *peerSyncState, totalBlocks int) {

	t.Helper()

	best := sm.chain.BestSnapshot()
	require.Equal(t, int32(totalBlocks), best.Height)
	require.False(t, sm.ibdMode,
		"ibdMode should be off after catching up")
	require.Empty(t, sm.requestedBlocks,
		"all requested blocks should be fulfilled")
	require.Empty(t, peerState.requestedBlocks,
		"peer should have no outstanding block requests")
}

// startIBD registers a sync peer and calls startSync, verifying that IBD
// mode is activated and the peer is selected.
func startIBD(t *testing.T, sm *SyncManager,
	peerHeight int) *peer.Peer {

	t.Helper()

	syncPeer := newSyncCandidate(t, sm, int32(peerHeight))

	sm.startSync()

	require.True(t, sm.syncPeer == syncPeer,
		"syncPeer should be set after startSync")
	require.True(t, sm.ibdMode, "ibdMode should be on")
	require.False(t, sm.lastProgressTime.IsZero(),
		"lastProgressTime should be set")

	return syncPeer
}

// syncSendHeaders delivers block headers to the sync manager and verifies
// that block requests are generated.
func syncSendHeaders(t *testing.T, sm *SyncManager,
	syncPeer *peer.Peer, blocks []*btcutil.Block, totalBlocks int) {

	t.Helper()

	// Reset the progress time to a zero sentinel so the assertion below
	// verifies that handleHeadersMsg writes it without depending on the
	// system clock advancing between calls to time.Now.
	sm.lastProgressTime = time.Time{}

	headers := wire.NewMsgHeaders()
	for _, block := range blocks {
		err := headers.AddBlockHeader(&block.MsgBlock().Header)
		require.NoError(t, err)
	}

	sm.handleHeadersMsg(&headersMsg{
		headers: headers,
		peer:    syncPeer,
	})

	_, bestHeaderHeight := sm.chain.BestHeader()
	require.Equal(t, int32(totalBlocks), bestHeaderHeight)

	require.False(t, sm.lastProgressTime.IsZero(),
		"handleHeadersMsg should update lastProgressTime")

	wantRequested := make(map[chainhash.Hash]struct{}, len(blocks))
	for _, block := range blocks {
		wantRequested[*block.Hash()] = struct{}{}
	}
	require.Equal(t, wantRequested, sm.requestedBlocks)
	require.Equal(t, wantRequested, sm.peerStates[syncPeer].requestedBlocks)
}

// syncProcessBlocks feeds all blocks to handleBlockMsg and verifies that IBD
// mode remains active until the final block, at which point IBD completes.
func syncProcessBlocks(t *testing.T, sm *SyncManager, syncPeer *peer.Peer,
	blocks []*btcutil.Block, totalBlocks int) {

	t.Helper()

	peerState := sm.peerStates[syncPeer]

	for i, block := range blocks {
		sm.handleBlockMsg(&blockMsg{
			block: block,
			peer:  syncPeer,
			reply: make(chan struct{}, 1),
		})

		if i < len(blocks)-1 {
			require.True(t, sm.ibdMode,
				"ibdMode should still be on at height %d", i+1)
		}
	}

	assertIBDComplete(t, sm, peerState, totalBlocks)
}

// syncStalledPeerRecovery processes stallAfter blocks from stalledPeer,
// triggers stall detection, verifies that stalledPeer is removed and
// replacementPeer takes over, then feeds remaining blocks and verifies
// IBD completes.
func syncStalledPeerRecovery(t *testing.T, sm *SyncManager,
	stalledPeer, replacementPeer *peer.Peer,
	blocks []*btcutil.Block, stallAfter, totalBlocks int) {

	t.Helper()

	// Process the first stallAfter blocks from the stalled peer.
	for _, block := range blocks[:stallAfter] {
		sm.handleBlockMsg(&blockMsg{
			block: block,
			peer:  stalledPeer,
			reply: make(chan struct{}, 1),
		})
	}

	best := sm.chain.BestSnapshot()
	require.Equal(t, int32(stallAfter), best.Height)
	require.True(t, sm.ibdMode)

	// Trigger stall detection.
	sm.lastProgressTime = time.Now().Add(
		-(maxStallDuration + time.Minute))
	sm.handleStallSample()

	// Verify that handleStallSample called Disconnect() on the
	// stalled peer (which closes p.quit, making WaitForDisconnect
	// return immediately).
	disconnected := make(chan struct{})
	go func() {
		stalledPeer.WaitForDisconnect()
		close(disconnected)
	}()
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("Disconnect() was not called on stalled peer")
	}

	// Snapshot the stalled peer's outstanding requested blocks before
	// disconnection so we can verify they are cleaned up.
	stalledState := sm.peerStates[stalledPeer]
	stalledRequested := make([]chainhash.Hash, 0, len(stalledState.requestedBlocks))
	for hash := range stalledState.requestedBlocks {
		stalledRequested = append(stalledRequested, hash)
	}
	require.NotEmpty(t, stalledRequested,
		"stalled peer should have outstanding requested blocks")

	// In production, Disconnect() triggers handleDonePeerMsg
	// asynchronously via the peer goroutine.  Call it directly to
	// complete the removal.  Note: handleDonePeerMsg first clears the
	// stalled peer's requested blocks from the global map via
	// clearRequestedState, then updateSyncPeer → startSync immediately
	// re-requests them for the replacement peer.
	sm.handleDonePeerMsg(stalledPeer)

	_, stalledTracked := sm.peerStates[stalledPeer]
	require.False(t, stalledTracked,
		"stalled peer should be removed")
	require.True(t, sm.syncPeer == replacementPeer,
		"replacement peer should take over as sync peer")
	require.True(t, sm.ibdMode)

	// Verify that the replacement peer re-requested the exact same
	// blocks that were outstanding from the stalled peer.
	replacementState := sm.peerStates[replacementPeer]
	require.Equal(t, len(stalledRequested),
		len(replacementState.requestedBlocks),
		"replacement peer should request same number of blocks")
	for _, hash := range stalledRequested {
		_, exists := replacementState.requestedBlocks[hash]
		require.True(t, exists,
			"block %v should be requested from replacement peer",
			hash)
	}

	// Feed remaining blocks from the replacement peer.
	for _, block := range blocks[stallAfter:] {
		sm.handleBlockMsg(&blockMsg{
			block: block,
			peer:  replacementPeer,
			reply: make(chan struct{}, 1),
		})
	}

	assertIBDComplete(t, sm, replacementState, totalBlocks)
}

// syncStalledHeaderRecovery simulates a stall during header download.
// It optionally delivers headersSent headers from stalledPeer, triggers stall
// detection, verifies that stalledPeer is removed and replacementPeer takes
// over, then delivers remaining headers and verifies block requests are
// generated.  The caller is responsible for the block-download phase.
func syncStalledHeaderRecovery(t *testing.T, sm *SyncManager,
	stalledPeer, replacementPeer *peer.Peer,
	blocks []*btcutil.Block, headersSent, totalBlocks int) {

	t.Helper()

	// Deliver partial headers from the stalled peer.  When
	// headersSent is 0, this is a no-op (peer stalls immediately).
	if headersSent > 0 {
		headers := wire.NewMsgHeaders()
		for _, block := range blocks[:headersSent] {
			err := headers.AddBlockHeader(
				&block.MsgBlock().Header)
			require.NoError(t, err)
		}

		sm.handleHeadersMsg(&headersMsg{
			headers: headers,
			peer:    stalledPeer,
		})

		_, bestHeaderHeight := sm.chain.BestHeader()
		require.Equal(t, int32(headersSent), bestHeaderHeight)
	}

	// No blocks should have been requested during header download
	// since the headers haven't caught up to the peer's height yet.
	require.Empty(t, sm.requestedBlocks,
		"no blocks should be requested during header download")

	// Trigger stall detection.
	sm.lastProgressTime = time.Now().Add(
		-(maxStallDuration + time.Minute))
	sm.handleStallSample()

	// Verify that handleStallSample called Disconnect() on the
	// stalled peer.
	disconnected := make(chan struct{})
	go func() {
		stalledPeer.WaitForDisconnect()
		close(disconnected)
	}()
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("Disconnect() was not called on stalled peer")
	}

	// Complete peer removal.  handleDonePeerMsg clears state and
	// triggers startSync which selects the replacement peer.
	sm.handleDonePeerMsg(stalledPeer)

	_, stalledTracked := sm.peerStates[stalledPeer]
	require.False(t, stalledTracked,
		"stalled peer should be removed")
	require.True(t, sm.syncPeer == replacementPeer,
		"replacement peer should take over as sync peer")
	require.True(t, sm.ibdMode)

	// Deliver remaining headers from the replacement peer.  When
	// headersSent is 0, this is all headers.
	remainingHeaders := wire.NewMsgHeaders()
	for _, block := range blocks[headersSent:] {
		err := remainingHeaders.AddBlockHeader(
			&block.MsgBlock().Header)
		require.NoError(t, err)
	}
	sm.handleHeadersMsg(&headersMsg{
		headers: remainingHeaders,
		peer:    replacementPeer,
	})

	_, bestHeaderHeight := sm.chain.BestHeader()
	require.Equal(t, int32(totalBlocks), bestHeaderHeight)

	// Verify all blocks were requested from the replacement.
	wantRequested := make(map[chainhash.Hash]struct{}, len(blocks))
	for _, block := range blocks {
		wantRequested[*block.Hash()] = struct{}{}
	}
	require.Equal(t, wantRequested, sm.requestedBlocks)
	replacementState := sm.peerStates[replacementPeer]
	require.Equal(t, wantRequested, replacementState.requestedBlocks)
}

// TestStartSyncBlockFallback verifies the startSync fallback path where
// headers are already caught up but the block chain lags behind.  In this
// case startSync should skip header download and directly request blocks.
func TestStartSyncBlockFallback(t *testing.T) {
	t.Parallel()

	params := chaincfg.RegressionNetParams
	params.Checkpoints = nil

	sm, tearDown := makeMockSyncManager(t, &params)
	defer tearDown()

	// Process headers so the header chain is at numBlocks while the
	// block chain stays at genesis.
	const numBlocks = 11
	blocks := generateTestBlocks(t, &params, numBlocks)
	for _, block := range blocks {
		_, err := sm.chain.ProcessBlockHeader(
			&block.MsgBlock().Header, blockchain.BFNone, false)
		require.NoError(t, err)
	}

	// Add a peer whose height equals the header height.
	// fetchHigherPeers(bestHeaderHeight) returns nothing because
	// the peer is not strictly higher than our headers.
	// fetchHigherPeers(bestBlockHeight=0) returns the peer.
	syncPeer := peer.NewInboundPeer(&peer.Config{})
	syncPeer.UpdateLastBlockHeight(int32(numBlocks))
	sm.peerStates[syncPeer] = &peerSyncState{
		syncCandidate:   true,
		requestedTxns:   make(map[chainhash.Hash]struct{}),
		requestedBlocks: make(map[chainhash.Hash]struct{}),
	}

	sm.startSync()

	require.NotNil(t, sm.syncPeer,
		"sync peer should be set for block download")
	require.NotEmpty(t, sm.requestedBlocks,
		"blocks should be requested via fetchHeaderBlocks")
}

// TestStallNoDisconnectAtSameHeight verifies that handleStallSample does
// not disconnect a sync peer whose advertised height equals our own.
func TestStallNoDisconnectAtSameHeight(t *testing.T) {
	t.Parallel()

	params := chaincfg.RegressionNetParams
	params.Checkpoints = nil

	sm, tearDown := makeMockSyncManager(t, &params)
	defer tearDown()

	p := peer.NewInboundPeer(&peer.Config{})
	p.UpdateLastBlockHeight(0) // Same height as our genesis chain.
	sm.peerStates[p] = &peerSyncState{}
	sm.syncPeer = p
	sm.ibdMode = true
	sm.lastProgressTime = time.Now().Add(
		-(maxStallDuration + time.Minute))

	sm.handleStallSample()

	_, tracked := sm.peerStates[p]
	require.True(t, tracked,
		"peer at same height should not be disconnected")
	require.Nil(t, sm.syncPeer,
		"we should have nil syncPeer after handleStallSample")
}

// TestStartSyncChainCurrent verifies that startSync does not set syncPeer
// or ibdMode when the chain is current and no peer is strictly higher.
// isInIBDMode sees IsCurrent()==true with no higher peers, returns false,
// and startSync exits immediately.
func TestStartSyncChainCurrent(t *testing.T) {
	t.Parallel()

	params := chaincfg.RegressionNetParams
	params.Checkpoints = nil

	sm, tearDown := makeMockSyncManager(t, &params)
	defer tearDown()

	// Mine a single block with a recent timestamp so
	// IsCurrent() returns true.
	cb := createTestCoinbase(1, &params)
	header := wire.BlockHeader{
		// Regtest enforces the BIP34/65/66 version floor from height 1.
		Version:    4,
		PrevBlock:  *params.GenesisHash,
		MerkleRoot: cb.TxHash(),
		Timestamp:  time.Now().Truncate(time.Second),
		Bits:       params.PowLimitBits,
	}
	require.True(t, solveTestBlock(&header, &params))

	block := btcutil.NewBlock(&wire.MsgBlock{
		Header:       header,
		Transactions: []*wire.MsgTx{cb},
	})
	_, _, err := sm.chain.ProcessBlock(block, blockchain.BFNone)
	require.NoError(t, err)
	require.True(t, sm.chain.IsCurrent())

	// Peer at our height — not higher.
	newSyncCandidate(t, sm, 1)

	sm.startSync()

	require.Nil(t, sm.syncPeer,
		"syncPeer should not be set when chain is already current")
	require.False(t, sm.ibdMode,
		"ibdMode should not be activated when chain is already current")
}

// TestIsSyncCandidateRegtest verifies that isSyncCandidate accepts peers
// on regtest and simnet based on their service flags.
func TestIsSyncCandidateRegtest(t *testing.T) {
	t.Parallel()

	params := chaincfg.RegressionNetParams
	sm, tearDown := makeMockSyncManager(t, &params)
	defer tearDown()

	tests := []struct {
		name      string
		flags     wire.ServiceFlag
		lastBlock int32
		want      bool
	}{
		{
			name:  "just node network",
			flags: wire.SFNodeNetwork,
			want:  true,
		},
		{
			name:  "just limited network",
			flags: wire.SFNodeNetworkLimited,
			want:  true,
		},
		{
			name:      "limited network with block ahead",
			flags:     wire.SFNodeNetworkLimited,
			lastBlock: wire.NodeNetworkLimitedBlockThreshold + 1,
			want:      false,
		},
		{
			name:  "node network and limited node network",
			flags: wire.SFNodeNetwork | wire.SFNodeNetworkLimited,
			want:  true,
		},
		{
			name:  "no flags",
			flags: 0,
			want:  false,
		},
		{
			name:  "different flag",
			flags: wire.SFNodeBloom,
			want:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := peer.NewInboundPeer(&peer.Config{
				ChainParams: sm.chainParams,
				Services:    tc.flags,
			})
			p.UpdateLastBlockHeight(tc.lastBlock)

			got := sm.isSyncCandidate(p)
			require.Equal(t, tc.want, got)
		})
	}
}

// makeTestHeaderChain builds n block headers (heights 1..n) chained onto the
// provided params' genesis block with monotonically increasing timestamps and
// an easy (regtest-style) difficulty so they pass header validation.
func makeTestHeaderChain(t *testing.T, params *chaincfg.Params,
	n int) []*wire.BlockHeader {

	headers := make([]*wire.BlockHeader, 0, n)
	prevHash := *params.GenesisHash
	prevTime := params.GenesisBlock.Header.Timestamp
	for i := 0; i < n; i++ {
		header := &wire.BlockHeader{
			Version:   4,
			PrevBlock: prevHash,
			Bits:      params.PowLimitBits,
			Timestamp: prevTime.Add(time.Duration(i+1) * time.Second),
		}
		prevHash = header.BlockHash()
		prevTime = header.Timestamp
		headers = append(headers, header)
	}
	return headers
}

// headerMsgFor wraps the given headers in a headers message from the peer.
func headerMsgFor(peer *peer.Peer, headers []*wire.BlockHeader) *headersMsg {
	msg := wire.NewMsgHeaders()
	for _, h := range headers {
		msg.AddBlockHeader(h)
	}
	return &headersMsg{headers: msg, peer: peer}
}

// newHeaderSyncPeers registers n peers that advertise the given last block
// height and returns them.
func newHeaderSyncPeers(t *testing.T, sm *SyncManager,
	n int, lastBlock int32) []*peer.Peer {

	peers := make([]*peer.Peer, 0, n)
	for i := 0; i < n; i++ {
		p := peer.NewInboundPeer(&peer.Config{})
		p.UpdateLastBlockHeight(lastBlock)
		sm.peerStates[p] = &peerSyncState{
			syncCandidate:   true,
			requestedTxns:   make(map[chainhash.Hash]struct{}),
			requestedBlocks: make(map[chainhash.Hash]struct{}),
		}
		peers = append(peers, p)
	}
	return peers
}

// startParallelHeaderSync installs a parallel header download over the given
// peers with a small slice size so the test only needs a handful of headers.
func startParallelHeaderSync(t *testing.T, sm *SyncManager,
	peers []*peer.Peer, target int32, sliceLen int32) {

	sm.ibdMode = true
	sm.headerSync = &headerSyncState{
		peers:      append([]*peer.Peer(nil), peers...),
		target:     target,
		nextHeight: 1,
		nextAssign: 1,
		ranges:     make(map[int32]*headerRange),
		peerRange:  make(map[*peer.Peer]*headerRange),
		sliceLen:   sliceLen,
	}
	for _, p := range peers {
		require.True(t, sm.assignHeaderRange(p))
	}
}

// TestParallelHeaderSync verifies the multi-peer header download applies
// slices in chain order even when the responses arrive out of order, hands the
// freed peers the next slice, and hands off to the block download once it has
// caught up.
func TestParallelHeaderSync(t *testing.T) {
	params := chaincfg.RegressionNetParams
	params.Checkpoints = nil
	sm, tearDown := makeMockSyncManager(t, &params)
	defer tearDown()

	const numHeaders = 20
	const sliceLen = 5
	headers := makeTestHeaderChain(t, &params, numHeaders)

	peers := newHeaderSyncPeers(t, sm, 3, numHeaders)
	sm.syncPeer = peers[0]
	startParallelHeaderSync(t, sm, peers, numHeaders, sliceLen)

	// The first round assigns three disjoint slices: [1,6), [6,11), [11,16).
	hs := sm.headerSync
	require.Equal(t, int32(16), hs.nextAssign)
	require.Equal(t, 3, len(hs.ranges))
	require.NotNil(t, hs.ranges[1])
	require.NotNil(t, hs.ranges[6])
	require.NotNil(t, hs.ranges[11])

	// Deliver the back slices first (out of order).
	sm.handleHeadersMsg(headerMsgFor(peers[1], headers[5:10]))
	sm.handleHeadersMsg(headerMsgFor(peers[2], headers[10:15]))

	// Nothing may be applied yet since the front slice [1,6) is still
	// outstanding; the back slices are buffered.
	_, bestHeaderHeight := sm.chain.BestHeader()
	require.Equal(t, int32(0), bestHeaderHeight)
	require.Equal(t, 3, len(sm.headerSync.ranges))

	// Deliver the front slice; this lets the buffered slices be applied in
	// order and hands a freed peer the remaining [16,21) slice.
	sm.handleHeadersMsg(headerMsgFor(peers[0], headers[0:5]))

	hs = sm.headerSync
	require.NotNil(t, hs)
	require.NotNil(t, hs.ranges[16])

	// Deliver the final slice to complete the download.
	sm.handleHeadersMsg(headerMsgFor(peers[0], headers[15:20]))

	// All headers must be applied and the download handed off to blocks.
	_, bestHeaderHeight = sm.chain.BestHeader()
	require.Equal(t, int32(numHeaders), bestHeaderHeight)
	require.Nil(t, sm.headerSync)
	require.True(t, sm.ibdMode)
}

// TestParallelHeaderSyncDropFrontPeer verifies that when the peer holding the
// front slice disconnects while every remaining peer is busy with a back slice,
// the front slice is re-issued immediately instead of stalling the download.
func TestParallelHeaderSyncDropFrontPeer(t *testing.T) {
	params := chaincfg.RegressionNetParams
	params.Checkpoints = nil
	sm, tearDown := makeMockSyncManager(t, &params)
	defer tearDown()

	const numHeaders = 20
	const sliceLen = 5
	headers := makeTestHeaderChain(t, &params, numHeaders)

	peers := newHeaderSyncPeers(t, sm, 3, numHeaders)
	sm.syncPeer = peers[1]
	startParallelHeaderSync(t, sm, peers, numHeaders, sliceLen)

	// peers[0] holds the front [1,6).  Drop it while peers[1] and peers[2]
	// are still busy with [6,11) and [11,16).
	sm.dropHeaderPeer(peers[0])

	hs := sm.headerSync
	require.NotNil(t, hs)

	// The front must be covered again by a remaining peer.
	require.NotNil(t, hs.ranges[1])
	require.NotEqual(t, peers[0], hs.ranges[1].peer)

	// Deliver all slices in order (whatever peer currently holds each one)
	// and verify the download completes.
	front := hs.ranges[1]
	sm.handleHeadersMsg(headerMsgFor(front.peer, headers[0:5]))
	if rng := hs.ranges[6]; rng != nil {
		sm.handleHeadersMsg(headerMsgFor(rng.peer, headers[5:10]))
	}
	if rng := hs.ranges[11]; rng != nil {
		sm.handleHeadersMsg(headerMsgFor(rng.peer, headers[10:15]))
	}
	if rng := hs.ranges[16]; rng != nil {
		sm.handleHeadersMsg(headerMsgFor(rng.peer, headers[15:20]))
	}

	// Drain any remaining slices until the download finishes.
	bestHeight := int32(0)
	for i := 0; i < 10 && sm.headerSync != nil; i++ {
		hs = sm.headerSync
		_, bestHeight = sm.chain.BestHeader()
		rng := hs.ranges[hs.nextHeight]
		if rng == nil {
			break
		}
		lo := int(rng.start - 1)
		sm.handleHeadersMsg(headerMsgFor(rng.peer, headers[lo:lo+sliceLen]))
	}

	_, bestHeight = sm.chain.BestHeader()
	require.Equal(t, int32(numHeaders), bestHeight)
	require.Nil(t, sm.headerSync)
}

// TestParallelHeaderSyncShortResponse verifies that a peer which advertises a
// taller chain than it can actually serve does not stall the download: its
// short response simply advances the front to where its chain ends.
func TestParallelHeaderSyncShortResponse(t *testing.T) {
	params := chaincfg.RegressionNetParams
	params.Checkpoints = nil
	sm, tearDown := makeMockSyncManager(t, &params)
	defer tearDown()

	const numHeaders = 20
	const sliceLen = 5
	headers := makeTestHeaderChain(t, &params, numHeaders)

	// peers[0] advertises the full chain but actually only has the first
	// 8 headers (its LastBlock stays 20, but it will serve fewer).
	peers := newHeaderSyncPeers(t, sm, 3, numHeaders)
	sm.syncPeer = peers[1]
	startParallelHeaderSync(t, sm, peers, numHeaders, sliceLen)

	// Deliver peers[0]'s front slice [1,6) normally, then peers[1]'s slice
	// [6,11) shortened to 3 headers (a peer whose chain ends at 8).
	sm.handleHeadersMsg(headerMsgFor(peers[0], headers[0:5]))
	sm.handleHeadersMsg(headerMsgFor(peers[1], headers[5:8]))

	hs := sm.headerSync
	require.NotNil(t, hs)

	// Headers 1..8 should have been applied, and the missing [9,11) hole
	// must have been handed out (capped so it does not overlap [11,16)).
	_, bestHeaderHeight := sm.chain.BestHeader()
	require.Equal(t, int32(8), bestHeaderHeight)
	require.Equal(t, int32(9), hs.nextHeight)
	require.NotNil(t, hs.ranges[9])
	require.NotNil(t, hs.ranges[11])
}

// TestParallelHeaderSyncAddPeer verifies that a peer arriving after the parallel
// header download has started is folded in (granted a slice) when it is admissible,
// is rejected once the peer cap is reached, and is ignored when it cannot serve
// headers we don't already have.
func TestParallelHeaderSyncAddPeer(t *testing.T) {
	params := chaincfg.RegressionNetParams
	params.Checkpoints = nil
	sm, tearDown := makeMockSyncManager(t, &params)
	defer tearDown()

	const numHeaders = 20
	const sliceLen = 5
	makeTestHeaderChain(t, &params, numHeaders)

	peers := newHeaderSyncPeers(t, sm, 3, numHeaders)
	sm.syncPeer = peers[0]

	// Use a large slice length so only one peer is busy per round and the
	// download is guaranteed to still be in progress (headerSync non-nil).
	sm.ibdMode = true
	sm.headerSync = &headerSyncState{
		peers:      []*peer.Peer{peers[0], peers[1]},
		target:     numHeaders,
		nextHeight: 1,
		nextAssign: 1,
		ranges:     make(map[int32]*headerRange),
		peerRange:  make(map[*peer.Peer]*headerRange),
		sliceLen:   sliceLen,
	}
	require.True(t, sm.assignHeaderRange(peers[0]))
	require.True(t, sm.assignHeaderRange(peers[1]))

	// A taller peer is admitted and handed a slice.
	sm.headerSyncAddPeer(peers[2])
	hs := sm.headerSync
	require.Len(t, hs.peers, 3)
	require.NotNil(t, hs.peerRange[peers[2]])

	// A peer that cannot contribute past our (still genesis) header height is
	// not admitted.
	stalePeer := peer.NewInboundPeer(&peer.Config{})
	stalePeer.UpdateLastBlockHeight(0)
	sm.peerStates[stalePeer] = &peerSyncState{
		syncCandidate:   true,
		requestedTxns:   make(map[chainhash.Hash]struct{}),
		requestedBlocks: make(map[chainhash.Hash]struct{}),
	}
	sm.headerSyncAddPeer(stalePeer)
	require.Len(t, hs.peers, 3)

	// A duplicate peer is not admitted twice.
	sm.headerSyncAddPeer(peers[0])
	require.Len(t, hs.peers, 3)

	// Once the cap is reached, extra peers are rejected.
	for i := 0; i < 10; i++ {
		p := peer.NewInboundPeer(&peer.Config{})
		p.UpdateLastBlockHeight(numHeaders)
		sm.peerStates[p] = &peerSyncState{
			syncCandidate:   true,
			requestedTxns:   make(map[chainhash.Hash]struct{}),
			requestedBlocks: make(map[chainhash.Hash]struct{}),
		}
		sm.headerSyncAddPeer(p)
	}
	require.Len(t, hs.peers, maxHeaderSyncPeers)
}

// containsPeer reports whether the given peer is present in the slice.
func containsPeer(slice []*peer.Peer, target *peer.Peer) bool {
	for _, p := range slice {
		if p == target {
			return true
		}
	}
	return false
}

// TestParallelBlockDownloadDisjoint verifies that the parallel block download
// hands disjoint slices of the header chain to multiple participating peers
// via the globally deduplicated requestedBlocks map.  A small request size is
// simulated by pre-claiming a front slice on one peer so blkDownload tops up
// the other peer with the disjoint tail.
func TestParallelBlockDownloadDisjoint(t *testing.T) {
	params := chaincfg.RegressionNetParams
	params.Checkpoints = nil

	sm, tearDown := makeMockSyncManager(t, &params)
	defer tearDown()

	// Install headers for 20 blocks while the block chain stays at genesis.
	// Headers are applied with BFNoPoWCheck to mirror the IBD header path so
	// the test does not depend on generating PoW-valid blocks.
	const numBlocks = 20
	for _, hdr := range makeTestHeaderChain(t, &params, numBlocks) {
		_, err := sm.chain.ProcessBlockHeader(hdr, blockchain.BFNoPoWCheck, false)
		require.NoError(t, err)
	}
	_, bestHeaderHeight := sm.chain.BestHeader()
	require.Equal(t, int32(numBlocks), bestHeaderHeight)

	hashAt := func(h int32) chainhash.Hash {
		hash, err := sm.chain.HeaderHashByHeight(h)
		require.NoError(t, err)
		return *hash
	}

	// Register two participating peers and start a parallel block download.
	peers := newHeaderSyncPeers(t, sm, 2, numBlocks)
	p0, p1 := peers[0], peers[1]
	sm.syncPeer = p0
	sm.ibdMode = true

	// Simulate p0 already being in-flight with the first 11 blocks (at or
	// above the minInFlightBlocks floor) so blkDownload tops up the other
	// peer instead of p0.
	state0 := sm.peerStates[p0]
	for h := int32(1); h <= 11; h++ {
		hash := hashAt(h)
		sm.requestedBlocks[hash] = struct{}{}
		state0.requestedBlocks[hash] = struct{}{}
	}
	sm.blockSync = []*peer.Peer{p0, p1}

	sm.blkDownload()

	// p1 must have claimed the remaining, disjoint blocks [12..20].
	state1 := sm.peerStates[p1]
	require.Equal(t, 9, len(state1.requestedBlocks))
	for h := int32(12); h <= numBlocks; h++ {
		hash := hashAt(h)
		require.Contains(t, state1.requestedBlocks, hash)
		require.Contains(t, sm.requestedBlocks, hash)
	}
	// No overlap: p0 still owns its front slice.
	for h := int32(1); h <= 11; h++ {
		require.Contains(t, state0.requestedBlocks, hashAt(h))
		require.NotContains(t, state1.requestedBlocks, hashAt(h))
	}
}

// TestParallelBlockDownloadDropPeer verifies that when a participating peer
// disconnects mid-download it is removed from blockSync and its in-flight
// blocks are freed back to the global requestedBlocks pool, so a remaining
// peer re-claims them on the next top-up.
func TestParallelBlockDownloadDropPeer(t *testing.T) {
	params := chaincfg.RegressionNetParams
	params.Checkpoints = nil

	sm, tearDown := makeMockSyncManager(t, &params)
	defer tearDown()

	const numBlocks = 20
	for _, hdr := range makeTestHeaderChain(t, &params, numBlocks) {
		_, err := sm.chain.ProcessBlockHeader(hdr, blockchain.BFNoPoWCheck, false)
		require.NoError(t, err)
	}

	hashAt := func(h int32) chainhash.Hash {
		hash, err := sm.chain.HeaderHashByHeight(h)
		require.NoError(t, err)
		return *hash
	}

	peers := newHeaderSyncPeers(t, sm, 2, numBlocks)
	p0, p1 := peers[0], peers[1]

	// p0 is a participating (non-sync) peer holding the front 11 blocks; p1
	// is the designated sync peer so dropping p0 does not restart startSync.
	sm.syncPeer = p1
	sm.ibdMode = true
	state0 := sm.peerStates[p0]
	for h := int32(1); h <= 11; h++ {
		hash := hashAt(h)
		sm.requestedBlocks[hash] = struct{}{}
		state0.requestedBlocks[hash] = struct{}{}
	}
	sm.blockSync = []*peer.Peer{p0, p1}

	// p0 disconnects: drop it from blockSync and free its in-flight blocks.
	sm.handleDonePeerMsg(p0)

	require.False(t, containsPeer(sm.blockSync, p0))
	require.True(t, containsPeer(sm.blockSync, p1))
	require.Empty(t, sm.requestedBlocks,
		"dropped peer's blocks should be freed back to the global pool")

	// Topping up re-claims all the recently freed blocks from the remaining
	// peer.
	sm.blkDownload()
	state1 := sm.peerStates[p1]
	require.Len(t, state1.requestedBlocks, numBlocks)
	require.Len(t, sm.requestedBlocks, numBlocks)
}

// TestBlockDownloadResumeAfterRestart verifies that a block download resumes
// after a restart: the furthest stored block is persisted, reconnectStoredBlocks
// leaves a fully-connected chain untouched, and buildBlockRequest continues from
// the persisted cursor instead of re-requesting blocks that are already present.
func TestBlockDownloadResumeAfterRestart(t *testing.T) {
	params := chaincfg.RegressionNetParams
	params.Checkpoints = nil

	newChain := func(db database.DB) (*blockchain.BlockChain, error) {
		return blockchain.New(&blockchain.Config{
			DB:           db,
			Checkpoints:  params.Checkpoints,
			ChainParams:  &params,
			TimeSource:   blockchain.NewMedianTime(),
			SigCache:     txscript.NewSigCache(1000),
			HeaderWindow: 5,
		})
	}
	newSM := func(chain *blockchain.BlockChain) (*SyncManager, error) {
		return New(&Config{
			PeerNotifier: noopPeerNotifier{},
			Chain:        chain,
			ChainParams:  &params,
		})
	}

	// Session 1: download headers 1..20 and connect blocks 1..10.  The best
	// header chain is 10 blocks ahead of the best chain, exactly the state a
	// restart would interrupt.
	dbPath := filepath.Join(t.TempDir(), "ffldb")
	db1, err := database.Create("ffldb", dbPath, params.Net)
	require.NoError(t, err)
	chain1, err := newChain(db1)
	require.NoError(t, err)
	sm1, err := newSM(chain1)
	require.NoError(t, err)
	_ = sm1

	blocks := generateTestBlocks(t, &params, 20)
	for _, blk := range blocks {
		_, err := chain1.ProcessBlockHeader(&blk.MsgBlock().Header,
			blockchain.BFNoPoWCheck, false)
		require.NoError(t, err)
	}
	for _, blk := range blocks[:10] {
		_, _, err := chain1.ProcessBlock(blk, blockchain.BFNoPoWCheck)
		require.NoError(t, err)
	}
	_, bestDownloadHeight := chain1.BestDownloadState()
	require.Equal(t, int32(10), bestDownloadHeight,
		"download cursor should track the highest connected block")
	db1.Close()

	// Session 2: reopen the database and resume the download.
	db2, err := database.Open("ffldb", dbPath, params.Net)
	require.NoError(t, err)
	chain2, err := newChain(db2)
	require.NoError(t, err)
	sm2, err := newSM(chain2)
	require.NoError(t, err)

	_, bestHeaderHeight := chain2.BestHeader()
	require.Equal(t, int32(20), bestHeaderHeight)
	require.Equal(t, int32(10), chain2.BestSnapshot().Height)

	// The download cursor survives the restart.
	_, bestDownloadHeight = chain2.BestDownloadState()
	require.Equal(t, int32(10), bestDownloadHeight)

	// Nothing is stored above the connected tip, so the resume pump is a
	// no-op and the chain stays put.
	sm2.reconnectStoredBlocks()
	require.Equal(t, int32(10), chain2.BestSnapshot().Height)

	// The next getdata request resumes at cursor+1 (height 11) rather than
	// re-requesting the already-present blocks 1..10.
	p := peer.NewInboundPeer(&peer.Config{})
	p.UpdateLastBlockHeight(20)
	sm2.peerStates[p] = &peerSyncState{
		requestedTxns:   make(map[chainhash.Hash]struct{}),
		requestedBlocks: make(map[chainhash.Hash]struct{}),
	}
	gdmsg := sm2.buildBlockRequest(p)
	require.Len(t, gdmsg.InvList, 10,
		"resumed request should cover exactly blocks 11..20")
	for i, iv := range gdmsg.InvList {
		want, err := chain2.HeaderHashByHeight(int32(11 + i))
		require.NoError(t, err)
		require.Equal(t, *want, iv.Hash, "requested block at height %d", 11+i)
	}

	// Now drive the actual block-arrival pipeline for the remaining blocks
	// 11..20 and confirm the download resumes all the way to the tip with no
	// gaps, exactly as a real peer would deliver them after the restart.
	syncPeer := newSyncCandidate(t, sm2, 20)
	peerState := sm2.peerStates[syncPeer]
	sm2.syncPeer = syncPeer
	sm2.ibdMode = true
	sm2.blockSync = []*peer.Peer{syncPeer}

	for _, blk := range blocks[10:] {
		hash := blk.Hash()
		sm2.requestedBlocks[*hash] = struct{}{}
		peerState.requestedBlocks[*hash] = struct{}{}

		sm2.handleBlockMsg(&blockMsg{
			block: blk,
			peer:  syncPeer,
			reply: make(chan struct{}, 1),
		})
	}

	assertIBDComplete(t, sm2, peerState, 20)
	require.Equal(t, int32(20),
		sm2.chain.BestSnapshot().Height,
		"resumed download should connect every block up to the tip")
	_, cursorHeight := sm2.chain.BestDownloadState()
	require.Equal(t, int32(20), cursorHeight,
		"download cursor should mark every block connected")
	db2.Close()
}

// TestFullSyncAllBlocks verifies an entire chain is synced end-to-end through
// the real block-arrival pipeline (handleBlockMsg -> checkHeadersList ->
// ProcessBlock) while the in-memory header window is smaller than the chain,
// forcing the eviction / cold-read frontier path to run as blocks connect.
// Every block must reach the best chain, the download cursor must land on the
// tip, and IBD must complete.
func TestFullSyncAllBlocks(t *testing.T) {
	params := chaincfg.RegressionNetParams
	params.Checkpoints = nil

	// Enough blocks for many flushes and window evictions at window size 5.
	const numBlocks = 80

	newChain := func(db database.DB) (*blockchain.BlockChain, error) {
		return blockchain.New(&blockchain.Config{
			DB:           db,
			Checkpoints:  params.Checkpoints,
			ChainParams:  &params,
			TimeSource:   blockchain.NewMedianTime(),
			SigCache:     txscript.NewSigCache(1000),
			HeaderWindow: 5,
		})
	}
	newSM := func(chain *blockchain.BlockChain) (*SyncManager, error) {
		return New(&Config{
			PeerNotifier: noopPeerNotifier{},
			Chain:        chain,
			ChainParams:  &params,
		})
	}

	dbPath := filepath.Join(t.TempDir(), "ffldb")
	db, err := database.Create("ffldb", dbPath, params.Net)
	require.NoError(t, err)
	defer db.Close()

	chain, err := newChain(db)
	require.NoError(t, err)
	sm, err := newSM(chain)
	require.NoError(t, err)

	// Header IBD over the entire range.
	blocks := generateTestBlocks(t, &params, numBlocks)
	for _, blk := range blocks {
		_, err := chain.ProcessBlockHeader(&blk.MsgBlock().Header,
			blockchain.BFNoPoWCheck, false)
		require.NoError(t, err)
	}
	_, bestHeaderHeight := chain.BestHeader()
	require.Equal(t, int32(numBlocks), bestHeaderHeight)

	// Register a download peer and feed every block through the real
	// arrival pipeline.
	syncPeer := newSyncCandidate(t, sm, numBlocks)
	peerState := sm.peerStates[syncPeer]
	sm.syncPeer = syncPeer
	sm.ibdMode = true
	sm.blockSync = []*peer.Peer{syncPeer}

	for i, blk := range blocks {
		hash := blk.Hash()
		sm.requestedBlocks[*hash] = struct{}{}
		peerState.requestedBlocks[*hash] = struct{}{}

		sm.handleBlockMsg(&blockMsg{
			block: blk,
			peer:  syncPeer,
			reply: make(chan struct{}, 1),
		})

		if i < len(blocks)-1 {
			require.True(t, sm.ibdMode,
				"ibdMode should still be on at block %d", i+1)
		}
	}

	// The whole chain is connected, IBD has finished, nothing is left
	// in-flight, and the cursor is at the tip.
	assertIBDComplete(t, sm, peerState, numBlocks)
	_, cursorHeight := sm.chain.BestDownloadState()
	require.Equal(t, int32(numBlocks), cursorHeight,
		"download cursor should land exactly on the tip")

	// Every single block must be stored (each was accepted), even the ones
	// long since evicted from the in-memory window.
	snapshot := sm.chain.BestSnapshot()
	require.Equal(t, int32(numBlocks), snapshot.Height)
	for _, blk := range blocks {
		have, err := sm.chain.HaveBlock(blk.Hash())
		require.NoError(t, err)
		require.True(t, have,
			"block %v should be stored after full sync", blk.Hash())
	}

	// Reopening a window-less chain on the same DB must reproduce the full
	// connected chain to prove nothing was silently dropped during the
	// windowed sync.
	db.Close()
	db2, err := database.Open("ffldb", dbPath, params.Net)
	require.NoError(t, err)
	defer db2.Close()
	chain2, err := blockchain.New(&blockchain.Config{
		DB:          db2,
		Checkpoints: params.Checkpoints,
		ChainParams: &params,
		TimeSource:  blockchain.NewMedianTime(),
		SigCache:    txscript.NewSigCache(1000),
	})
	require.NoError(t, err)
	best2 := chain2.BestSnapshot()
	require.Equal(t, int32(numBlocks), best2.Height,
		"reopened chain should reproduce the full synced height")
	// Walk the whole best chain backwards and confirm every height matches
	// the block we fed in.
	got := make(map[chainhash.Hash]bool, len(blocks))
	gotHash := best2.Hash
	for h := int32(numBlocks); h >= 1; h-- {
		got[gotHash] = true
		hb, err := chain2.FetchBlockByHash(&gotHash)
		require.NoError(t, err, "block at height %d must be resolvable", h)
		gotHash = hb.MsgBlock().Header.PrevBlock
	}
	for _, blk := range blocks {
		require.Contains(t, got, *blk.Hash(),
			"every fed block (%v) must appear on the reproduced best chain",
			blk.Hash())
	}
}

