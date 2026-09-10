// Copyright (c) 2013-2017 The btcsuite developers
// Copyright (c) 2017 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Asher_Mod_Start_20260910_123842
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/btcsuite/btcd/addrmgr"
	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/blockchain/indexers"
	"github.com/btcsuite/btcd/connmgr"
	"github.com/btcsuite/btcd/database"
	"github.com/btcsuite/btcd/internal/inbound"
	"github.com/btcsuite/btcd/mempool"
	"github.com/btcsuite/btcd/mining"
	"github.com/btcsuite/btcd/mining/cpuminer"
	"github.com/btcsuite/btcd/netsync"
	"github.com/btcsuite/btcd/peer"
	"github.com/btcsuite/btcd/txscript/v2"
	"github.com/btcsuite/btcd/v2transport"

	"github.com/btcsuite/btclog"
	"github.com/jrick/logrotate/rotator"
)

// logWriter implements an io.Writer that queues log lines to a bounded
// channel and flushes them to both standard output and the write-end pipe of
// an initialized log rotator on a background goroutine.  A log call made while
// holding the chain lock (SYNC/CHAN inside the block handler) therefore only
// enqueues the formatted line and never performs file I/O inline, so a slow
// disk or a blocking rotator can never stall the chain (A7).
// logWriter 实现了一个 io.Writer:日志行先进入有界队列,由后台 goroutine
// 统一刷到标准输出和日志轮转器。持链锁时的日志调用(SYNC/CHAN block
// handler 内)只做入队,绝不内联做文件 I/O,磁盘慢或轮转阻塞都不会卡链(A7)。
type logWriter struct {
	queue   chan []byte
	wg      sync.WaitGroup
	dropped atomic.Uint64
	stopped atomic.Bool
}

// logWriterQueueSize bounds the in-memory backlog of queued log lines.  When
// the queue is full, new lines are dropped (and counted) rather than blocking
// the caller: the alternative -- blocking the producer -- would reintroduce
// the exact stall this writer exists to prevent, and the 8192-line backlog is
// far beyond anything a live node produces between flushes.
const logWriterQueueSize = 8192

// newLogWriter creates a logWriter and starts its background flusher.
func newLogWriter() *logWriter {
	lw := &logWriter{
		queue: make(chan []byte, logWriterQueueSize),
	}
	lw.wg.Add(1)
	go lw.flushLoop()
	return lw
}

// Write enqueues the formatted line for the background flusher.  It never
// blocks: if the queue is full the line is dropped and counted.  Once the
// writer has been stopped (stopLogWriter) the line is dropped instead of
// sending on the closed channel, which would panic.  The returned byte count
// always equals len(p) so the btclog backend never retries.
func (lw *logWriter) Write(p []byte) (n int, err error) {
	if lw.stopped.Load() {
		lw.dropped.Add(1)
		return len(p), nil
	}
	msg := append([]byte(nil), p...)
	select {
	case lw.queue <- msg:
	default:
		lw.dropped.Add(1)
	}
	return len(p), nil
}

// flushLoop drains the queue, writing each line to stdout and the log
// rotator.  It runs until the queue is closed by stopLogWriter.  The rotator
// pointer is read under logRotatorMu because initLogRotator assigns it from
// the main goroutine while this loop may already be running (A7).
func (lw *logWriter) flushLoop() {
	defer lw.wg.Done()
	for msg := range lw.queue {
		os.Stdout.Write(msg)
		logRotatorMu.RLock()
		rot := logRotator
		logRotatorMu.RUnlock()
		if rot != nil {
			rot.Write(msg)
		}
	}
}

// stopLogWriter closes the queue and waits for the background flusher to
// drain every queued line.  It must be called before the rotator is closed on
// shutdown so no buffered line is lost.  It is idempotent: concurrent or
// repeated calls are safe, and any Write racing with the stop drops its line
// instead of panicking on the closed channel.
func (lw *logWriter) stopLogWriter() {
	if lw.stopped.Swap(true) {
		return
	}
	close(lw.queue)
	lw.wg.Wait()
}

// Dropped returns the number of log lines discarded since start: lines that
// arrived when the bounded queue was full, or after the writer was stopped.
// It makes silent log loss observable (A7).
func (lw *logWriter) Dropped() uint64 {
	return lw.dropped.Load()
}

// Loggers per subsystem.  A single backend logger is created and all subsystem
// loggers created from it will write to the backend.  When adding new
// subsystems, add the subsystem logger variable here and to the
// subsystemLoggers map.
//
// Loggers can not be used before the log rotator has been initialized with a
// log file.  This must be performed early during application startup by calling
// initLogRotator.
var (
	// backendLog is the logging backend used to create all subsystem loggers.
	// The backend must not be used before the log rotator has been initialized,
	// or data races and/or nil pointer dereferences will occur.
	//
	// logWriterInst holds the async log writer so shutdown can drain the queue
	// (stopLogWriter) before the rotator is closed (A7).
	// logWriterInst 持有异步日志 writer,供关闭时先排空队列
	// (stopLogWriter)再关轮转器(A7)。
	logWriterInst = newLogWriter()
	backendLog   = btclog.NewBackend(logWriterInst)

	// logRotator is one of the logging outputs.  It should be closed on
	// application shutdown.  It is written once by initLogRotator during
	// startup and read by the async logWriter flusher goroutine (A7), so
	// access is guarded by logRotatorMu to avoid a data race between the
	// background flusher and the rotator initialization.
	// logRotator 是日志输出之一,应用关闭时应 close。它在启动时由
	// initLogRotator 写一次,被异步 logWriter 的 flusher goroutine 读(A7),
	// 因此用 logRotatorMu 保护,避免后台 flusher 与轮转器初始化之间的
	// 数据竞争。
	logRotatorMu sync.RWMutex
	logRotator   *rotator.Rotator

	// logFilePath is the path of the active log file, recorded by
	// initLogRotator and used by LogFileSize to feed the A6 metrics layer.
	// logFilePath 是当前日志文件路径,由 initLogRotator 记录,
	// LogFileSize 用它为 A6 指标层提供日志字节数。
	logFilePath string

	adxrLog = backendLog.Logger("ADXR")
	amgrLog = backendLog.Logger("AMGR")
	cmgrLog = backendLog.Logger("CMGR")
	bcdbLog = backendLog.Logger("BCDB")
	iniLog = backendLog.Logger("INI")
	chanLog = backendLog.Logger("CHAN")
	discLog = backendLog.Logger("DISC")
	indxLog = backendLog.Logger("INDX")
	minrLog = backendLog.Logger("MINR")
	peerLog = backendLog.Logger("PEER")
	rpcsLog = backendLog.Logger("RPCS")
	scrpLog = backendLog.Logger("SCRP")
	srvrLog = backendLog.Logger("SRVR")
	syncLog = backendLog.Logger("SYNC")
	txmpLog = backendLog.Logger("TXMP")
	v2trLog = backendLog.Logger(v2transport.Subsystem)
)

