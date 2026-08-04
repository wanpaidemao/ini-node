package sugarindex

import (
	"fmt"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/database"
	"github.com/btcsuite/btcd/wire/v2"
	"github.com/btcsuite/btclog"
	"github.com/syndtr/goleveldb/leveldb"
)

// log 是包级日志器,默认禁用(与 indexers/log.go 惯例一致)。
// log is the package logger, disabled by default (mirrors indexers/log.go).
var log btclog.Logger

// DisableLog 禁用包日志。DisableLog disables package logging.
func DisableLog() { log = btclog.Disabled }

// UseLogger 设置包日志器。UseLogger sets the package logger.
func UseLogger(logger btclog.Logger) { log = logger }

// Manager 持有一个独立的 raw LevelDB(位于节点 datadir 下的 index/),以与 umami
// 逐字节兼容的格式维护地址/花费/时间戳索引,并实现 blockchain.IndexManager。
//
// Manager owns a standalone raw LevelDB (index/ under the node datadir) that
// mirrors the umami address/spent/timestamp index byte-for-byte, and implements
// blockchain.IndexManager.
type Manager struct {
	db   *leveldb.DB
	key  []byte // 8 字节混淆密钥
	path string
}

// Ensure Manager satisfies the blockchain.IndexManager interface.
var _ blockchain.IndexManager = (*Manager)(nil)

// NewManager 打开(必要时创建)sugar index 的 LevelDB。
// NewManager opens (creating if needed) the sugar index LevelDB.
func NewManager(path string) (*Manager, error) {
	ldb, key, err := openIndexDB(path, log)
	if err != nil {
		return nil, err
	}
	return &Manager{db: ldb, key: key, path: path}, nil
}

// Close 关闭底层 LevelDB。Close the underlying LevelDB.
func (m *Manager) Close() error { return m.db.Close() }

// DB 返回底层 LevelDB(供 RPC 层读取)。Returns the underlying LevelDB.
func (m *Manager) DB() *leveldb.DB { return m.db }

// ---------------------------------------------------------------------------
// blockchain.IndexManager interface

// Init 追上当前主链尖点;若本地尖点是孤儿(索引停用期间发生过重组)则整体重建。
// Init catches the index up to the current tip, rebuilding from scratch when
// the local tip became an orphan (reorg while the index was disabled).
func (m *Manager) Init(chain *blockchain.BlockChain,
	interrupt <-chan struct{}) error {

	tipHash, tipHeight, err := m.fetchIndexTip()
	if err != nil {
		return err
	}

	if tipHash != nil && !chain.MainChainHasBlock(tipHash) {
		log.Warnf("Sugar index tip %v is orphaned; rebuilding from scratch",
			tipHash)
		if err := m.wipeIndex(); err != nil {
			return err
		}
		tipHeight = -1
	}

	bestHeight := chain.BestSnapshot().Height
	if tipHeight >= bestHeight {
		return nil
	}

	log.Infof("Sugar index catching up from height %d to %d",
		tipHeight+1, bestHeight)
	for height := tipHeight + 1; height <= bestHeight; height++ {
		if interruptRequested(interrupt) {
			return errInterruptRequested
		}

		block, err := chain.BlockByHeight(height)
		if err != nil {
			return err
		}
		spentTxos, err := chain.FetchSpendJournal(block)
		if err != nil {
			return err
		}
		if err := m.connectBlock(block, spentTxos); err != nil {
			return err
		}
		if height%10000 == 0 {
			log.Infof("Sugar index: indexed height %d", height)
		}
	}
	log.Infof("Sugar index caught up to height %d", bestHeight)
	return nil
}

// ConnectBlock writes the index entries for a newly connected block.  The
// database.Tx of btcd's core DB is ignored: the sugar index lives in its own
// raw LevelDB.
func (m *Manager) ConnectBlock(_ database.Tx, block *btcutil.Block,
	stxos []blockchain.SpentTxOut) error {
	return m.connectBlock(block, stxos)
}

// DisconnectBlock reverses the entries written by ConnectBlock.
func (m *Manager) DisconnectBlock(_ database.Tx, block *btcutil.Block,
	stxos []blockchain.SpentTxOut) error {
	return m.disconnectBlock(block, stxos)
}

