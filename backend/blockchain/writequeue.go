// Copyright (c) 2026 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Asher_Mod_Start_20260911_123000
package blockchain

import (
	"math/big"
	"sync"

	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/database"
)

// writeQueueCap is the capacity of the async write queue (A3).  A bounded
// queue gives natural back-pressure: when the writer goroutine falls behind,
// the connect path blocks at the enqueue instead of unboundedly buffering
// memory.
// writeQueueCap 是异步写队列容量(A3)。有界队列提供天然背压:当 writer
// goroutine 落后时,连接路径在入队处阻塞,而非无界缓冲内存。
const writeQueueCap = 64

// writeQueueBatchSize is how many writeItems' metadata are merged into a
// single database transaction before the write watermark advances (A3).
// The write transaction count is therefore divided by writeQueueBatchSize
// (G4 target).  Distinct from blockFlushBatchSize (header-sync flush cadence,
// 1000) and the UTXO flush cadence.
// writeQueueBatchSize 是每多少个 writeItem 的元数据合并进单个数据库事务、
// 随后推进写水印的批量大小(A3)。写事务数因此除以 writeQueueBatchSize
// (G4 目标)。与 blockFlushBatchSize(header 同步 flush 节奏,1000)和 UTXO
// flush 节奏不同。
const writeQueueBatchSize = 32

// writeWatermarkBucketName is the db bucket that stores the highest height
// whose chain-state metadata (block index rows, best state, height index,
// spend journal) has been durably written by the async writer.  On startup,
// heights above the watermark are replayed from the block bodies already on
// disk (A3).
// writeWatermarkBucketName 是存储"链状态元数据(块索引行、best state、
// 高度索引、spend journal)已由异步 writer 持久化写入的最高高度"的
// db bucket。启动时,高于水印的高度从已落盘的块体重放(A3)。
var writeWatermarkBucketName = []byte("writewatermark")

// nodeRowSnapshot is the snapshot of one dirty blockNode's persisted rows:
// the block-index row (dbStoreBlockNode) plus the optional hash-index and
// height-index rows written by flushDirtyLocked.  It is captured inside the
// chain lock at the original db.Update site so the async writer reproduces
// exactly what the synchronous path used to write, even after the node has
// been evicted from the in-memory window.  The header fields are stored as
// the node's own immutable fields (not a wire.BlockHeader) so the writer can
// reconstruct a blockNode whose Header() serialization is byte-identical to
// the original.
// nodeRowSnapshot 是一个脏 blockNode 的持久化行快照:块索引行
// (dbStoreBlockNode)加上 flushDirtyLocked 写入的可选 hash 索引与高度索引
// 行。它在链锁内、原 db.Update 位置捕获,使异步 writer 精确重现同步路径
// 原本的写入内容,即使 node 之后已被从内存窗口驱逐。header 字段按 node
// 自身的不可变字段存储(而非 wire.BlockHeader),使 writer 重建的 blockNode
// 其 Header() 序列化与原节点逐字节一致。
type nodeRowSnapshot struct {
	hash   chainhash.Hash
	height int32

	// Immutable header fields (blockNode.Header() rebuilds from these).
	version    int32
	bits       uint32
	nonce      uint32
	timestamp  int64
	merkleRoot chainhash.Hash
	parentHash chainhash.Hash

	status blockStatus

	hashIndex   bool // dbPutHashIndex(hash, height)
	heightIndex bool // dbPutHeightIndex(height, hash) when on the best header view
}

