// Copyright (c) 2023 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Asher_Mod_Start_20260910_142235
// Asher_Mod_Start_20260910_131359
// Asher_Mod_Start_20260910_123842
package blockchain

import (
	"fmt"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/database"
	"github.com/btcsuite/btcd/txscript/v2"
	"github.com/btcsuite/btcd/wire/v2"
)

// mapSlice is a slice of maps for utxo entries.  The slice of maps are needed to
// guarantee that the map will only take up N amount of bytes.  As of v1.20, the
// go runtime will allocate 2^N + few extra buckets, meaning that for large N, we'll
// allocate a lot of extra memory if the amount of entries goes over the previously
// allocated buckets.  A slice of maps allows us to have a better control of how much
// total memory gets allocated by all the maps.
type mapSlice struct {
	// mtx protects against concurrent access for the map slice.
	mtx sync.Mutex

	// maps are the underlying maps in the slice of maps.
	maps []map[wire.OutPoint]*UtxoEntry

	// maxEntries is the maximum amount of elements that the map is allocated for.
	maxEntries []int

	// maxTotalMemoryUsage is the maximum memory usage in bytes that the state
	// should contain in normal circumstances.
	maxTotalMemoryUsage uint64
}

// length returns the length of all the maps in the map slice added together.
//
// This function is safe for concurrent access.
func (ms *mapSlice) length() int {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()

	var l int
	for _, m := range ms.maps {
		l += len(m)
	}

	return l
}

// size returns the size of all the maps in the map slice added together.
//
// This function is safe for concurrent access.
func (ms *mapSlice) size() int {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()

	var size int
	for _, num := range ms.maxEntries {
		size += calculateRoughMapSize(num, bucketSize)
	}

	return size
}

// get looks for the outpoint in all the maps in the map slice and returns
// the entry.  nil and false is returned if the outpoint is not found.
//
// This function is safe for concurrent access.
func (ms *mapSlice) get(op wire.OutPoint) (*UtxoEntry, bool) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()

	var entry *UtxoEntry
	var found bool

	for _, m := range ms.maps {
		entry, found = m[op]
		if found {
			return entry, found
		}
	}

	return nil, false
}

// put puts the outpoint and the entry into one of the maps in the map slice.  If the
// existing maps are all full, it will allocate a new map based on how much memory we
// have left over.  Leftover memory is calculated as:
// maxTotalMemoryUsage - (totalEntryMemory + mapSlice.size())
//
// This function is safe for concurrent access.
func (ms *mapSlice) put(op wire.OutPoint, entry *UtxoEntry, totalEntryMemory uint64) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()

	// Look for the key in the maps.
	for i := range ms.maxEntries {
		m := ms.maps[i]
		_, found := m[op]
		if found {
			// If the key is found, overwrite it.
			m[op] = entry
			return // Return as we were successful in adding the entry.
		}
	}

	for i, maxNum := range ms.maxEntries {
		m := ms.maps[i]
		if len(m) >= maxNum {
			// Don't try to insert if the map already at max since
			// that'll force the map to allocate double the memory it's
			// currently taking up.
			continue
		}

		m[op] = entry
		return // Return as we were successful in adding the entry.
	}

	// We only reach this code if we've failed to insert into the map above as
	// all the current maps were full.  We thus make a new map and insert into
	// it.
	m := ms.makeNewMap(totalEntryMemory)
	m[op] = entry
}

// delete attempts to delete the given outpoint in all of the maps. No-op if the
// outpoint doesn't exist.
//
// This function is safe for concurrent access.
func (ms *mapSlice) delete(op wire.OutPoint) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()

	for i := 0; i < len(ms.maps); i++ {
		delete(ms.maps[i], op)
	}
}

// makeNewMap makes and appends the new map into the map slice.
//
// This function is NOT safe for concurrent access and must be called with the
// lock held.
func (ms *mapSlice) makeNewMap(totalEntryMemory uint64) map[wire.OutPoint]*UtxoEntry {
	// Get the size of the leftover memory.  Compute in signed space and clamp to
	// zero so a saturated cache (entries + existing maps already at the cap)
	// never underflows uint64 into a gigantic allocation.
	// 计算剩余内存。用有符号计算并 clamp 到 0,防止缓存饱和
	// (条目+既有 map 已达上限)时 uint64 下溢为超大分配。
	leftover := int64(ms.maxTotalMemoryUsage) - int64(totalEntryMemory)
	for _, maxNum := range ms.maxEntries {
		leftover -= int64(calculateRoughMapSize(maxNum, bucketSize))
	}
	memSize := uint64(0)
	if leftover > 0 {
		memSize = uint64(leftover)
	}

	// Get a new map that's sized to house inside the leftover memory.
	// -1 on the returned value will make the map allocate half as much total
	// bytes.  This is done to make sure there's still room left for utxo
	// entries to take up.
	numMaxElements := calculateMinEntries(int(memSize), bucketSize+avgEntrySize)
	numMaxElements -= 1
	ms.maxEntries = append(ms.maxEntries, numMaxElements)
	ms.maps = append(ms.maps, make(map[wire.OutPoint]*UtxoEntry, numMaxElements))

	return ms.maps[len(ms.maps)-1]
}

// deleteMaps deletes all maps except for the first one which should be the biggest.
//
// This function is safe for concurrent access.
func (ms *mapSlice) deleteMaps() {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()

	size := ms.maxEntries[0]
	ms.maxEntries = []int{size}
	ms.maps = ms.maps[:1]
}

const (
	// utxoFlushPeriodicInterval is the interval at which a flush is performed
	// when the flush mode FlushPeriodic is used.  This is used when the initial
	// block download is complete and it's useful to flush periodically in case
	// of unforeseen shutdowns.
	utxoFlushPeriodicInterval = time.Minute * 5
)

// FlushMode is used to indicate the different urgency types for a flush.
type FlushMode uint8