// ---------------------------------------------------------------------------
// index computation

// connectBlock 索引一个新连接区块:输出写 +delta 与新增 unspent,输入写 -delta、
// 删除 unspent 并写入 spent-index,最后写时间戳与尖点。
//
// connectBlock indexes a freshly connected block: outputs get positive deltas
// and fresh unspents, inputs get negative deltas, spent unspents are removed
// and spent-index entries added, plus timestamp and tip entries.
func (m *Manager) connectBlock(block *btcutil.Block,
	stxos []blockchain.SpentTxOut) error {

	bd := newBlockDeltas()
	height := block.Height()
	stxoIndex := 0

	for txIdx, tx := range block.Transactions() {
		msgTx := tx.MsgTx()
		txHash := msgTx.TxHash()

		// The coinbase (txIdx 0) has no entries in the spend journal.
		if txIdx != 0 {
			for inIdx := range msgTx.TxIn {
				stxo := stxos[stxoIndex]
				bd.addSpent(msgTx.TxIn[inIdx], stxo, txHash, height,
					uint32(txIdx), uint32(inIdx))
				stxoIndex++
			}
		}

		for vIdx, txOut := range msgTx.TxOut {
			bd.addReceived(txHash, txOut, height, uint32(txIdx),
				uint32(vIdx))
		}
	}

	batch := new(leveldb.Batch)

	for _, e := range bd.addressIndex {
		ev := &enc{}
		ev.i64(e.delta)
		m.putObfuscated(batch, e.key, ev.bytes())
	}
	for _, u := range bd.addressUnspent {
		if u.isNull {
			batch.Delete(u.key)
		} else {
			m.putObfuscated(batch, u.key, u.encode())
		}
	}
	for _, s := range bd.spentIndex {
		m.putObfuscated(batch, s.key, s.encode())
	}

	tk := &TimestampIndexKey{
		Timestamp: uint32(block.MsgBlock().Header.Timestamp.Unix()),
		BlockHash: *block.Hash(),
	}
	m.putObfuscated(batch, tk.Key(), []byte{0})

	m.storeIndexTip(batch, block.Hash(), height)
	return m.db.Write(batch, nil)
}

// disconnectBlock 撤销 connectBlock:删除该区块产生的全部 address deltas 与
// 新 unspent,恢复被花费的 unspent,删除 spent-index 条目。
//
// disconnectBlock reverses connectBlock: it erases all address deltas and the
// freshly created unspents of the block, restores the spent unspents, and
// erases the spent-index entries.
func (m *Manager) disconnectBlock(block *btcutil.Block,
	stxos []blockchain.SpentTxOut) error {

	bd := newBlockDeltas()
	height := block.Height()
	stxoIndex := 0

	for txIdx, tx := range block.Transactions() {
		msgTx := tx.MsgTx()
		txHash := msgTx.TxHash()

		if txIdx != 0 {
			for inIdx := range msgTx.TxIn {
				stxo := stxos[stxoIndex]
				bd.undoSpent(msgTx.TxIn[inIdx], stxo, txHash, height,
					uint32(txIdx), uint32(inIdx))
				stxoIndex++
			}
		}

		for vIdx, txOut := range msgTx.TxOut {
			bd.undoReceived(txHash, txOut, height, uint32(txIdx),
				uint32(vIdx))
		}
	}

	batch := new(leveldb.Batch)

	// AddressIndex: erase everything this block wrote.
	for _, e := range bd.addressErase {
		batch.Delete(e.key)
	}
	// AddressUnspent: erase the outputs, restore the inputs.
	for _, u := range bd.addressUnspent {
		if u.isNull {
			batch.Delete(u.key)
		} else {
			m.putObfuscated(batch, u.key, u.encode())
		}
	}
	// SpentIndex: erase.
	for _, sk := range bd.spentErase {
		batch.Delete((&SpentIndexKey{
			TxID:        sk.txID,
			OutputIndex: sk.outN,
		}).Key())
	}

	m.storeIndexTip(batch, &block.MsgBlock().Header.PrevBlock, height-1)
	return m.db.Write(batch, nil)
}