// writeItem is one unit of asynchronous disk work (A3): the block body plus
// a full snapshot of the chain-state metadata produced by connecting it.
// All metadata is captured by value (not by pointer) because the writer runs
// asynchronously and the source structures (blockNode, BestState) may be
// mutated or evicted by later blocks before the writer consumes the item.
// writeItem 是一个异步落盘工作单元(A3):块体加上连接它产生的链状态
// 元数据的完整快照。所有元数据按值捕获(非指针),因为 writer 异步执行,
// 源结构(blockNode、BestState)可能在 writer 消费该条目前被后续块修改
// 或驱逐。
type writeItem struct {
	height int32
	block  *btcutil.Block

	// Metadata snapshot, mirroring the six writes inside connectBlock's
	// single db.Update (see design doc 3.3.1).
	// 元数据快照,对应 connectBlock 单个 db.Update 内的六项写入
	// (见设计文档 3.3.1)。
	nodeRows    []*nodeRowSnapshot
	bestTipHash chainhash.Hash
	tipHeight   int32
	tipWork     *big.Int
	bestState   *BestState
	blockHash   chainhash.Hash
	stxos       []SpentTxOut

	// bestHeaderHash/bestHeaderHeight snapshot the best-header state that
	// flushDirtyLocked persists via dbPutBestHeaderState, so the async
	// writer reproduces it too.  Negative height means "no best header node"
	// (not yet flushed) and skips the write.
	// bestHeaderHash/bestHeaderHeight 快照 flushDirtyLocked 通过
	// dbPutBestHeaderState 持久化的 best-header 状态,异步 writer 同样重现。
	// 高度为负表示"尚无 best header node"(尚未刷新),跳过写入。
	bestHeaderHash   chainhash.Hash
	bestHeaderHeight int32
}

// writeQueue serializes the chain-state disk writes that used to happen
// synchronously inside the chain lock (A3).  The connect path snapshots the
// metadata and enqueues a writeItem in O(block tx count) with no database
// I/O; a dedicated writer goroutine performs the actual transactions,
// merging metadata every writeQueueBatchSize items, and advances the
// persisted watermark so a crash can replay the tail.
// writeQueue 把过去在链锁内同步执行的链状态落盘串行化(A3)。连接路径在
// O(块内交易数)、零数据库 I/O 的情况下快照元数据并入队 writeItem;
// 专用 writer goroutine 执行实际事务,每 writeQueueBatchSize 个条目合并
// 一次元数据,并推进持久化水印,使崩溃后可重放尾部。
//
// Asher_Mod_Start_20260911_162551
// syncNow/syncNowBelow: the UTXO state is still persisted synchronously
// (entries + consistency marker) by the flush sites (connectBlock tail /
// background loop / shutdown), so without a guard the on-disk UTXO marker can
// advance ABOVE the async metadata watermark (up to one batch behind).  After
// an unclean shutdown the recovery assumes "consistent point <= chainstate";
// a marker above the watermark breaks that assumption (dbFetchHashByHeight of
// the marker height fails) and startup dies with "no block at height N
// exists".  The barrier below makes every flush site first drain all enqueued
// metadata to the current tip, so marker == watermark == chainstate at every
// durable moment.
// syncNow/syncNowBelow:UTXO 状态仍由 flush 各入口(connectBlock 尾部/后台循环/
// 关闭)同步落盘(条目+一致性标记),不加护栏时盘上 UTXO 标记可能领先异步元
// 数据水印(最多落后一个批次)。非正常关闭后恢复逻辑假设"一致点 <=
// chainstate";标记高于水印会破坏该假设(按标记高度查高度索引失败),启动报
// "no block at height N exists"。下面的屏障让每个 flush 入口先排空全部已入队
// 元数据到当前 tip,使任意持久化时刻满足 marker == watermark == chainstate。
type writeQueue struct {
	items  chan *writeItem
	syncCh chan syncReq

	// mu guards the stopped flag.
	mu      sync.Mutex
	stopped bool

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	db database.DB

	// indexManager replays optional-index ConnectBlock rows inside the
	// metadata merge so txindex/addrindex/sugarindex stay in lockstep with
	// the chain state (design doc 3.3.1, item ⑦).
	// indexManager 在元数据合并事务内重放可选索引的 ConnectBlock 行,
	// 使 txindex/addrindex/sugarindex 与链状态保持同步(设计文档 3.3.1 ⑦)。
	indexManager IndexManager
}

// syncReq is a drain-barrier request (A3 fix).  The requester holds the chain
// lock, so no new item can be enqueued while the writer drains: the writer
// flushes its in-flight batch, drains the queue, optionally drops items above
// maxHeight (reorg rebase -- the detached blocks' rows are removed
// synchronously by the disconnect path), flushes the tail and acks.
// syncReq 是排空屏障请求(A3 修复)。请求方持有链锁,排空期间不会再有新
// 条目入队:writer 冲刷在途批次、排空队列、按需丢弃 maxHeight 以上的条目
// (重组重基——被断开块的元数据行由 disconnect 路径同步删除)、冲刷尾部
// 并回执。
type syncReq struct {
	maxHeight int32 // < 0 保留全部条目;否则丢弃高于该高度的条目
	done      chan struct{}
}