const (
	// FlushRequired is the flush mode that means a flush must be performed
	// regardless of the cache state.  For example right before shutting down.
	FlushRequired FlushMode = iota

	// FlushPeriodic is the flush mode that means a flush can be performed
	// when it would be almost needed.  This is used to periodically signal when
	// no I/O heavy operations are expected soon, so there is time to flush.
	FlushPeriodic

	// FlushIfNeeded is the flush mode that means a flush must be performed only
	// if the cache is exceeding a safety threshold very close to its maximum
	// size.  This is used mostly internally in between operations that can
	// increase the cache size.
	FlushIfNeeded
)

// utxoCache is a cached utxo view in the chainstate of a BlockChain.
// utxoHotDepth is how many blocks of UTXO entries stay resident across a
// full cache flush (A4-3 hot region).  Entries whose block height is above
// bestTip.Height-utxoHotDepth are the "hot" outputs most likely to be spent
// by the next blocks; keeping them cached avoids falling through to the
// database right after a full flush.
// utxoHotDepth 是全量 flush 时仍驻留内存的 UTXO 条目块数(A4-3 热区)。
// 高度高于 bestTip.Height-utxoHotDepth 的条目即"热"输出,最可能被后续
// 块花费;全量 flush 后保留它们可避免紧接着的数据库冷读。
const utxoHotDepth int32 = 2000

// utxoDelta records one changed outpoint to be persisted by the A3 batch
// transaction (full 方案1).  A tombstone deletes the row; otherwise takeDelta
// copies the entry value out of the live cache under the chain lock, so the
// async writer never races concurrent cache mutations.
// utxoDelta 记录一个待由 A3 批次事务落盘的变更 outpoint(完整方案1)。
// tombstone 删除行;否则 takeDelta 在链锁内从活动缓存复制条目值,异步
// writer 不会与并发缓存修改竞争。
type utxoDelta struct {
	op        wire.OutPoint
	tombstone bool
	entry     *UtxoEntry // 仅 !tombstone 且经 takeDelta 复制后非 nil
}

// utxoCache houses a cache of unspent transaction outputs to be used for
// various purposes and gives concurrent access to the cache for callers.
type utxoCache struct {
	db database.DB

	// maxTotalMemoryUsage is the maximum memory usage in bytes that the state
	// should contain in normal circumstances.
	maxTotalMemoryUsage uint64

	// cachedEntries keeps the internal cache of the utxo state.  The tfModified
	// flag indicates that the state of the entry (potentially) deviates from the
	// state in the database.  Explicit nil values in the map are used to
	// indicate that the database does not contain the entry.
	cachedEntries    mapSlice
	totalEntryMemory uint64 // Total memory usage in bytes.

	// dirtyOps tracks the outpoints whose cached entry deviates from the
	// database (fresh new outputs, spent or nil entries waiting for a delete).
	// It is maintained in lockstep with cachedEntries under the chain lock so
	// an incremental flush (writeCache with cleanCache=false) only iterates the
	// actually-changed outpoints instead of scanning the whole cache.  It is
	// cleared together with the cache on a full flush and on purge.
	// dirtyOps 记录缓存条目与数据库不一致的 outpoint(新增输出、待删除的
	// spent/nil 条目)。它与 cachedEntries 在链锁内同步维护,使增量 flush
	// (writeCache cleanCache=false)只遍历真正变化的 outpoint,而非扫描整个
	// 缓存。全量 flush 与 purge 时随缓存一起清空。
	dirtyOps map[wire.OutPoint]struct{}

	// Asher_Mod_Start_20260911_173000
	// trackDeltas enables per-block UTXO delta capture for the A3 batch
	// (full 方案1).  When true, addTxIn/addTxOut register the touched outpoints
	// in deltaPoints and takeDelta snapshots them into the current writeItem,
	// so the batch transaction persists UTXO entries + consistency marker
	// together with the metadata and watermark.  It is enabled when the async
	// write queue is active (A3-on) and stays off for the synchronous/prune
	// path.
	// trackDeltas 启用逐块 UTXO 增量捕获(完整方案1/A3)。为 true 时
	// addTxIn/addTxOut 把触及的 outpoint 记入 deltaPoints,takeDelta 把它们
	// 快照进当前 writeItem,使批次事务把 UTXO 条目+一致性标记与元数据、
	// 水印一起落盘。异步写队列启用(A3-on)时打开;同步/prune 路径保持关闭。
	trackDeltas bool

	// deltaPoints is the per-block list of outpoints whose cache state changed
	// while applying the current block (under the chain lock).  It is consumed
	// and cleared by takeDelta once per connected block under A3.
	// deltaPoints 是当前块应用期间(链锁内)缓存状态发生变化的 outpoint
	// 列表。A3 下每连接一块,由 takeDelta 消费并清空一次。
	deltaPoints []utxoDelta
	// Asher_Mod_End_20260911_173000

	// hotFloor is the lowest block height whose UTXO entries are considered
	// "hot" (A4-3 hot region).  A full cache flush keeps entries at or above
	// hotFloor resident so the next blocks' lookups hit memory instead of the
	// database right after the flush.  It is advanced by the caller (the
	// connect path) to bestTip.Height-utxoHotDepth under the chain lock.
	// hotFloor 是"热"UTXO 条目的最低块高(A4-3 热区)。全量 flush 保留高度
	// ≥ hotFloor 的条目,使后续块的查询在 flush 后直接命中内存而非数据库。
	// 由调用方(连接路径)在链锁内推进为 bestTip.Height-utxoHotDepth。
	hotFloor int32

	// Below fields are used to indicate when the last flush happened.
	lastFlushHash chainhash.Hash
	lastFlushTime time.Time
}