// ---------------------------------------------------------------------------
// delta accumulation

// blockDeltas 汇总一个区块产生的全部索引增量。
// blockDeltas accumulates all index deltas produced by one block.
type blockDeltas struct {
	addressIndex   []addrIndexEntry
	addressUnspent []addrUnspentEntry
	spentIndex     []spentIndexEntry
	// disconnect only
	addressErase []addrIndexEntry
	spentErase   []spentIndexKey
}

func newBlockDeltas() *blockDeltas { return &blockDeltas{} }

type addrIndexEntry struct {
	key   []byte
	delta int64
}

type addrUnspentEntry struct {
	key      []byte
	isNull   bool
	satoshis int64
	script   []byte
	height   int32
}

func (u *addrUnspentEntry) encode() []byte {
	return (&AddressUnspentValue{
		Satoshis:    u.satoshis,
		Script:      u.script,
		BlockHeight: u.height,
	}).Encode()
}

type spentIndexEntry struct {
	key           []byte
	txHash        chainhash.Hash
	inIndex       uint32
	blockHeight   int32
	satoshis      int64
	addrType      int32
	addrHashBytes []byte
}

func (s *spentIndexEntry) encode() []byte {
	e := &enc{}
	e.hash(s.txHash)
	e.u32(s.inIndex)
	e.i32(s.blockHeight)
	e.i64(s.satoshis)
	e.i32(s.addrType)
	e.hashIndex(s.addrHashBytes)
	return e.bytes()
}

type spentIndexKey struct {
	txID chainhash.Hash
	outN uint32
}

// pushAddrIndex 追加一条地址增量(连接或断开均可用于构造删除键)。
// pushAddrIndex appends one address delta entry.
func (bd *blockDeltas) pushAddrIndex(erase bool, spending bool,
	scriptType int, hashBytes []byte, height int32, txIdx uint32,
	txHash chainhash.Hash, index uint32, delta int64) {

	k := &AddressIndexKey{
		Type:        uint32(scriptType),
		HashBytes:   hashBytes,
		BlockHeight: height,
		TxIndex:     txIdx,
		TxHash:      txHash,
		Index:       index,
		Spending:    spending,
	}
	entry := addrIndexEntry{key: k.Key(), delta: delta}
	if erase {
		bd.addressErase = append(bd.addressErase, entry)
	} else {
		bd.addressIndex = append(bd.addressIndex, entry)
	}
}

// addReceived 处理一个输出的 +delta 与 unspent 写入。
// addReceived handles one output's positive delta + unspent write.
func (bd *blockDeltas) addReceived(txHash chainhash.Hash, txOut *wire.TxOut,
	height int32, txIdx, vIdx uint32) {

	scriptType, hashBytes := ExtractIndexInfo(txOut.PkScript)
	if scriptType == AddrIndtUnknown {
		return
	}
	bd.pushAddrIndex(false, false, scriptType, hashBytes, height, txIdx,
		txHash, vIdx, txOut.Value)
	bd.addressUnspent = append(bd.addressUnspent, addrUnspentEntry{
		key: (&AddressUnspentKey{
			Type:      uint32(scriptType),
			HashBytes: hashBytes,
			TxHash:    txHash,
			Index:     vIdx,
		}).Key(),
		satoshis: txOut.Value,
		script:   txOut.PkScript,
		height:   height,
	})
}