// Initialize package-global logger variables.
func init() {
	addrmgr.UseLogger(amgrLog)
	connmgr.UseLogger(cmgrLog)
	database.UseLogger(bcdbLog)
	inbound.UseLogger(srvrLog)
	blockchain.UseLogger(chanLog)
	indexers.UseLogger(indxLog)
	mining.UseLogger(minrLog)
	cpuminer.UseLogger(minrLog)
	peer.UseLogger(peerLog)
	txscript.UseLogger(scrpLog)
	netsync.UseLogger(syncLog)
	mempool.UseLogger(txmpLog)
	v2transport.UseLogger(v2trLog)
}

// subsystemLoggers maps each subsystem identifier to its associated logger.
var subsystemLoggers = map[string]btclog.Logger{
	"ADXR":                adxrLog,
	"AMGR":                amgrLog,
	"CMGR":                cmgrLog,
	"BCDB":                bcdbLog,
	"INI":                 iniLog,
	"CHAN":                chanLog,
	"DISC":                discLog,
	"INDX":                indxLog,
	"MINR":                minrLog,
	"PEER":                peerLog,
	"RPCS":                rpcsLog,
	"SCRP":                scrpLog,
	"SRVR":                srvrLog,
	"SYNC":                syncLog,
	"TXMP":                txmpLog,
	v2transport.Subsystem: v2trLog,
}

// initLogRotator initializes the logging rotater to write logs to logFile and
// create roll files in the same directory.  It must be called before the
// package-global log rotater variables are used.
func initLogRotator(logFile string) {
	logDir, _ := filepath.Split(logFile)
	err := os.MkdirAll(logDir, 0700)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create log directory: %v\n", err)
		os.Exit(1)
	}
	r, err := rotator.New(logFile, 10*1024, false, 3)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create file rotator: %v\n", err)
		os.Exit(1)
	}

	logRotatorMu.Lock()
	logRotator = r
	logRotatorMu.Unlock()
	logFilePath = logFile
}

// LogFileSize returns the current size in bytes of the active log file, or 0
// if the rotator has not been initialized or the file cannot be statted.  It
// feeds the A6 log_bytes metric; the rotator renames the file on rotation, so
// statting the recorded path always reflects the live log.
// LogFileSize 返回当前日志文件的字节大小,未初始化或 stat 失败时返回 0。
// 它为 A6 log_bytes 指标供数;轮转时 rotator 会重命名文件,因此 stat 记录的
// 路径始终反映实时日志。
func LogFileSize() int64 {
	if logFilePath == "" {
		return 0
	}
	st, err := os.Stat(logFilePath)
	if err != nil {
		return 0
	}
	return st.Size()
}

// setLogLevel sets the logging level for provided subsystem.  Invalid
// subsystems are ignored.  Uninitialized subsystems are dynamically created as
// needed.
func setLogLevel(subsystemID string, logLevel string) {
	// Ignore invalid subsystems.
	logger, ok := subsystemLoggers[subsystemID]
	if !ok {
		return
	}

	// Defaults to info if the log level is invalid.
	level, _ := btclog.LevelFromString(logLevel)
	logger.SetLevel(level)
}

// setLogLevels sets the log level for all subsystem loggers to the passed
// level.  It also dynamically creates the subsystem loggers as needed, so it
// can be used to initialize the logging system.
func setLogLevels(logLevel string) {
	// Configure all sub-systems with the new logging level.  Dynamically
	// create loggers as needed.
	for subsystemID := range subsystemLoggers {
		setLogLevel(subsystemID, logLevel)
	}
}

// currentLogLevel returns the current log level of all subsystems as a
// string.  When every subsystem is set to the same level, that single level
// is returned; otherwise the level of the main INI subsystem is returned as
// a representative value (frontends consume a single dropdown value).
func currentLogLevel() string {
	rep := subsystemLoggers["INI"].Level()
	for id, logger := range subsystemLoggers {
		if id == "INI" {
			continue
		}
		if logger.Level() != rep {
			return rep.String()
		}
	}
	return rep.String()
}

// directionString is a helper function that returns a string that represents
// the direction of a connection (inbound or outbound).
func directionString(inbound bool) string {
	if inbound {
		return "inbound"
	}
	return "outbound"
}

// pickNoun returns the singular or plural form of a noun depending
// on the count n.
func pickNoun(n uint64, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}
// Asher_Mod_End_20260910_123842