// newUtxoCache initiates a new utxo cache instance with its memory usage limited
// to the given maximum.
func newUtxoCache(db database.DB, maxTotalMemoryUsage uint64) *utxoCache {
	// While the entry isn't included in the map size, add the average size to the
	// bucket size so we get some leftover space for entries to take up.
	numMaxElements := calculateMinEntries(int(maxTotalMemoryUsage), bucketSize+avgEntrySize)
	numMaxElements -= 1

	log.Infof("Pre-allocating for %d MiB", maxTotalMemoryUsage/(1024*1024)+1)

	m := make(map[wire.OutPoint]*UtxoEntry, numMaxElements)

	return &utxoCache{
		db:                  db,
		maxTotalMemoryUsage: maxTotalMemoryUsage,
		cachedEntries: mapSlice{
			maps:                []map[wire.OutPoint]*UtxoEntry{m},
			maxEntries:          []int{numMaxElements},
			maxTotalMemoryUsage: maxTotalMemoryUsage,
		},
		dirtyOps: make(map[wire.OutPoint]struct{}),
	}
}

// totalMemoryUsage returns the total memory usage in bytes of the UTXO cache.
func (s *utxoCache) totalMemoryUsage() uint64 {
	// Total memory is the map size + the size that the utxo entries are
	// taking up.
	size := uint64(s.cachedEntries.size())
	size += s.totalEntryMemory

	return size
}

