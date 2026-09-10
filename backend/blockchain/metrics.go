// Copyright (c) 2013-2016 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Asher_Add_Start_20260910_123842
package blockchain

import (
	"sync"
	"sync/atomic"
	"time"
)

// trackedRWMutex is a sync.RWMutex that records how long callers wait to
// acquire the lock, feeding the A6 unified atomic metrics layer
// (chain_lock_wait_ms in getblocksyncstatus).  Lock/RLock take the start time
// before blocking and accumulate the wait into two atomics after acquiring;
// Unlock/RUnlock are promoted from the embedded mutex unchanged, so every
// existing b.chainLock.Lock()/RLock() call site is accounted for with zero
// call-site changes.  The per-acquisition cost is two time.Now() calls
// (~50ns), which is negligible next to the lock-hold work it measures.
//
// NOTE: the promoted RLocker() method returns a Locker bound to the embedded
// mutex and would bypass wait accounting — do not use it on chainLock.
// trackedRWMutex 是带等待计时的 sync.RWMutex,为 A6 统一原子指标层
// (getblocksyncstatus 的 chain_lock_wait_ms)提供数据。Lock/RLock 在阻塞前
// 记起始时间,获取锁后把等待量累加到两个原子计数器;Unlock/RUnlock 由内嵌
// 互斥量原样提升,因此所有现有 b.chainLock.Lock()/RLock() 调用点零改动即可
// 被统计。每次获取多两次 time.Now()(~50ns),相对其测量的持锁工作量可忽略。
//
// 注意:提升方法 RLocker() 返回绑定在内嵌互斥量上的 Locker,会绕过等待
// 统计——不要对 chainLock 使用它。
type trackedRWMutex struct {
	sync.RWMutex
	waitNanos atomic.Int64
	acqCount  atomic.Int64
}

// Lock acquires the write lock and records the wait time.
func (m *trackedRWMutex) Lock() {
	start := time.Now()
	m.RWMutex.Lock()
	m.waitNanos.Add(time.Since(start).Nanoseconds())
	m.acqCount.Add(1)
}

// RLock acquires the read lock and records the wait time.
func (m *trackedRWMutex) RLock() {
	start := time.Now()
	m.RWMutex.RLock()
	m.waitNanos.Add(time.Since(start).Nanoseconds())
	m.acqCount.Add(1)
}

// ChainLockWaitStats returns the cumulative chain-lock wait time in
// nanoseconds and the number of lock acquisitions since process start.  The
// average wait per acquisition is waitNanos/acqCount; when acqCount is 0 the
// average is meaningless and callers should report 0.
// ChainLockWaitStats 返回进程启动以来链锁累计等待纳秒数与获取次数。
// 平均每次获取等待 = waitNanos/acqCount;acqCount 为 0 时平均值无意义,
// 调用方应报 0。
func (b *BlockChain) ChainLockWaitStats() (waitNanos, acqCount int64) {
	return b.chainLock.waitNanos.Load(), b.chainLock.acqCount.Load()
}

// UtxoFlushStats returns the most recent UTXO cache flush duration in
// milliseconds and the total number of flushes performed since process start.
// The count includes both periodic flushes from the sync manager's background
// loop and the required flush at shutdown, since timing happens at the
// FlushUtxoCache entry.
// UtxoFlushStats 返回最近一次 UTXO 缓存落盘耗时(毫秒)与进程启动以来的
// 落盘总次数。计数覆盖同步管理器后台周期的落盘与关闭时的必达落盘,
// 因为计时发生在 FlushUtxoCache 入口。
func (b *BlockChain) UtxoFlushStats() (lastMs, count int64) {
	return b.utxoFlushLastMs.Load(), b.utxoFlushCount.Load()
}
// Asher_Add_End_20260910_123842