// addSpent 处理一个输入的 -delta、unspent 删除与 spent-index 写入。
// addSpent handles one input's negative delta, unspent erasure and spent-index
// write.
func (bd *blockDeltas) addSpent(txIn *wire.TxIn,
	stxo blockchain.SpentTxOut, txHash chainhash.Hash, height int32,
	txIdx, inIdx uint32) {

	scriptType, hashBytes := ExtractIndexInfo(stxo.PkScript)
	if scriptType == AddrIndtUnknown {
		return
	}
	prev := &txIn.PreviousOutPoint
	bd.pushAddrIndex(false, true, scriptType, hashBytes, height, txIdx,
		txHash, inIdx, -stxo.Amount)
	bd.addressUnspent = append(bd.addressUnspent, addrUnspentEntry{
		key: (&AddressUnspentKey{
			Type:      uint32(scriptType),
			HashBytes: hashBytes,
			TxHash:    prev.Hash,
			Index:     prev.Index,
		}).Key(),
		isNull: true,
	})
	bd.spentIndex = append(bd.spentIndex, spentIndexEntry{
		key:           (&SpentIndexKey{TxID: prev.Hash, OutputIndex: prev.Index}).Key(),
		txHash:        txHash,
		inIndex:       inIdx,
		blockHeight:   height,
		satoshis:      stxo.Amount,
		addrType:      int32(scriptType),
		addrHashBytes: hashBytes,
	})
}

// undoReceived 断开时:删除该输出的 address delta 与 unspent。
// undoReceived on disconnect: erase the output's delta and unspent.
func (bd *blockDeltas) undoReceived(txHash chainhash.Hash, txOut *wire.TxOut,
	height int32, txIdx, vIdx uint32) {

	scriptType, hashBytes := ExtractIndexInfo(txOut.PkScript)
	if scriptType == AddrIndtUnknown {
		return
	}
	bd.pushAddrIndex(true, false, scriptType, hashBytes, height, txIdx,
		txHash, vIdx, txOut.Value)
	bd.addressUnspent = append(bd.addressUnspent, addrUnspentEntry{
		key: (&AddressUnspentKey{
			Type:      uint32(scriptType),
			HashBytes: hashBytes,
			TxHash:    txHash,
			Index:     vIdx,
		}).Key(),
		isNull: true,
	})
}

// undoSpent 断开时:删除输入的 address delta 与 spent-index,并恢复 unspent。
// undoSpent on disconnect: erase the input's delta and spent-index, restore
// the unspent.
func (bd *blockDeltas) undoSpent(txIn *wire.TxIn,
	stxo blockchain.SpentTxOut, txHash chainhash.Hash, height int32,
	txIdx, inIdx uint32) {

	scriptType, hashBytes := ExtractIndexInfo(stxo.PkScript)
	if scriptType == AddrIndtUnknown {
		return
	}
	prev := &txIn.PreviousOutPoint
	bd.pushAddrIndex(true, true, scriptType, hashBytes, height, txIdx,
		txHash, inIdx, -stxo.Amount)
	bd.addressUnspent = append(bd.addressUnspent, addrUnspentEntry{
		key: (&AddressUnspentKey{
			Type:      uint32(scriptType),
			HashBytes: hashBytes,
			TxHash:    prev.Hash,
			Index:     prev.Index,
		}).Key(),
		satoshis: stxo.Amount,
		script:   stxo.PkScript,
		height:   stxo.Height,
	})
	bd.spentErase = append(bd.spentErase, spentIndexKey{
		txID: prev.Hash,
		outN: prev.Index,
	})
}

// ---------------------------------------------------------------------------
// wipe / interrupt helpers

// wipeIndex 清空全部四类索引命名空间与尖点标记。
// wipeIndex removes all four index namespaces plus the tip marker.
func (m *Manager) wipeIndex() error {
	var keys [][]byte
	iter := m.db.NewIterator(nil, nil)
	for iter.Next() {
		k := iter.Key()
		if len(k) > 0 {
			switch k[0] {
			case DBAddressIndex, DBAddressUnspent, DBTimestampIndex,
				DBSpentIndex:
				keys = append(keys, append([]byte{}, k...))
			}
		}
	}
	iter.Release()
	if err := iter.Error(); err != nil {
		return err
	}

	batch := new(leveldb.Batch)
	for _, k := range keys {
		batch.Delete(k)
	}
	batch.Delete(indexTipKey)
	return m.db.Write(batch, nil)
}

// interruptRequested reports whether the interrupt channel was closed.
func interruptRequested(interrupt <-chan struct{}) bool {
	select {
	case <-interrupt:
		return true
	default:
		return false
	}
}

// errInterruptRequested indicates a user-requested interrupt.
var errInterruptRequested = fmt.Errorf("interrupt requested")
