// Copyright (c) 2026 The btcd developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Asher_Mod_Start_20260910_123842
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

	// A6 unified atomic metrics (see the project performance plan).
	// A6 统一原子指标(见项目性能方案)。
	BlocksPerSec     float64 `json:"blocks_per_sec"`
	ChainLockWaitMs  int64   `json:"chain_lock_wait_ms"`
	UtxoFlushLastMs  int64   `json:"utxo_flush_last_ms"`
	UtxoFlushCount   int64   `json:"utxo_flush_count"`
	MsgQueueDepth    int     `json:"msg_queue_depth"`
	LogBytes         int64   `json:"log_bytes"`

	// O8 metrics: how often HeaderHashByHeight resolved from the in-memory
	// header window vs the DB cold-read path.  The counters cover every
	// HeaderHashByHeight caller (prev check, locators, completion loop), not
	// only the receive-side prev check.  A rising cold-read count means
	// header lookups fall out of the window and hit disk.
	// O8 指标:HeaderHashByHeight 从内存 header 窗口命中与走 DB 冷读的
	// 次数。计数覆盖所有 HeaderHashByHeight 调用方(prev 校验、locator、
	// 完成循环),不只接收端 prev 校验。冷读计数上升意味着 header 查询
	// 逐出窗口、落盘。
	HeaderWindowHits uint64 `json:"header_window_hits"`
	HeaderColdReads  uint64 `json:"header_cold_reads"`
}

func init() {
	flags := UsageFlag(0)
	MustRegisterCmd("getblocksyncstatus", (*GetBlockSyncStatusCmd)(nil), flags)
}
// Asher_Mod_End_20260910_123842