// fetchEntries returns the UTXO entries for the given outpoints.  The function always
// returns as many entries as there are outpoints and the returns entries are in the
// same order as the outpoints.  It returns nil if there is no entry for the outpoint
// in the UTXO set.
//
// The returned entries are NOT safe for concurrent access.
func (s *utxoCache) fetchEntries(outpoints []wire.OutPoint) ([]*UtxoEntry, error) {
	entries := make([]*UtxoEntry, len(outpoints))
	var (
		missingOps    []wire.OutPoint
		missingOpsIdx []int
	)
	for i := range outpoints {
		if entry, ok := s.cachedEntries.get(outpoints[i]); ok {
			entries[i] = entry
			continue
		}

		// At this point, we have missing outpoints.  Allocate them now
		// so that we never allocate if the cache never misses.
		if len(missingOps) == 0 {
			missingOps = make([]wire.OutPoint, 0, len(outpoints))
			missingOpsIdx = make([]int, 0, len(outpoints))
		}

		missingOpsIdx = append(missingOpsIdx, i)
		missingOps = append(missingOps, outpoints[i])
	}

	// Return early and don't attempt access the database if we don't have any
	// missing outpoints.
	if len(missingOps) == 0 {
		return entries, nil
	}

	// Fetch the missing outpoints in the cache from the database.
	dbEntries := make([]*UtxoEntry, len(missingOps))
	err := s.db.View(func(dbTx database.Tx) error {
		utxoBucket := dbTx.Metadata().Bucket(utxoSetBucketName)

		for i := range missingOps {
			entry, err := dbFetchUtxoEntry(dbTx, utxoBucket, missingOps[i])
			if err != nil {
				return err
			}

			dbEntries[i] = entry
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	// Add each of the entries to the UTXO cache and update their memory
	// usage.
	//
	// NOTE: When the fetched entry is nil, it is still added to the cache
	// as a miss; this prevents future lookups to perform the same database
	// fetch.
	for i := range dbEntries {
		s.cachedEntries.put(missingOps[i], dbEntries[i], s.totalEntryMemory)
		s.totalEntryMemory += dbEntries[i].memoryUsage()
	}

	// Fill in the entries with the ones fetched from the database.
	for i := range missingOpsIdx {
		entries[missingOpsIdx[i]] = dbEntries[i]
	}

	return entries, nil
}

// prefetchInputs warms the UTXO cache with the distinct inputs referenced by
// the given block (A4-3 prefetch queue).  The block bodies are already on
// disk by the time the block is about to be connected, so resolving their
// inputs through fetchEntries brings the entries into the cache before the
// connect path's fetchInputUtxos runs -- turning what would be a chain-lock
// database read into an in-memory hit.  It is best-effort and read-only with
// respect to the chain state: errors are ignored (the connect path fetches
// on demand anyway) and entries are not registered as dirty.
//
// This function MUST be called with the chain state lock held (read or write).
// prefetchInputs 用即将连接块的去重输入预热 UTXO 缓存(A4-3 预取队列)。
// 块体在块连接前已落盘,通过 fetchEntries 解析其输入可在连接路径的
// fetchInputUtxos 之前把条目带入缓存——把链锁内的数据库读变成内存命中。
// 尽力而为、对链状态只读:错误被忽略(连接路径本就会按需读取),条目
// 不登记为脏。
//
// 调用方必须持有链状态锁(读或写)。
func (s *utxoCache) prefetchInputs(block *btcutil.Block) {
	// Collect the distinct input outpoints referenced by the block.  The
	// coinbase input is skipped since its previous outpoint is the zero
	// outpoint, which never exists in the UTXO set.
	// 收集块引用的去重输入 outpoint。跳过 coinbase 输入,其 prevout 是
	// 零值 outpoint,UTXO 集中必然不存在。
	needed := make([]wire.OutPoint, 0, 64)
	seen := make(map[wire.OutPoint]struct{})
	for _, tx := range block.Transactions() {
		if IsCoinBase(tx) {
			continue
		}
		for _, txIn := range tx.MsgTx().TxIn {
			op := txIn.PreviousOutPoint
			if _, ok := seen[op]; ok {
				continue
			}
			seen[op] = struct{}{}
			needed = append(needed, op)
		}
	}
	if len(needed) == 0 {
		return
	}
	// Best-effort warm-up: a DB error here just means the connect path will
	// perform the normal on-demand fetch later.
	// 尽力预热:这里的 DB 错误只会让连接路径之后执行正常的按需读取。
	_, _ = s.fetchEntries(needed)
}

// addTxOut adds the specified output to the cache if it is not provably
// unspendable.  When the cache already has an entry for the output, it will be
// overwritten with the given output.  All fields will be updated for existing
// entries since it's possible it has changed during a reorg.
func (s *utxoCache) addTxOut(outpoint wire.OutPoint, txOut *wire.TxOut, isCoinBase bool,
	blockHeight int32) error {

	// Don't add provably unspendable outputs.
	if txscript.IsUnspendable(txOut.PkScript) {
		return nil
	}

	entry := new(UtxoEntry)
	entry.amount = txOut.Value

	// Deep copy the script when the script in the entry differs from the one in
	// the txout.  This is required since the txout script is a subslice of the
	// overall contiguous buffer that the msg tx houses for all scripts within
	// the tx.  It is deep copied here since this entry may be added to the utxo
	// cache, and we don't want the utxo cache holding the entry to prevent all
	// of the other tx scripts from getting garbage collected.
	entry.pkScript = make([]byte, len(txOut.PkScript))
	copy(entry.pkScript, txOut.PkScript)

	entry.blockHeight = blockHeight
	entry.packedFlags = tfFresh | tfModified
	if isCoinBase {
		entry.packedFlags |= tfCoinBase
	}

	s.cachedEntries.put(outpoint, entry, s.totalEntryMemory)
	s.totalEntryMemory += entry.memoryUsage()
	s.dirtyOps[outpoint] = struct{}{}
	// Asher_Mod_Start_20260911_173000
	// Register the new/unspent output in the per-block delta list (full 方案1);
	// takeDelta copies its value into the batch item at connect time.
	// 把新增/恢复的输出登记进逐块增量列表(完整方案1);takeDelta 在连接时
	// 把值复制进批次 item。
	if s.trackDeltas {
		s.deltaPoints = append(s.deltaPoints, utxoDelta{op: outpoint})
	}
	// Asher_Mod_End_20260911_173000

	return nil
}

// addTxOuts adds all outputs in the passed transaction which are not provably
// unspendable to the view.  When the view already has entries for any of the
// outputs, they are simply marked unspent.  All fields will be updated for
// existing entries since it's possible it has changed during a reorg.
func (s *utxoCache) addTxOuts(tx *btcutil.Tx, blockHeight int32) error {
	// Loop all of the transaction outputs and add those which are not
	// provably unspendable.
	isCoinBase := IsCoinBase(tx)
	prevOut := wire.OutPoint{Hash: *tx.Hash()}
	for txOutIdx, txOut := range tx.MsgTx().TxOut {
		// Update existing entries.  All fields are updated because it's
		// possible (although extremely unlikely) that the existing
		// entry is being replaced by a different transaction with the
		// same hash.  This is allowed so long as the previous
		// transaction is fully spent.
		prevOut.Index = uint32(txOutIdx)
		err := s.addTxOut(prevOut, txOut, isCoinBase, blockHeight)
		if err != nil {
			return err
		}
	}

	return nil
}

// addTxIn will add the given input to the cache if the previous outpoint the txin
// is pointing to exists in the utxo set.  The utxo that is being spent by the input
// will be marked as spent and if the utxo is fresh (meaning that the database on disk
// never saw it), it will be removed from the cache.
func (s *utxoCache) addTxIn(txIn *wire.TxIn, stxos *[]SpentTxOut) error {
	// Ensure the referenced utxo exists in the view.  This should
	// never happen unless there is a bug is introduced in the code.
	entries, err := s.fetchEntries([]wire.OutPoint{txIn.PreviousOutPoint})
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0] == nil {
		return AssertError(fmt.Sprintf("missing input %v",
			txIn.PreviousOutPoint))
	}

	// Only create the stxo details if requested.
	entry := entries[0]
	if stxos != nil {
		// Populate the stxo details using the utxo entry.
		stxo := SpentTxOut{
			Amount:     entry.Amount(),
			PkScript:   entry.PkScript(),
			Height:     entry.BlockHeight(),
			IsCoinBase: entry.IsCoinBase(),
		}

		*stxos = append(*stxos, stxo)
	}

	// Mark the entry as spent.
	entry.Spend()

	// Asher_Mod_Start_20260911_173000
	// Under A3 (full 方案1) every spend is recorded as a tombstone delta: the
	// row must be deleted by the batch.  This covers BOTH the fresh case (the
	// entry was created by an earlier still-pending block of the same batch
	// window and was captured as a put; the delete must follow in order) and
	// the non-fresh case.
	// A3 下(完整方案1)每次花费都记录为 tombstone 增量:该行必须由批次删除。
	// fresh 与非 fresh 两种情形都覆盖(fresh 条目可能已被本批次窗口内更早
	// 的挂起块捕获为 put,删除必须按顺序跟在 put 之后)。
	if s.trackDeltas {
		s.deltaPoints = append(s.deltaPoints, utxoDelta{op: txIn.PreviousOutPoint, tombstone: true})
	}
	// Asher_Mod_End_20260911_173000

	// If an entry is fresh it indicates that this entry was spent before it could be
	// flushed to the database. Because of this, we can just delete it from the map of
	// cached entries.
	if entry.isFresh() {
		// If the entry is fresh, we will always have it in the cache.  The
		// database never held a row for it, so nothing is dirty anymore.
		s.cachedEntries.delete(txIn.PreviousOutPoint)
		s.totalEntryMemory -= entry.memoryUsage()
		delete(s.dirtyOps, txIn.PreviousOutPoint)
	} else {
		// Can leave the entry to be garbage collected as the only purpose
		// of this entry now is so that the entry on disk can be deleted.  It
		// is now dirty: the next incremental flush must delete its row.
		entry = nil
		s.dirtyOps[txIn.PreviousOutPoint] = struct{}{}
	}

	return nil
}

// addTxIns will add the given inputs of the tx if it's not a coinbase tx and if
// the previous output that the input is pointing to exists in the utxo set.  The
// utxo that is being spent by the input will be marked as spent and if the utxo
// is fresh (meaning that the database on disk never saw it), it will be removed
// from the cache.
func (s *utxoCache) addTxIns(tx *btcutil.Tx, stxos *[]SpentTxOut) error {
	// Coinbase transactions don't have any inputs to spend.
	if IsCoinBase(tx) {
		return nil
	}

	for _, txIn := range tx.MsgTx().TxIn {
		err := s.addTxIn(txIn, stxos)
		if err != nil {
			return err
		}
	}

	return nil
}

// connectTransaction updates the cache by adding all new utxos created by the
// passed transaction and marking and/or removing all utxos that the transactions
// spend as spent.  In addition, when the 'stxos' argument is not nil, it will
// be updated to append an entry for each spent txout.  An error will be returned
// if the cache and the database does not contain the required utxos.
func (s *utxoCache) connectTransaction(
	tx *btcutil.Tx, blockHeight int32, stxos *[]SpentTxOut) error {

	err := s.addTxIns(tx, stxos)
	if err != nil {
		return err
	}

	// Add the transaction's outputs as available utxos.
	return s.addTxOuts(tx, blockHeight)
}

// connectTransactions updates the cache by adding all new utxos created by all
// of the transactions in the passed block, marking and/or removing all utxos
// the transactions spend as spent, and setting the best hash for the view to
// the passed block.  In addition, when the 'stxos' argument is not nil, it will
// be updated to append an entry for each spent txout.
func (s *utxoCache) connectTransactions(block *btcutil.Block, stxos *[]SpentTxOut) error {
	for _, tx := range block.Transactions() {
		err := s.connectTransaction(tx, block.Height(), stxos)
		if err != nil {
			return err
		}
	}

	return nil
}

// Asher_Mod_Start_20260911_173000
// takeDelta snapshots the per-block changed outpoints (full 方案1) into a
// delta list the A3 batch writer will persist.  It MUST be called under the
// chain lock immediately after the block's connectTransactions: put entries
// are copied by value from the live cache so later blocks may mutate them
// freely, tombstoned (spent) entries drop their spent row from the cache (its
// delete is recorded by the batch), and the working dirtyOps set is drained
// so re-write tracking stays aligned with the batch cadence instead of
// growing without bound.
// takeDelta 把逐块变化的 outpoint 快照成增量列表(完整方案1),由 A3 批次
// writer 落盘。必须在链锁内、本块 connectTransactions 之后立即调用:put
// 条目按值从活动缓存复制,后续块可自由修改;被花费(tombstone)的条目从
// 缓存移除其 spent 行(删除由批次记录);工作用 dirtyOps 集合随之清空,
// 使重写跟踪与批次节奏对齐而不会无限增长。
func (s *utxoCache) takeDelta() []utxoDelta {
	if !s.trackDeltas {
		return nil
	}
	if len(s.deltaPoints) == 0 {
		s.dirtyOps = make(map[wire.OutPoint]struct{})
		return nil
	}
	deltas := make([]utxoDelta, 0, len(s.deltaPoints))
	for _, d := range s.deltaPoints {
		if d.tombstone {
			if entry, ok := s.cachedEntries.get(d.op); ok {
				s.cachedEntries.delete(d.op)
				if entry != nil {
					s.totalEntryMemory -= entry.memoryUsage()
				}
			}
			deltas = append(deltas, utxoDelta{op: d.op, tombstone: true})
			continue
		}
		entry, ok := s.cachedEntries.get(d.op)
		if ok && entry != nil {
			c := *entry
			deltas = append(deltas, utxoDelta{op: d.op, entry: &c})
		} else {
			// The fresh entry was consumed (e.g. spent within the same block);
			// persist a delete so an earlier pending put cannot resurrect it.
			// fresh 条目已被消费(如本块内即被花费);记录删除,避免更早的
			// 挂起 put 把它复活。
			deltas = append(deltas, utxoDelta{op: d.op, tombstone: true})
		}
	}
	s.deltaPoints = nil
	s.dirtyOps = make(map[wire.OutPoint]struct{})
	return deltas
}

// resetDelta discards any per-block deltas accumulated without being consumed
// (e.g. by the UTXO reconstruction replay during startup), so they can never
// leak into the first connected block's batch item.
// resetDelta 丢弃未被消费而累积的逐块增量(如启动时 UTXO 重建重放产生的),
// 使它们不会泄漏进首个连接块的批次 item。
func (s *utxoCache) resetDelta() {
	s.deltaPoints = nil
}
// Asher_Mod_End_20260911_173000

// writeCache writes the entries that differ from the database to the database
// atomically.  When cleanCache is true the whole in-memory cache is written and
// then cleared (historical behavior); when false only the tracked dirty
// outpoints are written — their modified/fresh flags are cleared, nil/spent
// entries (whose deletes are now synced) are dropped from the cache, and the
// remaining entries stay resident.  Retaining the cache keeps subsequent UTXO
// reads in memory instead of falling through to the database after every flush,
// and the dirty-set iteration keeps each flush proportional to the number of
// changes instead of the cache size.
// writeCache 把与数据库不一致的条目原子写盘。cleanCache 为 true 时写整个
// 内存缓存并清空(历史行为);为 false 时只写被跟踪的脏 outpoint——清除其
// modified/fresh 标记,nil/spent 条目(删除已落盘)从缓存移除,其余条目驻留。
// 保留缓存使后续 UTXO 读取持续命中内存;脏集合迭代使每次 flush 的开销与
// 改动量成正比而非缓存大小。
func (s *utxoCache) writeCache(dbTx database.Tx, bestState *BestState, cleanCache bool) error {
	// Update commits and flushes the cache to the database.
	// NOTE: The database has its own cache which gets atomically written
	// to leveldb.
	utxoBucket := dbTx.Metadata().Bucket(utxoSetBucketName)

	if cleanCache {
		// Full flush: walk every cached entry, sync it to the database and
		// clear the cache afterwards.  Entries at or above the hot region
		// floor (A4-3) are written but kept resident: they are the outputs
		// most likely to be spent by the next blocks, so keeping them cached
		// avoids a database cold-read right after the flush.  The hot entries
		// survive in a fresh map slice so the maps below hotFloor are still
		// reclaimed and totalEntryMemory stays accurate.
		// 全量 flush:遍历每个缓存条目写盘,随后清空缓存。高度 ≥ 热区下界
		// (A4-3)的条目写盘后保留驻留:它们是后续块最可能花费的输出,保留
		// 可避免 flush 后立即的数据库冷读。热条目放入新的 map 分片,冷区
		// map 仍被回收,totalEntryMemory 保持准确。
		var hotEntries []wire.OutPoint
		for i := range s.cachedEntries.maps {
			for outpoint, entry := range s.cachedEntries.maps[i] {
				switch {
				// If the entry is nil or spent, remove it from the database.
				case entry == nil || entry.IsSpent():
					err := dbDeleteUtxoEntry(utxoBucket, outpoint)
					if err != nil {
						return err
					}

				// No need to update the entry if it was not modified.
				case !entry.isModified():

				default:
					// Entry is fresh and needs to be put into the database.
					err := dbPutUtxoEntry(utxoBucket, outpoint, entry)
					if err != nil {
						return err
					}
					entry.clearModified()
				}

				if entry != nil && !entry.IsSpent() &&
					entry.BlockHeight() >= s.hotFloor {
					hotEntries = append(hotEntries, outpoint)
					continue
				}
				delete(s.cachedEntries.maps[i], outpoint)
			}
		}
		s.cachedEntries.deleteMaps()
		s.totalEntryMemory = 0
		s.dirtyOps = make(map[wire.OutPoint]struct{})

		// Re-insert the hot region entries into a fresh map slice so their
		// memory is accounted for and later lookups hit the cache.  Note the
		// entries keep their (now-clean) modified flags: they were just
		// written to the database, so a later incremental flush skips them.
		// 把热区条目重新放入新的 map 分片,计入内存并供后续查询命中。
		// 条目已写盘且标志已清除,后续增量 flush 会跳过它们。
		for _, op := range hotEntries {
			entry, _ := s.cachedEntries.get(op)
			s.cachedEntries.put(op, entry, s.totalEntryMemory)
			s.totalEntryMemory += entry.memoryUsage()
		}
	} else {
		// Incremental flush: only the outpoints registered in dirtyOps deviate
		// from the database.  Entries that disappeared from the cache or are no
		// longer modified are skipped defensively.
		// 增量 flush:只有 dirtyOps 中登记的 outpoint 与数据库不一致。
		// 防御性跳过已从缓存消失、或已不再 modified 的条目。
		for outpoint := range s.dirtyOps {
			entry, ok := s.cachedEntries.get(outpoint)
			if !ok {
				// The entry was removed from the cache (e.g. spent while
				// fresh, or purged); nothing to persist for it.
				// 条目已从缓存移除(如 fresh 被花费,或被 purge);无需写盘。
				continue
			}

			switch {
			// If the entry is nil or spent, remove the entry from the
			// database and the cache.  The delete is now persisted.
			// 条目为 nil/spent:删除数据库行并从缓存移除,删除已落盘。
			case entry == nil || entry.IsSpent():
				err := dbDeleteUtxoEntry(utxoBucket, outpoint)
				if err != nil {
					return err
				}
				s.cachedEntries.delete(outpoint)
				s.totalEntryMemory -= entry.memoryUsage()

			// No need to update the entry if it was not modified.
			case !entry.isModified():

			default:
				// Entry is dirty and needs to be put into the database; keep
				// it resident and clear its flags so it is not rewritten on
				// the next flush.
				// 条目脏且需写盘;保留驻留并清除标记,下次 flush 不重写。
				err := dbPutUtxoEntry(utxoBucket, outpoint, entry)
				if err != nil {
					return err
				}
				entry.clearModified()
			}
		}
		s.dirtyOps = make(map[wire.OutPoint]struct{})
	}

	// When done, store the best state hash in the database to indicate the state
	// is consistent until that hash.
	err := dbPutUtxoStateConsistency(dbTx, &bestState.Hash)
	if err != nil {
		return err
	}

	// The best state is the new last flush hash.
	s.lastFlushHash = bestState.Hash
	s.lastFlushTime = time.Now()

	return nil
}

// flush flushes the UTXO state to the database if a flush is needed with the given flush mode.
//
// This function MUST be called with the chain state lock held (for writes).
func (s *utxoCache) flush(dbTx database.Tx, mode FlushMode, bestState *BestState) error {
	var threshold uint64
	// cleanCache selects whether the whole in-memory cache is cleared after the
	// write.  FlushRequired (shutdown/rollback/prune) always clears; a flush
	// that is forced by a full cache also clears so memory is reclaimed and a
	// saturated IBD path never pays a full scan per block.  Only the periodic
	// background flush with headroom left performs an incremental flush that
	// retains the cache (see writeCache).
	// cleanCache 决定写盘后是否清空整个内存缓存。FlushRequired(关闭/回滚/
	// prune)总是清空;因缓存已满而触发的 flush 也清空以回收内存,饱和 IBD
	// 路径不会每块全扫。只有后台周期 flush 且仍有内存余量时才增量保留
	// (见 writeCache)。
	cleanCache := mode == FlushRequired

	switch mode {
	case FlushRequired:
		threshold = 0

	case FlushIfNeeded:
		// If we performed a flush in the current best state, we have nothing to do.
		if bestState.Hash == s.lastFlushHash {
			return nil
		}

		threshold = s.maxTotalMemoryUsage

	case FlushPeriodic:
		// If the time since the last flush is over the periodic interval,
		// force a flush.  Otherwise just flush when the cache is full.
		if time.Since(s.lastFlushTime) > utxoFlushPeriodicInterval {
			threshold = 0
		} else {
			threshold = s.maxTotalMemoryUsage
		}
	}

	totalUsage := s.totalMemoryUsage()
	if totalUsage >= s.maxTotalMemoryUsage {
		cleanCache = true
	}

	if totalUsage >= threshold {
		// Add one to round up the integer division.
		totalMiB := totalUsage / ((1024 * 1024) + 1)
		if cleanCache {
			log.Infof("Flushing UTXO cache of %d MiB with %d entries to disk. For large sizes, "+
				"this can take up to several minutes...", totalMiB, s.cachedEntries.length())
		} else {
			log.Infof("Incrementally flushing UTXO cache of %d MiB with %d entries to disk",
				totalMiB, s.cachedEntries.length())
		}

		return s.writeCache(dbTx, bestState, cleanCache)
	}

	return nil
}

// FlushUtxoCache flushes the UTXO state to the database if a flush is needed with the
// given flush mode.
//
// This function is safe for concurrent access.
func (b *BlockChain) FlushUtxoCache(mode FlushMode) error {
	start := time.Now()
	b.chainLock.Lock()
	defer b.chainLock.Unlock()

	// Asher_Mod_Start_20260911_162551
	// Under A3 the chain-state metadata is written asynchronously: drain the
	// queue to the current tip first so the UTXO entries/marker persisted below
	// stay at or below the metadata watermark (A3 crash-safety invariant).
	// A3 下链状态元数据异步写:先排空元数据队列到当前 tip,使下面落盘的
	// UTXO 条目/标记不高于元数据水印(A3 崩溃安全不变量)。
	if b.writeQueue != nil {
		b.writeQueue.syncNow()
	}
	// Asher_Mod_End_20260911_162551

	err := b.db.Update(func(dbTx database.Tx) error {
		return b.utxoCache.flush(dbTx, mode, b.BestSnapshot())
	})

	// Feed the A6 metrics layer: record the most recent flush duration and the
	// running flush count.  Timed from the entry so it covers the full flush
	// (chain lock + database write) for both the background periodic flush and
	// the shutdown flush.
	// 为 A6 指标层记录最近一次落盘耗时与累计落盘次数。从入口计时,
	// 覆盖完整落盘(链锁+数据库写入),对后台周期落盘与关闭时落盘均生效。
	b.utxoFlushLastMs.Store(time.Since(start).Milliseconds())
	b.utxoFlushCount.Add(1)
	return err
}

// PurgeUtxosAboveHeight removes every UTXO entry whose block height exceeds the
// given height from both the in-memory cache and the database, and returns the
// number of entries removed.  Entries above a connected tip cannot belong to
// any legitimate chain -- no block at those heights has ever been connected --
// so they are necessarily residue left behind when a previous session rolled
// the header chain back without undoing the UTXO state (see
// InvalidateHeaderChain / rollbackFabricatedHeaderChain).  This residue
// otherwise makes a re-downloaded block fail its BIP0030 overwrite check
// forever because the block's own outputs are already present in the UTXO set.
//
// This function is safe for concurrent access.
func (b *BlockChain) PurgeUtxosAboveHeight(height int32) (int, error) {
	b.chainLock.Lock()
	defer b.chainLock.Unlock()

	s := b.utxoCache
	var purged int

	// Track outpoints that have already been purged from the cache so the
	// database pass below does not count the same logical entry twice (a
	// cache entry fetched from the DB and its persisted row are the same
	// outpoint).  Entries that were flushed and re-fetched are unmodified,
	// so dropping the DB row after the cache drop is still required.
	seen := make(map[wire.OutPoint]struct{})

	// Purge the in-memory cache first.  Entries are deleted in place so the
	// loop is safe against concurrent misses; the chain lock serializes all
	// other cache access in the same way writeCache relies on it.
	for i := range s.cachedEntries.maps {
		m := s.cachedEntries.maps[i]
		for outpoint, entry := range m {
			if entry == nil || entry.IsSpent() {
				continue
			}
			if entry.BlockHeight() > height {
				delete(m, outpoint)
				s.totalEntryMemory -= entry.memoryUsage()
				delete(s.dirtyOps, outpoint)
				seen[outpoint] = struct{}{}
				purged++
			}
		}
	}

	// Purge the persisted entries from the database.  Cursor.Delete removes
	// the current pair without invalidating the cursor, so stale rows can be
	// dropped in a single pass.
	err := b.db.Update(func(dbTx database.Tx) error {
		utxoBucket := dbTx.Metadata().Bucket(utxoSetBucketName)
		cursor := utxoBucket.Cursor()
		for ok := cursor.First(); ok; ok = cursor.Next() {
			entry, derr := deserializeUtxoEntry(cursor.Value())
			if derr != nil {
				return derr
			}
			if entry.BlockHeight() > height {
				// Decode the outpoint from the cursor key so a row that
				// was already dropped from the cache is not double-counted.
				op, derr := deserializeOutpointKey(cursor.Key())
				if derr != nil {
					return derr
				}
				if derr := cursor.Delete(); derr != nil {
					return derr
				}
				if _, dup := seen[*op]; !dup {
					purged++
				}
			}
		}
		return nil
	})
	if err != nil {
		return purged, err
	}

	if purged > 0 {
		log.Warnf("Purged %d stale UTXO entries above height %d", purged, height)
	}

	return purged, nil
}

// InitConsistentState checks the consistency status of the utxo state and
// replays blocks if it lags behind the best state of the blockchain.
//
// It needs to be ensured that the chainView passed to this method does not
// get changed during the execution of this method.
func (b *BlockChain) InitConsistentState(tip *blockNode, interrupt <-chan struct{}) error {
	s := b.utxoCache

	// Load the consistency status from the database.
	var statusBytes []byte
	s.db.View(func(dbTx database.Tx) error {
		statusBytes = dbFetchUtxoStateConsistency(dbTx)
		return nil
	})

	// If no status was found, the database is old and didn't have a cached utxo
	// state yet. In that case, we set the status to the best state and write
	// this to the database.
	if statusBytes == nil {
		err := s.db.Update(func(dbTx database.Tx) error {
			return dbPutUtxoStateConsistency(dbTx, &tip.hash)
		})

		// Set the last flush hash as it's the default value of 0s.
		s.lastFlushHash = tip.hash
		s.lastFlushTime = time.Now()

		return err
	}

	statusHash, err := chainhash.NewHash(statusBytes)
	if err != nil {
		return err
	}

	// If state is consistent, we are done.
	if statusHash.IsEqual(&tip.hash) {
		log.Debugf("UTXO state consistent at (%d:%v)", tip.height, tip.hash)

		// The last flush hash is set to the default value of all 0s. Set
		// it to the tip since we checked it's consistent.
		s.lastFlushHash = tip.hash

		// Set the last flush time as now since we know the state is consistent
		// at this time.
		s.lastFlushTime = time.Now()

		return nil
	}

	lastFlushNode := b.index.LookupNode(statusHash)

	// With the in-memory header window, the block node for the last consistent
	// UTXO state may fall below the window boundary and be evicted from the
	// index.  Resolve its height via a temporary cold materialization from the
	// database so an unclean-shutdown recovery does not fault on a nil lookup;
	// without this a restart with windowing enabled would fail to reconstruct
	// the UTXO set.
	if lastFlushNode == nil {
		lastFlushNode = b.materializeColdNode(statusHash)
	}
	if lastFlushNode == nil {
		return ruleError(ErrPreviousBlockUnknown, fmt.Sprintf(
			"last utxo consistency status block %v not found in block index",
			statusHash))
	}
	log.Infof("Reconstructing UTXO state after an unclean shutdown. The UTXO state is "+
		"consistent at block %s (%d) but the chainstate is at block %s (%d),  This may "+
		"take a long time...", statusHash.String(), lastFlushNode.height,
		tip.hash.String(), tip.height)

	// The last consistent state and everything after it is necessarily on the
	// best chain (blocks are never disconnected during a reorganization, and
	// the cache is flushed before a reorganization begins).  Since the in-memory
	// parent chain may not reach back to the consistent node when windowing has
	// evicted it, recover by walking heights forward from the consistent height
	// instead of by following in-memory parent pointers.  The main-chain height
	// index must therefore agree on the consistent node's height, which also
	// re-validates that statusHash is on the best chain.
	var consistentHeight int32
	err = s.db.View(func(dbTx database.Tx) error {
		hash, err := dbFetchHashByHeight(dbTx, lastFlushNode.height)
		if err != nil {
			return err
		}
		if !hash.IsEqual(statusHash) {
			return AssertError(fmt.Sprintf("last utxo consistency status contains "+
				"hash that fails to match best chain at height %d: %v", lastFlushNode.height, statusHash))
		}
		consistentHeight = lastFlushNode.height
		return nil
	})
	if err != nil {
		return err
	}

	// Replay the blocks from the last consistent state up to the best state.
	// Blocks above the in-memory window are read directly from the database by
	// height, so recovery does not require any parent links that windowing may
	// have evicted.
	for height := consistentHeight + 1; height <= tip.height; height++ {
		block, err := b.fetchBlockByHeight(height)
		if err != nil {
			return err
		}

		if err := b.utxoCache.connectTransactions(block, nil); err != nil {
			return err
		}

		// Flush the utxo cache if needed.  This will in turn update the
		// consistent state to this block.
		err = b.db.Update(func(dbTx database.Tx) error {
			return s.flush(dbTx, FlushIfNeeded, &BestState{Height: height, Hash: *block.Hash()})
		})
		if err != nil {
			return err
		}

		if interruptRequested(interrupt) {
			log.Warn("UTXO state reconstruction interrupted")
			break
		}
	}
	log.Debug("UTXO state reconstruction done")

	// Asher_Mod_Start_20260911_173000
	// Discard any deltas accumulated by the replay above so they never leak
	// into the first connected block's batch item (full 方案1).
	// 丢弃上方重放累积的增量,避免泄漏进首个连接块的批次 item(完整方案1)。
	s.resetDelta()
	// Asher_Mod_End_20260911_173000

	// Set the last flush hash as it's the default value of 0s.
	s.lastFlushHash = tip.hash
	s.lastFlushTime = time.Now()

	return nil
}

// flushNeededAfterPrune returns true if the utxo cache needs to be flushed after a prune
// of the block storage.  In the case of an unexpected shutdown, the utxo cache needs
// to be reconstructed from where the utxo cache was last flushed.  In order for the
// utxo cache to be reconstructed, we always need to have the blocks since the utxo cache
// flush last happened.
//
// Example: if the last flush hash was at height 100 and one of the deleted blocks was at
// height 98, this function will return true.
func (b *BlockChain) flushNeededAfterPrune(deletedBlockHashes []chainhash.Hash) (bool, error) {
	node := b.index.LookupNode(&b.utxoCache.lastFlushHash)
	if node == nil {
		// If we couldn't find the node where we last flushed at, have the utxo cache
		// flush to be safe and that will set the last flush hash again.
		//
		// This realistically should never happen as nodes are never deleted from
		// the block index.  This happening likely means that there's a hardware
		// error which is something we can't recover from.  The best that we can
		// do here is to just force a flush and hope that the newly set
		// lastFlushHash doesn't error.
		return true, nil
	}

	lastFlushHeight := node.Height()

	// Loop through all the block hashes and find out what the highest block height
	// among the deleted hashes is.
	highestDeletedHeight := int32(-1)
	for _, deletedBlockHash := range deletedBlockHashes {
		node := b.index.LookupNode(&deletedBlockHash)
		if node == nil {
			// If we couldn't find this node, just skip it and try the next
			// deleted hash.  This might be a corruption in the database
			// but there's nothing we can do here to address it except for
			// moving onto the next block.
			continue
		}
		if node.height > highestDeletedHeight {
			highestDeletedHeight = node.height
		}
	}

	return highestDeletedHeight >= lastFlushHeight, nil
}
// Asher_Mod_End_20260910_142235
// Asher_Mod_End_20260910_131359
// Asher_Mod_End_20260910_123842