// newWriteQueue starts the writer goroutine for a new async write queue.
// newWriteQueue 启动新异步写队列的 writer goroutine。
func newWriteQueue(db database.DB, indexManager IndexManager) *writeQueue {
	q := &writeQueue{
		items:        make(chan *writeItem, writeQueueCap),
		syncCh:       make(chan syncReq),
		stopCh:       make(chan struct{}),
		db:           db,
		indexManager: indexManager,
	}
	q.wg.Add(1)
	go q.writer()
	return q
}

// syncNow blocks until every item enqueued so far is durably written and the
// watermark advanced to the current tip.  It must be called with the chain
// lock held (before persisting UTXO entries/marker) so the on-disk UTXO
// consistency marker never exceeds the metadata watermark.
// syncNow 阻塞直到当前已入队的全部条目持久化、水印推进到当前 tip。必须
// 在持有链锁时调用(落盘 UTXO 条目/标记之前),保证盘上 UTXO 一致性标记
// 不高于元数据水印。
func (q *writeQueue) syncNow() {
	q.syncNowBelow(-1)
}

// syncNowBelow is syncNow with a height filter: items above maxHeight are
// dropped instead of flushed.  Used by the disconnect path so metadata rows
// for blocks just detached from the best chain are not resurrected by the
// async writer, and the UTXO flush that follows stays consistent with the
// reconnected chain tip.  Both syncNow paths bail out without blocking when
// the queue is stopping, so a shutdown that races an in-flight barrier never
// hangs (the caller's UTXO flush then lags at worst one batch, which the
// stop-drain below repairs).
// syncNowBelow 是带高度过滤的 syncNow:maxHeight 以上的条目被丢弃而非落盘。
// 供 disconnect 路径使用,使刚被断开区块的元数据行不会被异步 writer 复活,
// 且紧随其后的 UTXO flush 与重组后的链 tip 保持一致。两个 syncNow 入口在
// 队列停止时直接返回而不阻塞,因此关闭与在途屏障竞争时不会挂死(调用方的
// UTXO flush 至多落后一个批次,由下面的 stop 排空补上)。
func (q *writeQueue) syncNowBelow(maxHeight int32) {
	req := syncReq{maxHeight: maxHeight, done: make(chan struct{})}
	select {
	case q.syncCh <- req:
	case <-q.stopCh:
		return
	}
	select {
	case <-req.done:
	case <-q.stopCh:
		return
	}
}

// enqueue adds one item to the async queue.  It blocks when the queue is
// full (back-pressure) and reports false after the queue has been stopped.
// enqueue 向异步队列添加一个条目。队列满时阻塞(背压),队列已停止后返回
// false。
func (q *writeQueue) enqueue(item *writeItem) bool {
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		return false
	}
	q.mu.Unlock()

	select {
	case q.items <- item:
		return true
	case <-q.stopCh:
		return false
	}
}

// stop drains the queue, flushes the remaining (partial) batch and stops the
// writer goroutine.  It is called during shutdown after the chain lock is
// released.  Idempotent via sync.Once.
// stop 排空队列,冲刷剩余(部分)批次并停止 writer goroutine。在关闭流程
// 中、链锁释放后调用。通过 sync.Once 幂等。
func (q *writeQueue) stop() {
	q.stopOnce.Do(func() {
		q.mu.Lock()
		q.stopped = true
		q.mu.Unlock()
		close(q.stopCh)

		// Drain whatever the writer has not consumed yet and flush it as the
		// final partial batch so nothing is lost.  The writer flushes its own
		// in-flight batch before exiting (see writer), and wg.Wait below
		// ensures both the writer's batch and this tail are durably written
		// before stop returns.
		// 排空 writer 尚未消费的条目,作为最后的部分批次冲刷,避免丢失。
		// writer 退出前会冲刷自己在途的批次(见 writer),下面的 wg.Wait
		// 确保 writer 的批次与本尾部都在 stop 返回前持久化。
		var tail []*writeItem
		for {
			select {
			case item := <-q.items:
				tail = append(tail, item)
			default:
				q.flushBatch(tail)
				q.wg.Wait()
				return
			}
		}
	})
}

