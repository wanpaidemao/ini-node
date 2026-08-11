// Copyright (c) 2026 The btcd developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package btcjson

// GetBlockSyncStatusCmd defines the getblocksyncstatus JSON-RPC command.
type GetBlockSyncStatusCmd struct{}

// NewGetBlockSyncStatusCmd returns a new instance which can be used to issue a
// getblocksyncstatus JSON-RPC command.
func NewGetBlockSyncStatusCmd() *GetBlockSyncStatusCmd {
	return &GetBlockSyncStatusCmd{}
}

// PeerSyncStatusResult models one peer's role in an in-progress parallel
// initial download, as returned by the getblocksyncstatus command.
type PeerSyncStatusResult struct {
	ID            int32  `json:"id"`
	Addr          string `json:"addr"`
	SyncNode      bool   `json:"sync_node"`
	SyncCandidate bool   `json:"sync_candidate"`
	CurrentHeight int32  `json:"current_height"`
	SliceStart    int32  `json:"slice_start"`
	SliceEnd      int32  `json:"slice_end"`
	SliceAssignedAt int64 `json:"slice_assigned_at"`
	SliceReceived int32  `json:"slice_received"`
	HeaderRangeStart      int32 `json:"header_range_start"`
	HeaderRangeEnd        int32 `json:"header_range_end"`
	HeaderRangeReceived   bool  `json:"header_range_received"`
	HeaderRangeAssignedAt int64 `json:"header_range_assigned_at"`
	InFlightBlocks int   `json:"in_flight_blocks"`
	LastBlockAt    int64 `json:"last_block_at"`
}

// HeaderRecentRangeResult models one recently completed parallel header
// download window (the contiguous [start, end) range a single peer fetched).
type HeaderRecentRangeResult struct {
	Start      int32  `json:"start"`
	End        int32  `json:"end"`
	Peer       string `json:"peer"`
	AssignedAt int64  `json:"assigned_at"`
}

// GetBlockSyncStatusResult models the data returned from the
// getblocksyncstatus command.
type GetBlockSyncStatusResult struct {
	Current          bool                  `json:"current"`
	IBD              bool                  `json:"ibd"`
	BestChainHeight  int32                 `json:"best_chain_height"`
	HeaderTip        int32                 `json:"header_tip"`
	HeaderTarget     int32                 `json:"header_target"`
	HeaderNextAssign int32                 `json:"header_next_assign"`
	HeaderSliceLen   int32                 `json:"header_slice_len"`
	HeaderRecentRanges []HeaderRecentRangeResult `json:"header_recent_ranges"`
	BlockTarget      int32                 `json:"block_target"`
	BlockNextAssign  int32                 `json:"block_next_assign"`
	BlockWindow      int32                 `json:"block_window"`
	Peers            []PeerSyncStatusResult `json:"peers"`
}

func init() {
	flags := UsageFlag(0)
	MustRegisterCmd("getblocksyncstatus", (*GetBlockSyncStatusCmd)(nil), flags)
}