// writer is the dedicated goroutine that serializes the disk writes.  It
// accumulates items into a batch and flushes the whole batch in one database
// transaction every writeQueueBatchSize items (design doc 3.3.1).  On stop it
// flushes its in-flight (partial) batch before exiting.  A syncReq barrier
// flushes the in-flight batch plus everything currently queued (optionally
// dropping items above maxHeight) before acknowledging, so the requester can
// safely persist UTXO state at the same height afterward.
// writer 是串行执行落盘的专用 goroutine。它把条目累积成批次,每
// writeQueueBatchSize 个条目把整个批次在单个数据库事务内冲刷
// (设计文档 3.3.1)。停止时先冲刷在途(部分)批次再退出。syncReq 屏障会
// 冲刷在途批次加当前队列中的全部条目(可按 maxHeight 丢弃部分)后再回执,
// 使请求方可安全地在同一高度随后落盘 UTXO 状态。
func (q *writeQueue) writer() {
	defer q.wg.Done()
	batch := make([]*writeItem, 0, writeQueueBatchSize)
	for {
		select {
		case item := <-q.items:
			batch = append(batch, item)
			if len(batch) >= writeQueueBatchSize {
				q.flushBatch(batch)
				batch = batch[:0]
			}

		case req := <-q.syncCh:
			// Flush the in-flight batch first, filtering out any items above
			// maxHeight (reorg rebase drops the just-detached blocks here).
			// 先冲刷在途批次,过滤掉 maxHeight 以上的条目(重组重基在此丢弃
			// 刚被断开的块)。
			var keep []*writeItem
			for _, it := range batch {
				if req.maxHeight < 0 || it.height <= req.maxHeight {
					keep = append(keep, it)
				}
			}
			q.flushBatch(keep)
			batch = batch[:0]

			// Drain everything still queued.  The requester holds the chain
			// lock, so nothing new is enqueued during this loop.
			// 排空仍在队列中的全部条目。请求方持有链锁,此循环期间不会有新条目。
			var tail []*writeItem
		drainsync:
			for {
				select {
				case item := <-q.items:
					if req.maxHeight < 0 || item.height <= req.maxHeight {
						tail = append(tail, item)
					}
				default:
					break drainsync
				}
			}
			q.flushBatch(tail)
			close(req.done)

		case <-q.stopCh:
			q.flushBatch(batch)
			return
		}
	}
}

// Asher_Mod_End_20260911_162551

// flushBatch merges the whole batch's metadata rows into a single database
// transaction (block-index rows, best-tip snapshot, best state, height index,
// spend journal and index manager replay) and advances the watermark to the
// batch's highest height in the same transaction.  The block bodies
// themselves are stored synchronously by maybeAcceptBlock before the
// metadata is enqueued (v1 scope: A3 asyncs the metadata write path only).
// flushBatch 把整个批次的元数据行合并进单个数据库事务(块索引行、
// best-tip 快照、best state、高度索引、spend journal 与 index manager
// 重放),并在同一事务内把水印推进到批次的最高高度。块体本身由
// maybeAcceptBlock 在元数据入队前同步存储(v1 范围:A3 只异步元数据
// 写路径)。
func (q *writeQueue) flushBatch(items []*writeItem) {
	if len(items) == 0 {
		return
	}
	lastHeight := items[len(items)-1].height
	err := q.db.Update(func(dbTx database.Tx) error {
		for _, item := range items {
			if err := writeItemRows(dbTx, item, q.indexManager); err != nil {
				return err
			}
		}
		return dbPutWriteWatermark(dbTx, lastHeight)
	})
	if err != nil {
		log.Errorf("A3 writeQueue: failed to merge metadata batch ending at "+
			"height %d: %v", lastHeight, err)
	}
}

// writeItemRows reproduces, inside one db transaction, the six metadata
// writes that connectBlock used to perform synchronously (design doc 3.3.1),
// plus the index-manager replay.
// writeItemRows 在单个数据库事务内重现 connectBlock 过去同步执行的六项
// 元数据写入(设计文档 3.3.1),加上 index manager 重放。
func writeItemRows(dbTx database.Tx, item *writeItem, indexManager IndexManager) error {
	// ① Block index rows + hash/height index rows from the node snapshots.
	for _, row := range item.nodeRows {
		if err := writeNodeRowSnapshot(dbTx, row); err != nil {
			return err
		}
	}

	// ①b Best-header state (flushDirtyLocked also persists this in the same
	// transaction via dbPutBestHeaderState).
	// ①b Best-header 状态(flushDirtyLocked 也在同一事务内通过
	// dbPutBestHeaderState 持久化它)。
	if item.bestHeaderHeight >= 0 {
		if err := dbPutBestHeaderState(dbTx, &item.bestHeaderHash,
			item.bestHeaderHeight); err != nil {
			return err
		}
	}

	// ② Best-tip snapshot.
	if err := dbPutBestTipSnapshot(dbTx, &item.bestTipHash,
		item.tipHeight, item.tipWork); err != nil {
		return err
	}

	// ④ Best state.
	if err := dbPutBestState(dbTx, item.bestState, item.tipWork); err != nil {
		return err
	}

	// ⑤ Height -> hash block index row.
	if err := dbPutBlockIndex(dbTx, &item.blockHash, item.height); err != nil {
		return err
	}

	// ⑥ Spend journal.
	if err := dbPutSpendJournalEntry(dbTx, &item.blockHash, item.stxos); err != nil {
		return err
	}

	// ⑦ Index manager replay: optional indexes (txindex/addrindex/
	// sugarindex) connect rows inside the same metadata transaction so they
	// stay in lockstep with the chain state (design doc 3.3.1, item ⑦).
	// The block body is referenced for the indexers' per-block updates.
	// ⑦ Index manager 重放:可选索引(txindex/addrindex/sugarindex)在同一个
	// 元数据事务内连接行,使其与链状态保持同步(设计文档 3.3.1 ⑦)。
	// 引用块体供索引器做每块更新。
	if indexManager != nil && item.block != nil {
		if err := indexManager.ConnectBlock(dbTx, item.block, item.stxos); err != nil {
			return err
		}
	}
	return nil
}

// writeNodeRowSnapshot writes one captured node row: the block-index entry
// plus the optional hash-index and height-index rows.  The blockNode is
// reconstructed from the snapshot's immutable header fields so its Header()
// serialization is byte-identical to the original node.
// writeNodeRowSnapshot 写入一个捕获的节点行:块索引条目加上可选的
// hash 索引与高度索引行。blockNode 由快照的不可变 header 字段重建,使其
// Header() 序列化与原节点逐字节一致。
func writeNodeRowSnapshot(dbTx database.Tx, row *nodeRowSnapshot) error {
	node := &blockNode{
		hash:       row.hash,
		height:     row.height,
		version:    row.version,
		bits:       row.bits,
		nonce:      row.nonce,
		timestamp:  row.timestamp,
		merkleRoot: row.merkleRoot,
		parentHash: row.parentHash,
		status:     row.status,
	}
	if err := dbStoreBlockNode(dbTx, node); err != nil {
		return err
	}
	if row.hashIndex {
		if err := dbPutHashIndex(dbTx, &row.hash, row.height); err != nil {
			return err
		}
	}
	if row.heightIndex {
		if err := dbPutHeightIndex(dbTx, row.height, &row.hash); err != nil {
			return err
		}
	}
	return nil
}

// dbPutWriteWatermark persists the highest durably-written metadata height.
// dbPutWriteWatermark 持久化已落盘元数据的最高高度。
func dbPutWriteWatermark(dbTx database.Tx, height int32) error {
	bucket, err := dbTx.Metadata().CreateBucketIfNotExists(writeWatermarkBucketName)
	if err != nil {
		return err
	}
	serialized := make([]byte, 4)
	byteOrder.PutUint32(serialized, uint32(height))
	return bucket.Put([]byte("height"), serialized)
}

// loadWriteWatermark returns the persisted write watermark, or -1 when none
// has been stored yet.
// loadWriteWatermark 返回已持久化的写水印;未存储时返回 -1。
func loadWriteWatermark(dbTx database.Tx) (int32, error) {
	bucket := dbTx.Metadata().Bucket(writeWatermarkBucketName)
	if bucket == nil {
		return -1, nil
	}
	v := bucket.Get([]byte("height"))
	if v == nil {
		return -1, nil
	}
	return int32(byteOrder.Uint32(v)), nil
}
