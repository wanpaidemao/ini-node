// Copyright (c) 2015-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package blockchain

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/database"
	"github.com/btcsuite/btcd/wire/v2"
)

const (
	// blockHdrSize is the size of a block header.  This is simply the
	// constant from wire and is only provided here for convenience since
	// wire.MaxBlockHeaderPayload is quite long.
	blockHdrSize = wire.MaxBlockHeaderPayload

	// latestUtxoSetBucketVersion is the current version of the utxo set
	// bucket that is used to track all unspent outputs.
	latestUtxoSetBucketVersion = 2

	// latestSpendJournalBucketVersion is the current version of the spend
	// journal bucket that is used to track all spent transactions for use
	// in reorgs.
	latestSpendJournalBucketVersion = 1
)

var (
	// blockIndexBucketName is the name of the db bucket used to house to the
	// block headers and contextual information.
	blockIndexBucketName = []byte("blockheaderidx")

	// hashIndexBucketName is the name of the db bucket used to house to the
	// block hash -> block height index.
	hashIndexBucketName = []byte("hashidx")

	// heightIndexBucketName is the name of the db bucket used to house to
	// the block height -> block hash index.
	heightIndexBucketName = []byte("heightidx")

	// chainStateKeyName is the name of the db key used to store the best
	// chain state.
	chainStateKeyName = []byte("chainstate")

	// bestHeaderStateKeyName is the name of the db key used to store the best
	// header state.  Unlike chainStateKeyName, which tracks the best fully
	// connected chain, this tracks the tip of the best known header chain so a
	// restart can resume a header sync from the last accepted header instead of
	// re-downloading headers up to the best connected chain tip.
	bestHeaderStateKeyName = []byte("headerchainstate")

	// blockDownloadStateKeyName is the name of the db key used to store the
	// furthest block whose data has been written to disk.  A restart can use it
	// to resume a block download from that block instead of re-scanning every
	// height below it.
	blockDownloadStateKeyName = []byte("blockdownloadstate")

	// utxoStateConsistencyKeyName is the name of the db key used to store the
	// consistency status of the utxo state.
	utxoStateConsistencyKeyName = []byte("utxostateconsistency")

	// spendJournalVersionKeyName is the name of the db key used to store
	// the version of the spend journal currently in the database.
	spendJournalVersionKeyName = []byte("spendjournalversion")

	// spendJournalBucketName is the name of the db bucket used to house
	// transactions outputs that are spent in each block.
	spendJournalBucketName = []byte("spendjournal")

	// utxoSetVersionKeyName is the name of the db key used to store the
	// version of the utxo set currently in the database.
	utxoSetVersionKeyName = []byte("utxosetversion")

	// utxoSetBucketName is the name of the db bucket used to house the
	// unspent transaction output set.
	utxoSetBucketName = []byte("utxosetv2")

	// byteOrder is the preferred byte order used for serializing numeric
	// fields for storage in the database.
	byteOrder = binary.LittleEndian
)

// errNotInMainChain signifies that a block hash or height that is not in the
// main chain was requested.
type errNotInMainChain string

// Error implements the error interface.
func (e errNotInMainChain) Error() string {
	return string(e)
}

// isNotInMainChainErr returns whether or not the passed error is an
// errNotInMainChain error.
func isNotInMainChainErr(err error) bool {
	_, ok := err.(errNotInMainChain)
	return ok
}

// IsNotInMainChainErr reports whether the given error is an
// errNotInMainChain error, i.e. a block hash or height that is not on the
// main chain was requested.  Exported so indexers (e.g. sugarindex) can
// tolerate missing heights during a from-scratch rebuild instead of
// aborting startup: the block simply has not been downloaded yet and will
// be indexed by ConnectBlock when it arrives.
// IsNotInMainChainErr 报告给定错误是否为 errNotInMainChain 错误,即请求了
// 不在主链上的块 hash 或高度。导出给索引器(如 sugarindex)在从零重建时
// 容忍缺失高度而不中止启动:该块只是尚未下载,到达时由 ConnectBlock 补索引。
func IsNotInMainChainErr(err error) bool {
	return isNotInMainChainErr(err)
}

// errDeserialize signifies that a problem was encountered when deserializing
// data.
type errDeserialize string

// Error implements the error interface.
func (e errDeserialize) Error() string {
	return string(e)
}

// isDeserializeErr returns whether or not the passed error is an errDeserialize
// error.
func isDeserializeErr(err error) bool {
	_, ok := err.(errDeserialize)
	return ok
}

// isDbBucketNotFoundErr returns whether or not the passed error is a
// database.Error with an error code of database.ErrBucketNotFound.
func isDbBucketNotFoundErr(err error) bool {
	dbErr, ok := err.(database.Error)
	return ok && dbErr.ErrorCode == database.ErrBucketNotFound
}

// dbFetchVersion fetches an individual version with the given key from the
// metadata bucket.  It is primarily used to track versions on entities such as
// buckets.  It returns zero if the provided key does not exist.
func dbFetchVersion(dbTx database.Tx, key []byte) uint32 {
	serialized := dbTx.Metadata().Get(key)
	if serialized == nil {
		return 0
	}

	return byteOrder.Uint32(serialized)
}

// dbPutVersion uses an existing database transaction to update the provided
// key in the metadata bucket to the given version.  It is primarily used to
// track versions on entities such as buckets.
func dbPutVersion(dbTx database.Tx, key []byte, version uint32) error {
	var serialized [4]byte
	byteOrder.PutUint32(serialized[:], version)
	return dbTx.Metadata().Put(key, serialized[:])
}

// dbFetchOrCreateVersion uses an existing database transaction to attempt to
// fetch the provided key from the metadata bucket as a version and in the case
// it doesn't exist, it adds the entry with the provided default version and
// returns that.  This is useful during upgrades to automatically handle loading
// and adding version keys as necessary.
func dbFetchOrCreateVersion(dbTx database.Tx, key []byte, defaultVersion uint32) (uint32, error) {
	version := dbFetchVersion(dbTx, key)
	if version == 0 {
		version = defaultVersion
		err := dbPutVersion(dbTx, key, version)
		if err != nil {
			return 0, err
		}
	}

	return version, nil
}

// -----------------------------------------------------------------------------
// The transaction spend journal consists of an entry for each block connected
// to the main chain which contains the transaction outputs the block spends
// serialized such that the order is the reverse of the order they were spent.
//
// This is required because reorganizing the chain necessarily entails
// disconnecting blocks to get back to the point of the fork which implies
// unspending all of the transaction outputs that each block previously spent.
// Since the utxo set, by definition, only contains unspent transaction outputs,
// the spent transaction outputs must be resurrected from somewhere.  There is
// more than one way this could be done, however this is the most straight
// forward method that does not require having a transaction index and unpruned
// blockchain.
//
// NOTE: This format is NOT self describing.  The additional details such as
// the number of entries (transaction inputs) are expected to come from the
// block itself and the utxo set (for legacy entries).  The rationale in doing
// this is to save space.  This is also the reason the spent outputs are
// serialized in the reverse order they are spent because later transactions are
// allowed to spend outputs from earlier ones in the same block.
//
// The reserved field below used to keep track of the version of the containing
// transaction when the height in the header code was non-zero, however the
// height is always non-zero now, but keeping the extra reserved field allows
// backwards compatibility.
//
// The serialized format is:
//
//   [<header code><reserved><compressed txout>],...
//
//   Field                Type     Size
//   header code          VLQ      variable
//   reserved             byte     1
//   compressed txout
//     compressed amount  VLQ      variable
//     compressed script  []byte   variable
//
// The serialized header code format is:
//   bit 0 - containing transaction is a coinbase
//   bits 1-x - height of the block that contains the spent txout
//
// Example 1:
// From block 170 in main blockchain.
//
//    1300320511db93e1dcdb8a016b49840f8c53bc1eb68a382e97b1482ecad7b148a6909a5c
//    <><><------------------------------------------------------------------>
//     | |                                  |
//     | reserved                  compressed txout
//    header code
//
//  - header code: 0x13 (coinbase, height 9)
//  - reserved: 0x00
//  - compressed txout 0:
//    - 0x32: VLQ-encoded compressed amount for 5000000000 (50 BTC)
//    - 0x05: special script type pay-to-pubkey
//    - 0x11...5c: x-coordinate of the pubkey
//
// Example 2:
// Adapted from block 100025 in main blockchain.
//
//    8b99700091f20f006edbc6c4d31bae9f1ccc38538a114bf42de65e868b99700086c64700b2fb57eadf61e106a100a7445a8c3f67898841ec
//    <----><><----------------------------------------------><----><><---------------------------------------------->
//     |    |                         |                        |    |                         |
//     |    reserved         compressed txout                  |    reserved         compressed txout
//    header code                                          header code
//
//  - Last spent output:
//    - header code: 0x8b9970 (not coinbase, height 100024)
//    - reserved: 0x00
//    - compressed txout:
//      - 0x91f20f: VLQ-encoded compressed amount for 34405000000 (344.05 BTC)
//      - 0x00: special script type pay-to-pubkey-hash
//      - 0x6e...86: pubkey hash
//  - Second to last spent output:
//    - header code: 0x8b9970 (not coinbase, height 100024)
//    - reserved: 0x00
//    - compressed txout:
//      - 0x86c647: VLQ-encoded compressed amount for 13761000000 (137.61 BTC)
//      - 0x00: special script type pay-to-pubkey-hash
//      - 0xb2...ec: pubkey hash
// -----------------------------------------------------------------------------

// SpentTxOut contains a spent transaction output and potentially additional
// contextual information such as whether or not it was contained in a coinbase
// transaction, the version of the transaction it was contained in, and which
// block height the containing transaction was included in.  As described in
// the comments above, the additional contextual information will only be valid
// when this spent txout is spending the last unspent output of the containing
// transaction.
type SpentTxOut struct {
	// Amount is the amount of the output.
	Amount int64

	// PkScript is the public key script for the output.
	PkScript []byte

	// Height is the height of the block containing the creating tx.
	Height int32

	// Denotes if the creating tx is a coinbase.
	IsCoinBase bool
}

// FetchSpendJournal attempts to retrieve the spend journal, or the set of
// outputs spent for the target block. This provides a view of all the outputs
// that will be consumed once the target block is connected to the end of the
// main chain.
//
// This function is safe for concurrent access.
func (b *BlockChain) FetchSpendJournal(targetBlock *btcutil.Block) ([]SpentTxOut, error) {
	b.chainLock.RLock()
	defer b.chainLock.RUnlock()

	var spendEntries []SpentTxOut
	err := b.db.View(func(dbTx database.Tx) error {
		var err error

		spendEntries, err = dbFetchSpendJournalEntry(dbTx, targetBlock)
		return err
	})
	if err != nil {
		return nil, err
	}

	return spendEntries, nil
}

// FetchSpendJournals retrieves the spend journals for a batch of blocks in a
// single database view, cutting the per-block read-transaction overhead during
// a full index rebuild.  The returned slice is parallel to blocks.
//
// This function is safe for concurrent access.
func (b *BlockChain) FetchSpendJournals(blocks []*btcutil.Block) ([][]SpentTxOut, error) {
	b.chainLock.RLock()
	defer b.chainLock.RUnlock()

	journals := make([][]SpentTxOut, len(blocks))
	err := b.db.View(func(dbTx database.Tx) error {
		for i, block := range blocks {
			entries, err := dbFetchSpendJournalEntry(dbTx, block)
			if err != nil {
				return err
			}
			journals[i] = entries
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return journals, nil
}

// spentTxOutHeaderCode returns the calculated header code to be used when
// serializing the provided stxo entry.
func spentTxOutHeaderCode(stxo *SpentTxOut) uint64 {
	// As described in the serialization format comments, the header code
	// encodes the height shifted over one bit and the coinbase flag in the
	// lowest bit.
	headerCode := uint64(stxo.Height) << 1
	if stxo.IsCoinBase {
		headerCode |= 0x01
	}

	return headerCode
}

// spentTxOutSerializeSize returns the number of bytes it would take to
// serialize the passed stxo according to the format described above.
func spentTxOutSerializeSize(stxo *SpentTxOut) int {
	size := serializeSizeVLQ(spentTxOutHeaderCode(stxo))
	if stxo.Height > 0 {
		// The legacy v1 spend journal format conditionally tracked the
		// containing transaction version when the height was non-zero,
		// so this is required for backwards compat.
		size += serializeSizeVLQ(0)
	}
	return size + compressedTxOutSize(uint64(stxo.Amount), stxo.PkScript)
}

// putSpentTxOut serializes the passed stxo according to the format described
// above directly into the passed target byte slice.  The target byte slice must
// be at least large enough to handle the number of bytes returned by the
// SpentTxOutSerializeSize function or it will panic.
func putSpentTxOut(target []byte, stxo *SpentTxOut) int {
	headerCode := spentTxOutHeaderCode(stxo)
	offset := putVLQ(target, headerCode)
	if stxo.Height > 0 {
		// The legacy v1 spend journal format conditionally tracked the
		// containing transaction version when the height was non-zero,
		// so this is required for backwards compat.
		offset += putVLQ(target[offset:], 0)
	}
	return offset + putCompressedTxOut(target[offset:], uint64(stxo.Amount),
		stxo.PkScript)
}

// decodeSpentTxOut decodes the passed serialized stxo entry, possibly followed
// by other data, into the passed stxo struct.  It returns the number of bytes
// read.
func decodeSpentTxOut(serialized []byte, stxo *SpentTxOut) (int, error) {
	// Ensure there are bytes to decode.
	if len(serialized) == 0 {
		return 0, errDeserialize("no serialized bytes")
	}

	// Deserialize the header code.
	code, offset := deserializeVLQ(serialized)
	if offset >= len(serialized) {
		return offset, errDeserialize("unexpected end of data after " +
			"header code")
	}

	// Decode the header code.
	//
	// Bit 0 indicates containing transaction is a coinbase.
	// Bits 1-x encode height of containing transaction.
	stxo.IsCoinBase = code&0x01 != 0
	stxo.Height = int32(code >> 1)
	if stxo.Height > 0 {
		// The legacy v1 spend journal format conditionally tracked the
		// containing transaction version when the height was non-zero,
		// so this is required for backwards compat.
		_, bytesRead := deserializeVLQ(serialized[offset:])
		offset += bytesRead
		if offset >= len(serialized) {
			return offset, errDeserialize("unexpected end of data " +
				"after reserved")
		}
	}

	// Decode the compressed txout.
	amount, pkScript, bytesRead, err := decodeCompressedTxOut(
		serialized[offset:])
	offset += bytesRead
	if err != nil {
		return offset, errDeserialize(fmt.Sprintf("unable to decode "+
			"txout: %v", err))
	}
	stxo.Amount = int64(amount)
	stxo.PkScript = pkScript
	return offset, nil
}

// deserializeSpendJournalEntry decodes the passed serialized byte slice into a
// slice of spent txouts according to the format described in detail above.
//
// Since the serialization format is not self describing, as noted in the
// format comments, this function also requires the transactions that spend the
// txouts.
func deserializeSpendJournalEntry(serialized []byte, txns []*wire.MsgTx) ([]SpentTxOut, error) {
	// Calculate the total number of stxos.
	var numStxos int
	for _, tx := range txns {
		numStxos += len(tx.TxIn)
	}

	// When a block has no spent txouts there is nothing to serialize.
	if len(serialized) == 0 {
		// Ensure the block actually has no stxos.  This should never
		// happen unless there is database corruption or an empty entry
		// erroneously made its way into the database.
		if numStxos != 0 {
			return nil, AssertError(fmt.Sprintf("mismatched spend "+
				"journal serialization - no serialization for "+
				"expected %d stxos", numStxos))
		}

		return nil, nil
	}

	// Loop backwards through all transactions so everything is read in
	// reverse order to match the serialization order.
	stxoIdx := numStxos - 1
	offset := 0
	stxos := make([]SpentTxOut, numStxos)
	for txIdx := len(txns) - 1; txIdx > -1; txIdx-- {
		tx := txns[txIdx]

		// Loop backwards through all of the transaction inputs and read
		// the associated stxo.
		for txInIdx := len(tx.TxIn) - 1; txInIdx > -1; txInIdx-- {
			txIn := tx.TxIn[txInIdx]
			stxo := &stxos[stxoIdx]
			stxoIdx--

			n, err := decodeSpentTxOut(serialized[offset:], stxo)
			offset += n
			if err != nil {
				return nil, errDeserialize(fmt.Sprintf("unable "+
					"to decode stxo for %v: %v",
					txIn.PreviousOutPoint, err))
			}
		}
	}

	return stxos, nil
}

// serializeSpendJournalEntry serializes all of the passed spent txouts into a
// single byte slice according to the format described in detail above.
func serializeSpendJournalEntry(stxos []SpentTxOut) []byte {
	if len(stxos) == 0 {
		return nil
	}

	// Calculate the size needed to serialize the entire journal entry.
	var size int
	for i := range stxos {
		size += spentTxOutSerializeSize(&stxos[i])
	}
	serialized := make([]byte, size)

	// Serialize each individual stxo directly into the slice in reverse
	// order one after the other.
	var offset int
	for i := len(stxos) - 1; i > -1; i-- {
		offset += putSpentTxOut(serialized[offset:], &stxos[i])
	}

	return serialized
}

// dbFetchSpendJournalEntry fetches the spend journal entry for the passed block
// and deserializes it into a slice of spent txout entries.
//
// NOTE: Legacy entries will not have the coinbase flag or height set unless it
// was the final output spend in the containing transaction.  It is up to the
// caller to handle this properly by looking the information up in the utxo set.
func dbFetchSpendJournalEntry(dbTx database.Tx, block *btcutil.Block) ([]SpentTxOut, error) {
	// Exclude the coinbase transaction since it can't spend anything.
	spendBucket := dbTx.Metadata().Bucket(spendJournalBucketName)
	serialized := spendBucket.Get(block.Hash()[:])
	blockTxns := block.MsgBlock().Transactions[1:]
	stxos, err := deserializeSpendJournalEntry(serialized, blockTxns)
	if err != nil {
		// Ensure any deserialization errors are returned as database
		// corruption errors.
		if isDeserializeErr(err) {
			return nil, database.Error{
				ErrorCode: database.ErrCorruption,
				Description: fmt.Sprintf("corrupt spend "+
					"information for %v: %v", block.Hash(),
					err),
			}
		}

		return nil, err
	}

	return stxos, nil
}

// dbPutSpendJournalEntry uses an existing database transaction to update the
// spend journal entry for the given block hash using the provided slice of
// spent txouts.   The spent txouts slice must contain an entry for every txout
// the transactions in the block spend in the order they are spent.
func dbPutSpendJournalEntry(dbTx database.Tx, blockHash *chainhash.Hash, stxos []SpentTxOut) error {
	spendBucket := dbTx.Metadata().Bucket(spendJournalBucketName)
	serialized := serializeSpendJournalEntry(stxos)
	return spendBucket.Put(blockHash[:], serialized)
}

// dbRemoveSpendJournalEntry uses an existing database transaction to remove the
// spend journal entry for the passed block hash.
func dbRemoveSpendJournalEntry(dbTx database.Tx, blockHash *chainhash.Hash) error {
	spendBucket := dbTx.Metadata().Bucket(spendJournalBucketName)
	return spendBucket.Delete(blockHash[:])
}

// dbPruneSpendJournalEntry uses an existing database transaction to remove all
// the spend journal entries for the pruned blocks.
func dbPruneSpendJournalEntry(dbTx database.Tx, blockHashes []chainhash.Hash) error {
	spendBucket := dbTx.Metadata().Bucket(spendJournalBucketName)

	for _, blockHash := range blockHashes {
		err := spendBucket.Delete(blockHash[:])
		if err != nil {
			return err
		}
	}

	return nil
}

// -----------------------------------------------------------------------------
// The unspent transaction output (utxo) set consists of an entry for each
// unspent output using a format that is optimized to reduce space using domain
// specific compression algorithms.  This format is a slightly modified version
// of the format used in Bitcoin Core.
//
// Each entry is keyed by an outpoint as specified below.  It is important to
// note that the key encoding uses a VLQ, which employs an MSB encoding so
// iteration of utxos when doing byte-wise comparisons will produce them in
// order.
//
// The serialized key format is:
//   <hash><output index>
//
//   Field                Type             Size
//   hash                 chainhash.Hash   chainhash.HashSize
//   output index         VLQ              variable
//
// The serialized value format is:
//
//   <header code><compressed txout>
//
//   Field                Type     Size
//   header code          VLQ      variable
//   compressed txout
//     compressed amount  VLQ      variable
//     compressed script  []byte   variable
//
// The serialized header code format is:
//   bit 0 - containing transaction is a coinbase
//   bits 1-x - height of the block that contains the unspent txout
//
// Example 1:
// From tx in main blockchain:
// Blk 1, 0e3e2357e806b6cdb1f70b54c3a3a17b6714ee1f0e68bebb44a74b1efd512098:0
//
//    03320496b538e853519c726a2c91e61ec11600ae1390813a627c66fb8be7947be63c52
//    <><------------------------------------------------------------------>
//     |                                          |
//   header code                         compressed txout
//
//  - header code: 0x03 (coinbase, height 1)
//  - compressed txout:
//    - 0x32: VLQ-encoded compressed amount for 5000000000 (50 BTC)
//    - 0x04: special script type pay-to-pubkey
//    - 0x96...52: x-coordinate of the pubkey
//
// Example 2:
// From tx in main blockchain:
// Blk 113931, 4a16969aa4764dd7507fc1de7f0baa4850a246de90c45e59a3207f9a26b5036f:2
//
//    8cf316800900b8025be1b3efc63b0ad48e7f9f10e87544528d58
//    <----><------------------------------------------>
//      |                             |
//   header code             compressed txout
//
//  - header code: 0x8cf316 (not coinbase, height 113931)
//  - compressed txout:
//    - 0x8009: VLQ-encoded compressed amount for 15000000 (0.15 BTC)
//    - 0x00: special script type pay-to-pubkey-hash
//    - 0xb8...58: pubkey hash
//
// Example 3:
// From tx in main blockchain:
// Blk 338156, 1b02d1c8cfef60a189017b9a420c682cf4a0028175f2f563209e4ff61c8c3620:22
//
//    a8a2588ba5b9e763011dd46a006572d820e448e12d2bbb38640bc718e6
//    <----><-------------------------------------------------->
//      |                             |
//   header code             compressed txout
//
//  - header code: 0xa8a258 (not coinbase, height 338156)
//  - compressed txout:
//    - 0x8ba5b9e763: VLQ-encoded compressed amount for 366875659 (3.66875659 BTC)
//    - 0x01: special script type pay-to-script-hash
//    - 0x1d...e6: script hash
// -----------------------------------------------------------------------------

// maxUint32VLQSerializeSize is the maximum number of bytes a max uint32 takes
// to serialize as a VLQ.
var maxUint32VLQSerializeSize = serializeSizeVLQ(1<<32 - 1)

// outpointKeyPool defines a concurrent safe free list of byte slices used to
// provide temporary buffers for outpoint database keys.
var outpointKeyPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, chainhash.HashSize+maxUint32VLQSerializeSize)
		return &b // Pointer to slice to avoid boxing alloc.
	},
}

// outpointKey returns a key suitable for use as a database key in the utxo set
// while making use of a free list.  A new buffer is allocated if there are not
// already any available on the free list.  The returned byte slice should be
// returned to the free list by using the recycleOutpointKey function when the
// caller is done with it _unless_ the slice will need to live for longer than
// the caller can calculate such as when used to write to the database.
func outpointKey(outpoint wire.OutPoint) *[]byte {
	// A VLQ employs an MSB encoding, so they are useful not only to reduce
	// the amount of storage space, but also so iteration of utxos when
	// doing byte-wise comparisons will produce them in order.
	key := outpointKeyPool.Get().(*[]byte)
	idx := uint64(outpoint.Index)
	*key = (*key)[:chainhash.HashSize+serializeSizeVLQ(idx)]
	copy(*key, outpoint.Hash[:])
	putVLQ((*key)[chainhash.HashSize:], idx)
	return key
}

// recycleOutpointKey puts the provided byte slice, which should have been
// obtained via the outpointKey function, back on the free list.
func recycleOutpointKey(key *[]byte) {
	outpointKeyPool.Put(key)
}

// deserializeOutpointKey decodes a database key produced by outpointKey back
// into the outpoint it represents.  The key layout is the 32-byte tx hash
// followed by the VLQ-encoded output index.
func deserializeOutpointKey(key []byte) (*wire.OutPoint, error) {
	if len(key) < chainhash.HashSize {
		return nil, AssertError(fmt.Sprintf("invalid outpoint key length %d",
			len(key)))
	}

	index, numBytes := deserializeVLQ(key[chainhash.HashSize:])
	if numBytes == 0 || chainhash.HashSize+numBytes != len(key) {
		return nil, AssertError(fmt.Sprintf("invalid outpoint key length %d",
			len(key)))
	}

	hash, err := chainhash.NewHash(key[:chainhash.HashSize])
	if err != nil {
		return nil, err
	}

	return &wire.OutPoint{Hash: *hash, Index: uint32(index)}, nil
}

// utxoEntryHeaderCode returns the calculated header code to be used when
// serializing the provided utxo entry.
func utxoEntryHeaderCode(entry *UtxoEntry) (uint64, error) {
	if entry.IsSpent() {
		return 0, AssertError("attempt to serialize spent utxo header")
	}

	// As described in the serialization format comments, the header code
	// encodes the height shifted over one bit and the coinbase flag in the
	// lowest bit.
	headerCode := uint64(entry.BlockHeight()) << 1
	if entry.IsCoinBase() {
		headerCode |= 0x01
	}

	return headerCode, nil
}

// serializeUtxoEntry returns the entry serialized to a format that is suitable
// for long-term storage.  The format is described in detail above.
func serializeUtxoEntry(entry *UtxoEntry) ([]byte, error) {
	// Spent outputs have no serialization.
	if entry.IsSpent() {
		return nil, nil
	}

	// Encode the header code.
	headerCode, err := utxoEntryHeaderCode(entry)
	if err != nil {
		return nil, err
	}

	// Calculate the size needed to serialize the entry.
	size := serializeSizeVLQ(headerCode) +
		compressedTxOutSize(uint64(entry.Amount()), entry.PkScript())

	// Serialize the header code followed by the compressed unspent
	// transaction output.
	serialized := make([]byte, size)
	offset := putVLQ(serialized, headerCode)
	offset += putCompressedTxOut(serialized[offset:], uint64(entry.Amount()),
		entry.PkScript())

	return serialized, nil
}

// deserializeUtxoEntry decodes a utxo entry from the passed serialized byte
// slice into a new UtxoEntry using a format that is suitable for long-term
// storage.  The format is described in detail above.
func deserializeUtxoEntry(serialized []byte) (*UtxoEntry, error) {
	// Deserialize the header code.
	code, offset := deserializeVLQ(serialized)
	if offset >= len(serialized) {
		return nil, errDeserialize("unexpected end of data after header")
	}

	// Decode the header code.
	//
	// Bit 0 indicates whether the containing transaction is a coinbase.
	// Bits 1-x encode height of containing transaction.
	isCoinBase := code&0x01 != 0
	blockHeight := int32(code >> 1)

	// Decode the compressed unspent transaction output.
	amount, pkScript, _, err := decodeCompressedTxOut(serialized[offset:])
	if err != nil {
		return nil, errDeserialize(fmt.Sprintf("unable to decode "+
			"utxo: %v", err))
	}

	entry := &UtxoEntry{
		amount:      int64(amount),
		pkScript:    pkScript,
		blockHeight: blockHeight,
		packedFlags: 0,
	}
	if isCoinBase {
		entry.packedFlags |= tfCoinBase
	}

	return entry, nil
}

// dbFetchUtxoEntryByHash attempts to find and fetch a utxo for the given hash.
// It uses a cursor and seek to try and do this as efficiently as possible.
//
// When there are no entries for the provided hash, nil will be returned for the
// both the entry and the error.
func dbFetchUtxoEntryByHash(dbTx database.Tx, hash *chainhash.Hash) (*UtxoEntry, error) {
	// Attempt to find an entry by seeking for the hash along with a zero
	// index.  Due to the fact the keys are serialized as <hash><index>,
	// where the index uses an MSB encoding, if there are any entries for
	// the hash at all, one will be found.
	cursor := dbTx.Metadata().Bucket(utxoSetBucketName).Cursor()
	key := outpointKey(wire.OutPoint{Hash: *hash, Index: 0})
	ok := cursor.Seek(*key)
	recycleOutpointKey(key)
	if !ok {
		return nil, nil
	}

	// An entry was found, but it could just be an entry with the next
	// highest hash after the requested one, so make sure the hashes
	// actually match.
	cursorKey := cursor.Key()
	if len(cursorKey) < chainhash.HashSize {
		return nil, nil
	}
	if !bytes.Equal(hash[:], cursorKey[:chainhash.HashSize]) {
		return nil, nil
	}

	return deserializeUtxoEntry(cursor.Value())
}

// dbFetchUtxoEntry uses an existing database transaction to fetch the specified
// transaction output from the utxo set.
//
// When there is no entry for the provided output, nil will be returned for both
// the entry and the error.
func dbFetchUtxoEntry(dbTx database.Tx, utxoBucket database.Bucket,
	outpoint wire.OutPoint) (*UtxoEntry, error) {

	// Fetch the unspent transaction output information for the passed
	// transaction output.  Return now when there is no entry.
	key := outpointKey(outpoint)
	serializedUtxo := utxoBucket.Get(*key)
	recycleOutpointKey(key)
	if serializedUtxo == nil {
		return nil, nil
	}

	// A non-nil zero-length entry means there is an entry in the database
	// for a spent transaction output which should never be the case.
	if len(serializedUtxo) == 0 {
		return nil, AssertError(fmt.Sprintf("database contains entry "+
			"for spent tx output %v", outpoint))
	}

	// Deserialize the utxo entry and return it.
	entry, err := deserializeUtxoEntry(serializedUtxo)
	if err != nil {
		// Ensure any deserialization errors are returned as database
		// corruption errors.
		if isDeserializeErr(err) {
			return nil, database.Error{
				ErrorCode: database.ErrCorruption,
				Description: fmt.Sprintf("corrupt utxo entry "+
					"for %v: %v", outpoint, err),
			}
		}

		return nil, err
	}

	return entry, nil
}

// dbPutUtxoView uses an existing database transaction to update the utxo set
// in the database based on the provided utxo view contents and state.  In
// particular, only the entries that have been marked as modified are written
// to the database.
func dbPutUtxoView(dbTx database.Tx, view *UtxoViewpoint) error {
	// Return early if the view is nil.
	if view == nil {
		return nil
	}

	utxoBucket := dbTx.Metadata().Bucket(utxoSetBucketName)
	for outpoint, entry := range view.entries {
		// No need to update the database if the entry was not modified.
		if entry == nil || !entry.isModified() {
			continue
		}

		// Remove the utxo entry if it is spent.
		if entry.IsSpent() {
			err := dbDeleteUtxoEntry(utxoBucket, outpoint)
			if err != nil {
				return err
			}
		} else {
			err := dbPutUtxoEntry(utxoBucket, outpoint, entry)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// dbDeleteUtxoEntry uses an existing database transaction to delete the utxo
// entry from the database.
func dbDeleteUtxoEntry(utxoBucket database.Bucket, outpoint wire.OutPoint) error {
	key := outpointKey(outpoint)
	err := utxoBucket.Delete(*key)
	recycleOutpointKey(key)
	return err
}

// dbPutUtxoEntry uses an existing database transaction to update the utxo entry
// in the database.
func dbPutUtxoEntry(utxoBucket database.Bucket, outpoint wire.OutPoint,
	entry *UtxoEntry) error {

	if entry == nil || entry.IsSpent() {
		return AssertError("trying to store nil or spent entry")
	}

	// Serialize and store the utxo entry.
	serialized, err := serializeUtxoEntry(entry)
	if err != nil {
		return err
	}
	key := outpointKey(outpoint)
	err = utxoBucket.Put(*key, serialized)
	if err != nil {
		return err
	}

	// NOTE: The key is intentionally not recycled here since the
	// database interface contract prohibits modifications.  It will
	// be garbage collected normally when the database is done with
	// it.
	return nil
}

// -----------------------------------------------------------------------------
// The block index consists of two buckets with an entry for every block in the
// main chain.  One bucket is for the hash to height mapping and the other is
// for the height to hash mapping.
//
// The serialized format for values in the hash to height bucket is:
//   <height>
//
//   Field      Type     Size
//   height     uint32   4 bytes
//
// The serialized format for values in the height to hash bucket is:
//   <hash>
//
//   Field      Type             Size
//   hash       chainhash.Hash   chainhash.HashSize
// -----------------------------------------------------------------------------

// dbPutBlockIndex uses an existing database transaction to update or add the
// block index entries for the hash to height and height to hash mappings for
// the provided values.
func dbPutBlockIndex(dbTx database.Tx, hash *chainhash.Hash, height int32) error {
	// Serialize the height for use in the index entries.
	var serializedHeight [4]byte
	byteOrder.PutUint32(serializedHeight[:], uint32(height))

	// Add the block hash to height mapping to the index.
	meta := dbTx.Metadata()
	hashIndex := meta.Bucket(hashIndexBucketName)
	if err := hashIndex.Put(hash[:], serializedHeight[:]); err != nil {
		return err
	}

	// Add the block height to hash mapping to the index.
	heightIndex := meta.Bucket(heightIndexBucketName)
	return heightIndex.Put(serializedHeight[:], hash[:])
}

// dbPutHashIndex uses an existing database transaction to add a block hash to
// height mapping to the hash index bucket.  Unlike dbPutBlockIndex, it does not
// write the height to hash mapping: it is used when flushing header-only block
// nodes whose height in the height index must stay owned by the main chain.
func dbPutHashIndex(dbTx database.Tx, hash *chainhash.Hash, height int32) error {
	var serializedHeight [4]byte
	byteOrder.PutUint32(serializedHeight[:], uint32(height))
	meta := dbTx.Metadata()
	hashIndex := meta.Bucket(hashIndexBucketName)
	return hashIndex.Put(hash[:], serializedHeight[:])
}

// dbPutHeightIndex uses an existing database transaction to add a block height
// to hash mapping to the height index bucket.  It is the inverse of
// dbPutHashIndex and is used when flushing header-only block nodes that sit on
// the best header chain so the DB cold-read fallback can resolve any evicted
// height back to its header hash.
func dbPutHeightIndex(dbTx database.Tx, height int32, hash *chainhash.Hash) error {
	var serializedHeight [4]byte
	byteOrder.PutUint32(serializedHeight[:], uint32(height))
	meta := dbTx.Metadata()
	heightIndex := meta.Bucket(heightIndexBucketName)
	return heightIndex.Put(serializedHeight[:], hash[:])
}

// dbRemoveHeightIndex uses an existing database transaction to delete the
// height to hash mapping for the provided height from the height index bucket.
// It is the inverse of dbPutHeightIndex and is used when a fabricated or
// forked header chain is rolled back: the stale height rows above the rollback
// point must be removed so the DB cold-read fallback can never resolve an
// evicted height back to a hash of the discarded chain (which would otherwise
// keep feeding the pollution into header sync after the rollback).  Unlike
// dbRemoveBlockIndex it only removes the height→hash direction; the hash→height
// rows for side-chain / orphan hashes are left intact.
func dbRemoveHeightIndex(dbTx database.Tx, height int32) error {
	var serializedHeight [4]byte
	byteOrder.PutUint32(serializedHeight[:], uint32(height))
	meta := dbTx.Metadata()
	heightIndex := meta.Bucket(heightIndexBucketName)
	return heightIndex.Delete(serializedHeight[:])
}

// dbRemoveBlockIndex uses an existing database transaction remove block index
// entries from the hash to height and height to hash mappings for the provided
// values.
func dbRemoveBlockIndex(dbTx database.Tx, hash *chainhash.Hash, height int32) error {
	// Remove the block hash to height mapping.
	meta := dbTx.Metadata()
	hashIndex := meta.Bucket(hashIndexBucketName)
	if err := hashIndex.Delete(hash[:]); err != nil {
		return err
	}

	// Remove the block height to hash mapping.
	var serializedHeight [4]byte
	byteOrder.PutUint32(serializedHeight[:], uint32(height))
	heightIndex := meta.Bucket(heightIndexBucketName)
	return heightIndex.Delete(serializedHeight[:])
}

// dbFetchHeightByHash uses an existing database transaction to retrieve the
// height for the provided hash from the index.
func dbFetchHeightByHash(dbTx database.Tx, hash *chainhash.Hash) (int32, error) {
	meta := dbTx.Metadata()
	hashIndex := meta.Bucket(hashIndexBucketName)
	serializedHeight := hashIndex.Get(hash[:])
	if serializedHeight == nil {
		str := fmt.Sprintf("block %s is not in the main chain", hash)
		return 0, errNotInMainChain(str)
	}

	return int32(byteOrder.Uint32(serializedHeight)), nil
}

// heightIndexFlushBatch is the maximum number of best-header-chain rows written
// to the height index within a single write transaction when rebuilding it.  The
// ffldb write transaction buffers every pending write in memory until it is
// committed, so a single transaction over the whole ~43.7M-header Sugarchain
// chain would balloon the process's memory; batching keeps the in-memory buffer
// bounded to this many rows.
const heightIndexFlushBatch = 50000

// rebuildHeightIndex reconstructs the height-to-hash mapping for every height
// on the best header chain from the persisted block index.  It upgrades
// databases created before the height index was maintained for header-only
// nodes, so the cold-read fallback can resolve evicted heights without
// re-downloading the chain.
//
// The walk descends in height order, matching each row against the expected
// best-header-chain hash, and commits every heightIndexFlushBatch rows so the
// write transaction's in-memory buffer stays bounded.  Every height's
// best-header-chain hash overwrites any previously stored value, and side-chain
// rows are skipped so they never claim a main-chain height.
func (b *BlockChain) rebuildHeightIndex(tipHash *chainhash.Hash,
	tipHeight int32) error {

	expected := *tipHash
	for end := tipHeight; end >= 0; {
		start := end - heightIndexFlushBatch + 1
		if start < 0 {
			start = 0
		}

		var nextHeight int32
		var err error
		expected, nextHeight, err = b.rebuildHeightIndexRange(&expected, start, end)
		if err != nil {
			return err
		}
		if nextHeight < 0 {
			return nil
		}
		end = nextHeight
	}
	return nil
}

// rebuildHeightIndexRange writes the height-to-hash mapping for the best header
// chain over the inclusive height range [start, end] within a single write
// transaction and returns the hash of the best-header-chain header at height
// end-below-range (start-1), together with that height, so the caller can
// continue the walk in a fresh transaction.  A nextHeight of -1 means the walk
// has reached genesis and no further rows remain.
func (b *BlockChain) rebuildHeightIndexRange(expected *chainhash.Hash,
	start, end int32) (chainhash.Hash, int32, error) {

	var nextHash chainhash.Hash
	nextHeight := int32(-1)

	err := b.db.Update(func(dbTx database.Tx) error {
		blockIndexBucket := dbTx.Metadata().Bucket(blockIndexBucketName)
		heightIndex := dbTx.Metadata().Bucket(heightIndexBucketName)
		cursor := blockIndexBucket.Cursor()

		// curHash is the best-header-chain hash expected at height curHeight.
		// Seek directly to it (the exact key exists) so each batch starts near
		// where the previous one ended rather than re-scanning the whole index.
		curHash := *expected
		curHeight := end
		var serializedHeight [4]byte
		first := true
		for {
			if first {
				if !cursor.Seek(blockIndexKey(&curHash, uint32(curHeight))) {
					return nil
				}
				first = false
			} else if !cursor.Prev() {
				break
			}

			i := int32(binary.BigEndian.Uint32(cursor.Key()[0:4]))

			// Rows above the height we are currently matching share the last
			// matched height and are side chains; skip them since they must
			// never claim a main-chain height.
			if i > curHeight {
				continue
			}

			// The block index is ordered by height, so once the walk has
			// descended past the expected height, no further rows below can be
			// on the best header chain.
			if i < curHeight {
				return nil
			}

			header, _, err := deserializeBlockRow(cursor.Value())
			if err != nil {
				return err
			}
			hash := header.BlockHash()
			if !hash.IsEqual(&curHash) {
				// A side-chain row (or stale row from a reorg) at this height.
				continue
			}

			// Best-header-chain row: write height→hash.
			byteOrder.PutUint32(serializedHeight[:], uint32(i))
			if err := heightIndex.Put(serializedHeight[:], hash[:]); err != nil {
				return err
			}

			// Remember the row just below for the continuation.
			nextHash = header.PrevBlock
			nextHeight = i - 1

			if i <= start {
				// Reached the bottom of this batch.
				return nil
			}

			curHash = header.PrevBlock
			curHeight = i - 1
		}
		return nil
	})
	if err != nil {
		return chainhash.Hash{}, -1, err
	}
	return nextHash, nextHeight, nil
}

// dbFetchHashByHeight uses an existing database transaction to retrieve the
// hash for the provided height from the index.
func dbFetchHashByHeight(dbTx database.Tx, height int32) (*chainhash.Hash, error) {
	var serializedHeight [4]byte
	byteOrder.PutUint32(serializedHeight[:], uint32(height))

	meta := dbTx.Metadata()
	heightIndex := meta.Bucket(heightIndexBucketName)
	hashBytes := heightIndex.Get(serializedHeight[:])
	if hashBytes == nil {
		str := fmt.Sprintf("no block at height %d exists", height)
		return nil, errNotInMainChain(str)
	}

	var hash chainhash.Hash
	copy(hash[:], hashBytes)
	return &hash, nil
}

// MainChainHashByHeight returns the main-chain block hash at the given
// height directly from the DB height index.  Unlike BlockHashByHeight /
// nodeAtHeight it does NOT depend on the in-memory header window, so it
// works for any height — including a sugar-index tip that sits just above
// the window materialized from the best-tip snapshot.  Returns
// errNotInMainChain when the height has no main-chain block.
func (b *BlockChain) MainChainHashByHeight(height int32) (*chainhash.Hash, error) {
	var hash *chainhash.Hash
	err := b.db.View(func(dbTx database.Tx) error {
		var herr error
		hash, herr = dbFetchHashByHeight(dbTx, height)
		return herr
	})
	return hash, err
}

// writeLoadProgress writes the block-index load progress to
// <sugarIndexDir>/progress.json so the frontend can show it via
// /api/index-progress while the RPC server is still starting.  The format
// matches sugarindex's progress file ({height,total,percent}) plus phase
// and updatedAt so both the load and the index rebuild share one progress
// bar and an ETA can be derived from successive polls.
func (b *BlockChain) writeLoadProgress(height, total int32) {
	if b.sugarIndexDir == "" {
		return
	}
	raw, err := json.Marshal(map[string]interface{}{
		"phase":     "load",
		"height":    height,
		"total":     total,
		"percent":   float64(height) / float64(total) * 100,
		"updatedAt": time.Now().Unix(),
	})
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(b.sugarIndexDir, "progress.json"), raw, 0o600)
}

// dbFetchBlockRowByHash uses an existing database transaction to retrieve the
// block header and status for the provided hash from the block index via the
// two-hop hash to-height to-block-index mapping, without reading the block
// file.  It returns a nil header when the hash has no corresponding block index
// entry.
func dbFetchBlockRowByHash(dbTx database.Tx, hash *chainhash.Hash) (*wire.BlockHeader, blockStatus, int32, error) {
	height, err := dbFetchHeightByHash(dbTx, hash)
	if err != nil {
		return nil, statusNone, 0, err
	}

	key := blockIndexKey(hash, uint32(height))
	meta := dbTx.Metadata()
	blockIndex := meta.Bucket(blockIndexBucketName)
	row := blockIndex.Get(key)
	if row == nil {
		return nil, statusNone, 0, nil
	}

	header, status, err := deserializeBlockRow(row)
	if err != nil {
		return nil, statusNone, 0, err
	}

	return header, status, height, nil
}

// dbFetchBlockRowByHeight uses an existing database transaction to retrieve the
// main-chain block header and status for the provided height via the two-hop
// height-to-hash-to-block-index mapping, without reading the block file.  It
// returns a nil header when the height has no corresponding main-chain entry.
func dbFetchBlockRowByHeight(dbTx database.Tx, height int32) (*wire.BlockHeader, blockStatus, int32, error) {
	hash, err := dbFetchHashByHeight(dbTx, height)
	if err != nil {
		return nil, statusNone, 0, err
	}

	return dbFetchBlockRowByHash(dbTx, hash)
}

// -----------------------------------------------------------------------------
// The best chain state consists of the best block hash and height, the total
// number of transactions up to and including those in the best block, and the
// accumulated work sum up to and including the best block.
//
// The serialized format is:
//
//   <block hash><block height><total txns><work sum length><work sum>
//
//   Field             Type             Size
//   block hash        chainhash.Hash   chainhash.HashSize
//   block height      uint32           4 bytes
//   total txns        uint64           8 bytes
//   work sum length   uint32           4 bytes
//   work sum          big.Int          work sum length
// -----------------------------------------------------------------------------

// bestChainState represents the data to be stored the database for the current
// best chain state.
type bestChainState struct {
	hash      chainhash.Hash
	height    uint32
	totalTxns uint64
	workSum   *big.Int
}

// serializeBestChainState returns the serialization of the passed block best
// chain state.  This is data to be stored in the chain state bucket.
func serializeBestChainState(state bestChainState) []byte {
	// Calculate the full size needed to serialize the chain state.
	workSumBytes := state.workSum.Bytes()
	workSumBytesLen := uint32(len(workSumBytes))
	serializedLen := chainhash.HashSize + 4 + 8 + 4 + workSumBytesLen

	// Serialize the chain state.
	serializedData := make([]byte, serializedLen)
	copy(serializedData[0:chainhash.HashSize], state.hash[:])
	offset := uint32(chainhash.HashSize)
	byteOrder.PutUint32(serializedData[offset:], state.height)
	offset += 4
	byteOrder.PutUint64(serializedData[offset:], state.totalTxns)
	offset += 8
	byteOrder.PutUint32(serializedData[offset:], workSumBytesLen)
	offset += 4
	copy(serializedData[offset:], workSumBytes)
	return serializedData
}

// deserializeBestChainState deserializes the passed serialized best chain
// state.  This is data stored in the chain state bucket and is updated after
// every block is connected or disconnected form the main chain.
// block.
func deserializeBestChainState(serializedData []byte) (bestChainState, error) {
	// Ensure the serialized data has enough bytes to properly deserialize
	// the hash, height, total transactions, and work sum length.
	if len(serializedData) < chainhash.HashSize+16 {
		return bestChainState{}, database.Error{
			ErrorCode:   database.ErrCorruption,
			Description: "corrupt best chain state",
		}
	}

	state := bestChainState{}
	copy(state.hash[:], serializedData[0:chainhash.HashSize])
	offset := uint32(chainhash.HashSize)
	state.height = byteOrder.Uint32(serializedData[offset : offset+4])
	offset += 4
	state.totalTxns = byteOrder.Uint64(serializedData[offset : offset+8])
	offset += 8
	workSumBytesLen := byteOrder.Uint32(serializedData[offset : offset+4])
	offset += 4

	// Ensure the serialized data has enough bytes to deserialize the work
	// sum.
	if uint32(len(serializedData[offset:])) < workSumBytesLen {
		return bestChainState{}, database.Error{
			ErrorCode:   database.ErrCorruption,
			Description: "corrupt best chain state",
		}
	}
	workSumBytes := serializedData[offset : offset+workSumBytesLen]
	state.workSum = new(big.Int).SetBytes(workSumBytes)

	return state, nil
}

// dbPutBestState uses an existing database transaction to update the best chain
// state with the given parameters.
func dbPutBestState(dbTx database.Tx, snapshot *BestState, workSum *big.Int) error {
	// Serialize the current best chain state.
	serializedData := serializeBestChainState(bestChainState{
		hash:      snapshot.Hash,
		height:    uint32(snapshot.Height),
		totalTxns: snapshot.TotalTxns,
		workSum:   workSum,
	})

	// Store the current best chain state into the database.
	return dbTx.Metadata().Put(chainStateKeyName, serializedData)
}

// bestHeaderState represents the data to be stored in the database for the
// current best header state.
type bestHeaderState struct {
	hash   chainhash.Hash
	height uint32
}

// serializeBestHeaderState returns the serialization of the passed best header
// state.  This is data to be stored under bestHeaderStateKeyName.
func serializeBestHeaderState(state bestHeaderState) []byte {
	serializedLen := chainhash.HashSize + 4
	serializedData := make([]byte, serializedLen)
	copy(serializedData[0:chainhash.HashSize], state.hash[:])
	byteOrder.PutUint32(serializedData[chainhash.HashSize:], state.height)
	return serializedData
}

// deserializeBestHeaderState deserializes the passed serialized best header
// state.
func deserializeBestHeaderState(serializedData []byte) (bestHeaderState, error) {
	if len(serializedData) < chainhash.HashSize+4 {
		return bestHeaderState{}, database.Error{
			ErrorCode:   database.ErrCorruption,
			Description: "corrupt best header state",
		}
	}

	state := bestHeaderState{}
	copy(state.hash[:], serializedData[0:chainhash.HashSize])
	state.height = byteOrder.Uint32(serializedData[chainhash.HashSize:])
	return state, nil
}

// dbPutBestHeaderState uses an existing database transaction to update the best
// header state with the given parameters.
func dbPutBestHeaderState(dbTx database.Tx, hash *chainhash.Hash, height int32) error {
	serializedData := serializeBestHeaderState(bestHeaderState{
		hash:   *hash,
		height: uint32(height),
	})

	return dbTx.Metadata().Put(bestHeaderStateKeyName, serializedData)
}

// dbFetchBestHeaderState uses an existing database transaction to retrieve the
// best header state.  It returns nil when no state has been stored, which is
// the case for databases created by versions that predate the key.
func dbFetchBestHeaderState(dbTx database.Tx) (*bestHeaderState, error) {
	serializedData := dbTx.Metadata().Get(bestHeaderStateKeyName)
	if serializedData == nil {
		return nil, nil
	}

	state, err := deserializeBestHeaderState(serializedData)
	if err != nil {
		return nil, err
	}
	return &state, nil
}

// bestBlockDownloadState represents the data to be stored in the database for
// the furthest block whose data is present on disk.
type bestBlockDownloadState struct {
	hash   chainhash.Hash
	height uint32
}

// serializeBestBlockDownloadState returns the serialization of the passed best
// block download state.  This is data to be stored under
// blockDownloadStateKeyName.
func serializeBestBlockDownloadState(state bestBlockDownloadState) []byte {
	serializedLen := chainhash.HashSize + 4
	serializedData := make([]byte, serializedLen)
	copy(serializedData[0:chainhash.HashSize], state.hash[:])
	byteOrder.PutUint32(serializedData[chainhash.HashSize:], state.height)
	return serializedData
}

// deserializeBestBlockDownloadState deserializes the passed serialized best
// block download state.
func deserializeBestBlockDownloadState(serializedData []byte) (bestBlockDownloadState,
	error) {

	if len(serializedData) < chainhash.HashSize+4 {
		return bestBlockDownloadState{}, database.Error{
			ErrorCode:   database.ErrCorruption,
			Description: "corrupt best block download state",
		}
	}

	state := bestBlockDownloadState{}
	copy(state.hash[:], serializedData[0:chainhash.HashSize])
	state.height = byteOrder.Uint32(serializedData[chainhash.HashSize:])
	return state, nil
}

// dbPutBestBlockDownloadState uses an existing database transaction to update
// the best block download state with the given parameters.
func dbPutBestBlockDownloadState(dbTx database.Tx, hash *chainhash.Hash,
	height int32) error {

	serializedData := serializeBestBlockDownloadState(bestBlockDownloadState{
		hash:   *hash,
		height: uint32(height),
	})

	return dbTx.Metadata().Put(blockDownloadStateKeyName, serializedData)
}

// dbFetchBestBlockDownloadState uses an existing database transaction to
// retrieve the best block download state.  It returns nil when no state has
// been stored, which is the case for databases created by versions that predate
// the key.
func dbFetchBestBlockDownloadState(dbTx database.Tx) (*bestBlockDownloadState,
	error) {

	serializedData := dbTx.Metadata().Get(blockDownloadStateKeyName)
	if serializedData == nil {
		return nil, nil
	}

	state, err := deserializeBestBlockDownloadState(serializedData)
	if err != nil {
		return nil, err
	}
	return &state, nil
}

// dbPutUtxoStateConsistency uses an existing database transaction to
// update the utxo state consistency status with the given parameters.
func dbPutUtxoStateConsistency(dbTx database.Tx, hash *chainhash.Hash) error {
	// Store the utxo state consistency status into the database.
	return dbTx.Metadata().Put(utxoStateConsistencyKeyName, hash[:])
}

// dbFetchUtxoStateConsistency uses an existing database transaction to retrieve
// the utxo state consistency status from the database.  The code is 0 when
// nothing was found.
func dbFetchUtxoStateConsistency(dbTx database.Tx) []byte {
	// Fetch the serialized data from the database.
	statusBytes := dbTx.Metadata().Get(utxoStateConsistencyKeyName)
	if statusBytes != nil {
		result := make([]byte, len(statusBytes))
		copy(result, statusBytes)
		return result
	}

	return nil
}

// createChainState initializes both the database and the chain state to the
// genesis block.  This includes creating the necessary buckets and inserting
// the genesis block, so it must only be called on an uninitialized database.
func (b *BlockChain) createChainState() error {
	// Create a new node from the genesis block and set it as the best node.
	genesisBlock := btcutil.NewBlock(b.chainParams.GenesisBlock)
	genesisBlock.SetHeight(0)
	header := &genesisBlock.MsgBlock().Header
	node := newBlockNode(header, nil)
	node.status = statusDataStored | statusValid
	b.bestChain.SetTip(node)
	b.bestHeader.SetTip(node)

	// Add the new node to the index which is used for faster lookups.
	b.index.addNode(node)

	// Initialize the state related to the best block.  Since it is the
	// genesis block, use its timestamp for the median time.
	numTxns := uint64(len(genesisBlock.MsgBlock().Transactions))
	blockSize := uint64(genesisBlock.MsgBlock().SerializeSize())
	blockWeight := uint64(GetBlockWeight(genesisBlock))
	b.stateSnapshot = newBestState(node, blockSize, blockWeight, numTxns,
		numTxns, time.Unix(node.timestamp, 0))

	// Create the initial the database chain state including creating the
	// necessary index buckets and inserting the genesis block.
	err := b.db.Update(func(dbTx database.Tx) error {
		meta := dbTx.Metadata()

		// Create the bucket that houses the block index data.
		_, err := meta.CreateBucket(blockIndexBucketName)
		if err != nil {
			return err
		}

		// Create the bucket that houses the chain block hash to height
		// index.
		_, err = meta.CreateBucket(hashIndexBucketName)
		if err != nil {
			return err
		}

		// Create the bucket that houses the chain block height to hash
		// index.
		_, err = meta.CreateBucket(heightIndexBucketName)
		if err != nil {
			return err
		}

		// Create the bucket that houses the spend journal data and
		// store its version.
		_, err = meta.CreateBucket(spendJournalBucketName)
		if err != nil {
			return err
		}
		err = dbPutVersion(dbTx, utxoSetVersionKeyName,
			latestUtxoSetBucketVersion)
		if err != nil {
			return err
		}

		// Create the bucket that houses the utxo set and store its
		// version.  Note that the genesis block coinbase transaction is
		// intentionally not inserted here since it is not spendable by
		// consensus rules.
		_, err = meta.CreateBucket(utxoSetBucketName)
		if err != nil {
			return err
		}
		err = dbPutVersion(dbTx, spendJournalVersionKeyName,
			latestSpendJournalBucketVersion)
		if err != nil {
			return err
		}

		// Save the genesis block to the block index database.
		err = dbStoreBlockNode(dbTx, node)
		if err != nil {
			return err
		}

		// Add the genesis block hash to height and height to hash
		// mappings to the index.
		err = dbPutBlockIndex(dbTx, &node.hash, node.height)
		if err != nil {
			return err
		}

		// Store the current best chain state into the database.
		err = dbPutBestState(dbTx, b.stateSnapshot, node.workSum)
		if err != nil {
			return err
		}

		// Store the current best header state (initially the genesis
		// block) into the database.
		err = dbPutBestHeaderState(dbTx, &node.hash, node.height)
		if err != nil {
			return err
		}

		// Store the genesis block into the database.
		return dbStoreBlock(dbTx, genesisBlock)
	})
	return err
}

// DBBlockFromBytes deserializes a block fetched from the local database,
// tolerating trailing bytes rather than rejecting them outright.  Databases
// written by older btcd versions may have persisted blocks with trailing
// bytes, and failing here would make such blocks permanently unreadable.
// Instead, any trailing bytes are logged, ignored, and excluded from the
// serialization cached in the returned block.
//
// This lenient parsing is only appropriate for blocks read back from the
// node's own database.  Blocks from external sources (p2p, RPC) should be
// parsed with the strict btcutil.NewBlockFromBytes instead.
func DBBlockFromBytes(blockBytes []byte, hash chainhash.Hash) (*btcutil.Block,
	error) {

	blockReader := bytes.NewReader(blockBytes)
	var msgBlock wire.MsgBlock
	if err := msgBlock.Deserialize(blockReader); err != nil {
		return nil, err
	}
	if trailing := blockReader.Len(); trailing > 0 {
		log.Debugf("Block %v has %d trailing bytes in the database; "+
			"ignoring them", hash, trailing)
		blockBytes = blockBytes[:len(blockBytes)-trailing]
	}

	// Cache the exact serialization on the block so downstream consumers
	// of the raw bytes never observe the trailing bytes.
	return btcutil.NewBlockFromBlockAndBytes(&msgBlock, blockBytes), nil
}

// initChainState attempts to load and initialize the chain state from the
// database.  When the db does not yet contain any chain state, both it and the
// chain state are initialized to the genesis block.
func (b *BlockChain) initChainState() error {
	// Determine the state of the chain database. We may need to initialize
	// everything from scratch or upgrade certain buckets.
	var initialized, hasBlockIndex bool
	err := b.db.View(func(dbTx database.Tx) error {
		initialized = dbTx.Metadata().Get(chainStateKeyName) != nil
		hasBlockIndex = dbTx.Metadata().Bucket(blockIndexBucketName) != nil
		return nil
	})
	if err != nil {
		return err
	}

	if !initialized {
		// At this point the database has not already been initialized, so
		// initialize both it and the chain state to the genesis block.
		return b.createChainState()
	}

	if !hasBlockIndex {
		err := migrateBlockIndex(b.db)
		if err != nil {
			return nil
		}
	}

	// Attempt to load the chain state from the database.
	//
	// The best header state observed here is surfaced to the caller so the
	// height-to-hash index can be rebuilt afterwards when it does not already
	// cover the stored best header chain.
	var headerStateHash chainhash.Hash
	var headerStateHeight int32
	var needHeightRebuild bool
	// useSnapshot is set when the persisted best-tip snapshot validated and
	// was used to skip the full block-index scan.  It is read again after the
	// view closes so a fresh snapshot can be persisted when the full scan
	// path ran instead (the snapshot is only written on connect otherwise).
	var useSnapshot bool
	err = b.db.View(func(dbTx database.Tx) error {
		// Fetch the stored chain state from the database metadata.
		// When it doesn't exist, it means the database hasn't been
		// initialized for use with chain yet, so break out now to allow
		// that to happen under a writable database transaction.
		serializedData := dbTx.Metadata().Get(chainStateKeyName)
		log.Tracef("Serialized chain state: %x", serializedData)
		state, err := deserializeBestChainState(serializedData)
		if err != nil {
			return err
		}

		// Restore the best header tip state before loading the block index
		// so the in-memory header window can be computed from the furthest
		// tip.  When no state exists, such as with a database created by an
		// older version, the header tip falls back to the best connected
		// chain tip as before.
		headerState, err := dbFetchBestHeaderState(dbTx)
		if err != nil {
			return err
		}
		headerTipHeight := int32(state.height)
		if headerState != nil {
			headerTipHeight = int32(headerState.height)
		}

		// Restore the furthest stored block so a restart can resume the block
		// download from the highest locally-available block instead of
		// re-scanning every height below it.  When no state exists, such as
		// with a database created by an older version, it stays nil.
		b.blockDownload, err = dbFetchBestBlockDownloadState(dbTx)
		if err != nil {
			return err
		}

		// Determine the in-memory header window boundaries.  When the window
		// is enabled, only the most recent windowSize blocks from each tip
		// are materialized in memory: the connected chain keeps its own
		// trailing window (during a header sync the best chain tip can sit
		// far below the header tip), and the header chain keeps the trailing
		// window of the furthest tip.  Every older row is accumulated only as
		// running work so the node at each window boundary carries the
		// correct cumulative workSum.  A windowSize of zero materializes the
		// entire index as before.
		chainBoundary := b.index.windowBoundary(int32(state.height))
		headerBoundary := b.index.windowBoundary(headerTipHeight)

		// Load the headers from the data for the known best chain and
		// construct the block index accordingly.
		log.Infof("Loading block index...")

		blockIndexBucket := dbTx.Metadata().Bucket(blockIndexBucketName)

		var i int32
		var lastNode *blockNode

		// runningWorkSum accumulates the cumulative chain work of every row
		// below the header window boundary so the boundary node can be
		// seeded with the correct workSum even though its parent is not
		// materialized.
		var runningWorkSum *big.Int

		// Attempt to load the block index from the persisted best-tip snapshot
		// to skip the full 43.8M-row scan.  The snapshot is a pure cache: it
		// is validated against the DB height index and falls back to the full
		// scan below on any mismatch.
		snapHash, snapHeight, snapWork, snapOK, serr := dbFetchBestTipSnapshot(dbTx)
		if serr != nil {
			return serr
		}
		useSnapshot = false
		if snapOK {
			mainHash, herr := dbFetchHashByHeight(dbTx, snapHeight)
			if herr == nil && mainHash != nil && *mainHash == snapHash {
				useSnapshot = true
			}
		}

		if useSnapshot {
			// Snapshot path: materialize only the in-memory window, walking
			// back from the snapshot tip via the DB height index.  The window
			// boundary anchor is seeded with the snapshot's cumulative work so
			// consensus walks see the correct workSum.
			log.Infof("Loading block index from snapshot (height %d)...",
				snapHeight)
			startH := b.index.windowBoundary(snapHeight)
			if startH < 0 {
				startH = 0
			}
			i = startH
			runningWorkSum = new(big.Int).Set(snapWork)
			for h := startH; h <= snapHeight; h++ {
				hash, herr := dbFetchHashByHeight(dbTx, h)
				if herr != nil {
					log.Warnf("Snapshot height %d: %v; falling back to full scan", h, herr)
					useSnapshot = false
					i = 0
					lastNode = nil
					runningWorkSum = nil
					break
				}
				header, status, _, rerr := dbFetchBlockRowByHash(dbTx, hash)
				if rerr != nil {
					log.Warnf("Snapshot height %d hash %s: %v; falling back to full scan", h, hash, rerr)
					useSnapshot = false
					i = 0
					lastNode = nil
					runningWorkSum = nil
					break
				}
				var parent *blockNode
				if lastNode != nil && header.PrevBlock == lastNode.hash {
					parent = lastNode
				} else if parent = b.index.LookupNode(&header.PrevBlock); parent != nil {
					// Side chain within the window.
				}
				node := new(blockNode)
				initBlockNode(node, header, parent)
				node.height = h
				node.status = status
				if h == startH {
					// Boundary anchor: seed with the snapshot's cumulative
					// work (initBlockNode allocated a pooled workSum).
					node.workSum = node.workSum.Set(snapWork)
				}
				b.index.addNode(node)
				lastNode = node
				i = h
			}
		}
		if !useSnapshot {
			cursor := blockIndexBucket.Cursor()
			for ok := cursor.First(); ok; ok = cursor.Next() {
				header, status, err := deserializeBlockRow(cursor.Value())
				if err != nil {
					// A single torn row (e.g. a crash mid-write) must not
					// abort the whole startup.  Log the row's key (height +
					// hash) and skip it; the block will be re-downloaded and
					// re-indexed by the sync machinery.
					// 单条撕裂行(如强杀时写了一半)不应中止整个启动。记录该行
					// 的 key(高度+hash)并跳过;缺失块由同步机制重新下载索引。
					key := cursor.Key()
					keyHeight := int32(-1)
					var keyHash chainhash.Hash
					if len(key) >= chainhash.HashSize+4 {
						keyHeight = int32(binary.BigEndian.Uint32(key[0:4]))
						copy(keyHash[:], key[4:chainhash.HashSize+4])
					}
					log.Warnf("Skipping corrupt block index row "+
						"(height=%d hash=%s): %v", keyHeight, keyHash, err)
					i++
					continue
				}

				// The row counter i is NOT the block height: the block index
				// can be missing rows (a torn write dropped the tail), so i
				// stays far below the window boundary while the real heights
				// are above it.  Parse the true height from the key and use
				// it for every height-based decision below.
				// 行计数 i 不是真实高度:block index 可能缺行(撕裂写丢了尾部),
				// 导致 i 远低于窗口边界而真实高度在其之上。从 key 解析真实
				// 高度,下面所有基于高度的判断都用它。
				rowKey := cursor.Key()
				rowHeight := int32(-1)
				if len(rowKey) >= chainhash.HashSize+4 {
					rowHeight = int32(binary.BigEndian.Uint32(rowKey[0:4]))
				}

				// Accumulate the work of every row below the header window
				// boundary, materialized or not, so the running total is correct
				// at whichever boundary anchor consumes it.
				if rowHeight < headerBoundary {
					if runningWorkSum == nil {
						runningWorkSum = CalcWork(header.Bits)
					} else {
						runningWorkSum.Add(runningWorkSum, CalcWork(header.Bits))
					}
				}

				// Materialize this row when it falls within either the connected
				// chain's window or the header chain's window.
				//
				// The connected chain's window extends a full window ahead of the
				// best chain tip.  After a restart the block-connection frontier
				// must be able to resolve the parent of every block the downloader
				// requests next (which always lives within one request window of
				// the tip) from the in-memory index, since block acceptance
				// resolves parents only through the in-memory index and never falls
				// back to the cold-read layer.
				inChainWindow := rowHeight >= chainBoundary &&
					rowHeight <= int32(state.height)+b.index.windowSize
				inHeaderWindow := rowHeight >= headerBoundary
				if !inChainWindow && !inHeaderWindow {
					i++
					continue
				}

				// Determine the parent block node. Since we iterate block headers
				// in order of height, if the blocks are mostly linear there is a
				// very good chance the previous header processed is the parent.
				var parent *blockNode
				if lastNode != nil && header.PrevBlock == lastNode.hash {
					// Since we iterate block headers in order of height, if the
					// blocks are mostly linear there is a very good chance the
					// previous header processed is the parent.
					parent = lastNode
				} else if parent = b.index.LookupNode(&header.PrevBlock); parent != nil {
					// The parent was materialized earlier within the window (a
					// side chain, or a jump between the connected chain window
					// and the header window).
				} else if rowHeight == 0 {
					// This is the very first row, which must be genesis.
					blockHash := header.BlockHash()
					if !blockHash.IsEqual(b.chainParams.GenesisHash) {
						return AssertError(fmt.Sprintf("initChainState: Expected "+
							"first entry in block index to be genesis block, "+
							"found %s", blockHash))
					}
				} else {
					// The node at a window boundary.  Its parent lives below the
					// materialized window; the parent hash is retained on the
					// node so its header can be reconstructed, and the running
					// work accumulated above provides its cumulative work.
					parent = nil
				}

				// Initialize the block node for the block, connect it,
				// and add it to the block index.
				node := new(blockNode)
				initBlockNode(node, header, parent)
				if parent == nil && rowHeight > 0 {
					// Seed the boundary anchor with the cumulative work of the
					// entire chain up to and including this row, and fix its
					// height since its parent is not materialized.  The work
					// sum value already allocated by initBlockNode is reused
					// rather than replaced so it stays recycled through the
					// block work pool.
					node.height = rowHeight
					if runningWorkSum != nil {
						node.workSum = node.workSum.Add(runningWorkSum, node.workSum)
					}
				}
				node.status = status
				b.index.addNode(node)

				lastNode = node
				i++
				if i%500000 == 0 {
					// Surface load progress (frontend shows it via
					// /api/index-progress while RPC is still starting).
					b.writeLoadProgress(i, headerStateHeight)
				}
			}
		} // end full scan fallback / 全量扫描回退

		// Set the best chain view and the best header to the stored best state.
		tip := b.index.LookupNode(&state.hash)
		if tip == nil {
			// TEMP-DBG: dump the load context so we can see why no node was
			// materialized / why lastNode is nil.
			log.Warnf("TEMP-DBG tip-missing state.hash=%s state.height=%d "+
				"useSnapshot=%v lastNodeNil=%v i=%d chainBoundary=%d "+
				"headerBoundary=%d headerTipHeight=%d",
				state.hash, state.height, useSnapshot, lastNode == nil, i,
				chainBoundary, headerBoundary, headerTipHeight)
			// The stored chain tip row is missing from the block index (e.g.
			// it was the corrupt row skipped above, or a torn write dropped
			// it).  Rather than aborting startup, fall back to the highest
			// node the scan actually materialized (lastNode) and let the
			// sync machinery re-download and reconnect the missing tail.
			// Do NOT walk the height index height-by-height here: with a
			// 44M-block chain that is tens of millions of random LevelDB
			// reads and stalls startup for hours.
			// 存储的主链 tip 行在 block index 中缺失(如正是上面跳过的损坏行,
			// 或撕裂写把它弄丢了)。不要中止启动,直接回退到扫描实际物化的
			// 最高节点(lastNode),缺失的尾部由同步机制重新下载连接。
			// 切勿在此逐高度遍历高度索引:44M 块链上那是数千万次随机读,
			// 会让启动卡死数小时。
			if lastNode != nil {
				tip = lastNode
				log.Warnf("Chain tip %s (height %d) missing from block "+
					"index; falling back to last materialized node %s "+
					"(height %d)", state.hash, state.height,
					lastNode.hash, lastNode.height)
			}
			if tip == nil {
				return AssertError(fmt.Sprintf("initChainState: cannot find "+
					"chain tip %s in block index", state.hash))
			}
		}
		b.bestChain.SetTip(tip)

		headerTip := tip
		if headerState != nil {
			headerNode := b.index.LookupNode(&headerState.hash)
			if headerNode == nil {
				log.Warnf("Best header state %s not found in block "+
					"index, falling back to best chain tip",
					headerState.hash)
			} else {
				headerTip = headerNode
			}
		}
		b.bestHeader.SetTip(headerTip)

		// Compact both views down to the in-memory header window.  The tip
		// installation above sizes each backing array to the full chain
		// height (the views are only populated with the materialized
		// window, but the array capacity covers everything below it), so
		// prune immediately to release the memory that would otherwise be
		// retained for the entire process lifetime.
		if b.headerWindow > 0 {
			b.bestChain.PruneBelow(b.index.windowBoundary(int32(state.height)))
			b.bestHeader.PruneBelow(b.index.windowBoundary(headerTip.height))
		}

		// Determine whether the persisted height-to-hash index already covers
		// the best header chain.  Databases created before the height index
		// was maintained for header-only nodes have an empty or stale index;
		// they are rebuilt once the in-memory index has been loaded so the
		// cold-read fallback can resolve evicted heights right after startup.
		if headerState != nil {
			headerStateHash = headerState.hash
			headerStateHeight = int32(headerState.height)
			storedHash, err := dbFetchHashByHeight(dbTx,
				int32(headerState.height))
			if err != nil || !storedHash.IsEqual(&headerState.hash) {
				needHeightRebuild = true
			}
		}

		// Load the raw block bytes for the best block.
		blockBytes, err := dbTx.FetchBlock(&state.hash)
		if err != nil {
			return err
		}
		block, err := DBBlockFromBytes(blockBytes, state.hash)
		if err != nil {
			return err
		}

		// As a final consistency check, we'll run through all the
		// nodes which are ancestors of the current chain tip, and mark
		// them as valid if they aren't already marked as such.  This
		// is a safe assumption as all the block before the current tip
		// are valid by definition.
		for iterNode := tip; iterNode != nil; iterNode = iterNode.parent {
			// If this isn't already marked as valid in the index, then
			// we'll mark it as valid now to ensure consistency once
			// we're up and running.
			if !iterNode.status.KnownValid() {
				log.Infof("Block %v (height=%v) ancestor of "+
					"chain tip not marked as valid, "+
					"upgrading to valid for consistency",
					iterNode.hash, iterNode.height)

				b.index.SetStatusFlags(iterNode, statusValid)
			}
		}

		// Initialize the state related to the best block.  The block
		// bytes are re-derived from the block itself so any trailing
		// bytes ignored during deserialization are excluded from the
		// recorded size.
		serializedBlock, err := block.Bytes()
		if err != nil {
			return err
		}
		blockSize := uint64(len(serializedBlock))
		blockWeight := uint64(GetBlockWeight(block))
		numTxns := uint64(len(block.MsgBlock().Transactions))
		b.stateSnapshot = newBestState(tip, blockSize, blockWeight,
			numTxns, state.totalTxns, CalcPastMedianTime(tip))

		return nil
	})
	if err != nil {
		return err
	}

	// When the full-scan path ran (no valid snapshot), persist a fresh
	// best-tip snapshot now so the next startup can skip the scan.  The
	// snapshot is only otherwise written on connect/disconnect.
	if !useSnapshot {
		if tip := b.bestChain.Tip(); tip != nil {
			if err := b.db.Update(func(dbTx database.Tx) error {
				return dbPutBestTipSnapshot(dbTx, &tip.hash, tip.height,
					tip.workSum)
			}); err != nil {
				return err
			}
			log.Infof("Persisted best-tip snapshot (height %d)", tip.height)
		}
	}

	// Rebuild the height-to-hash index for the best header chain when the
	// stored copy is missing or stale.  This is a one-time O(n) sequential
	// backfill over the persisted block index for databases that were synced
	// before the height index was maintained for header-only nodes.
	if needHeightRebuild {
		log.Infof("Rebuilding height-to-hash index for the best header "+
			"chain (height %d)...", headerStateHeight)
		if err := b.rebuildHeightIndex(&headerStateHash, headerStateHeight); err != nil {
			return err
		}
	}

	// As we might have updated the index after it was loaded, we'll
	// attempt to flush the index to the DB. This will only result in a
	// write if the elements are dirty, so it'll usually be a noop.
	return b.index.flushToDB(true)
}

// deserializeBlockRow parses a value in the block index bucket into a block
// header and block status bitfield.
// bestTipSnapshotKey is the key under which the best-tip snapshot is stored
// in the blockheaderidx bucket.  It is a pure cache used to skip the full
// 43.8M-row block-index scan at startup: the DB height index remains the
// source of truth, and the snapshot is validated (height -> main-chain hash
// comparison) before being trusted, falling back to a full scan otherwise.
var bestTipSnapshotKey = []byte("\x00best_tip_snapshot")

// dbFetchBestTipSnapshot loads the persisted best-tip snapshot.  Returns
// ok=false when absent or malformed.
func dbFetchBestTipSnapshot(dbTx database.Tx) (hash chainhash.Hash, height int32, work *big.Int, ok bool, err error) {
	meta := dbTx.Metadata()
	bucket := meta.Bucket(blockIndexBucketName)
	if bucket == nil {
		return chainhash.Hash{}, 0, nil, false, nil
	}
	raw := bucket.Get(bestTipSnapshotKey)
	if raw == nil || len(raw) != 32+4+32 {
		return chainhash.Hash{}, 0, nil, false, nil
	}
	copy(hash[:], raw[:32])
	height = int32(binary.LittleEndian.Uint32(raw[32:36]))
	work = new(big.Int).SetBytes(raw[36:68])
	return hash, height, work, true, nil
}

// dbPutBestTipSnapshot persists the best-tip snapshot in the same
// transaction as the block index so a crash never leaves the snapshot and
// the block index out of sync.
func dbPutBestTipSnapshot(dbTx database.Tx, hash *chainhash.Hash, height int32, work *big.Int) error {
	meta := dbTx.Metadata()
	bucket, err := meta.CreateBucketIfNotExists(blockIndexBucketName)
	if err != nil {
		return err
	}
	raw := make([]byte, 32+4+32)
	copy(raw[:32], hash[:])
	binary.LittleEndian.PutUint32(raw[32:36], uint32(height))
	wb := work.Bytes()
	copy(raw[36+32-len(wb):], wb) // right-align to 32B big-endian
	return bucket.Put(bestTipSnapshotKey, raw)
}

func deserializeBlockRow(blockRow []byte) (*wire.BlockHeader, blockStatus, error) {
	buffer := bytes.NewReader(blockRow)

	var header wire.BlockHeader
	err := header.Deserialize(buffer)
	if err != nil {
		return nil, statusNone, err
	}

	statusByte, err := buffer.ReadByte()
	if err != nil {
		return nil, statusNone, err
	}

	return &header, blockStatus(statusByte), nil
}

// dbFetchHeaderByHash uses an existing database transaction to retrieve the
// block header for the provided hash.
func dbFetchHeaderByHash(dbTx database.Tx, hash *chainhash.Hash) (*wire.BlockHeader, error) {
	headerBytes, err := dbTx.FetchBlockHeader(hash)
	if err != nil {
		return nil, err
	}

	var header wire.BlockHeader
	err = header.Deserialize(bytes.NewReader(headerBytes))
	if err != nil {
		return nil, err
	}

	return &header, nil
}

// dbFetchHeaderByHeight uses an existing database transaction to retrieve the
// block header for the provided height.
func dbFetchHeaderByHeight(dbTx database.Tx, height int32) (*wire.BlockHeader, error) {
	hash, err := dbFetchHashByHeight(dbTx, height)
	if err != nil {
		return nil, err
	}

	return dbFetchHeaderByHash(dbTx, hash)
}

// dbFetchBlockByNode uses an existing database transaction to retrieve the
// raw block for the provided node, deserialize it, and return a btcutil.Block
// with the height set.
func dbFetchBlockByNode(dbTx database.Tx, node *blockNode) (*btcutil.Block, error) {
	// Load the raw block bytes from the database.
	blockBytes, err := dbTx.FetchBlock(&node.hash)
	if err != nil {
		return nil, err
	}

	// Create the encapsulated block and set the height appropriately.
	block, err := DBBlockFromBytes(blockBytes, node.hash)
	if err != nil {
		return nil, err
	}
	block.SetHeight(node.height)

	return block, nil
}

// dbStoreBlockNode stores the block header and validation status to the block
// index bucket. This overwrites the current entry if there exists one.
func dbStoreBlockNode(dbTx database.Tx, node *blockNode) error {
	// Serialize block data to be stored.
	w := bytes.NewBuffer(make([]byte, 0, blockHdrSize+1))
	header := node.Header()
	err := header.Serialize(w)
	if err != nil {
		return err
	}
	err = w.WriteByte(byte(node.status))
	if err != nil {
		return err
	}
	value := w.Bytes()

	// Write block header data to block index bucket.
	blockIndexBucket := dbTx.Metadata().Bucket(blockIndexBucketName)
	key := blockIndexKey(&node.hash, uint32(node.height))
	return blockIndexBucket.Put(key, value)
}

// dbRemoveBlockNode removes a block header entry from the block index bucket.
// It is used to fully delete a disconnected block (e.g. a locally-mined block
// no peer shares) after a reorg, so a restart cannot re-materialize the header.
func dbRemoveBlockNode(dbTx database.Tx, hash *chainhash.Hash, height int32) error {
	blockIndexBucket := dbTx.Metadata().Bucket(blockIndexBucketName)
	key := blockIndexKey(hash, uint32(height))
	return blockIndexBucket.Delete(key)
}

// dbStoreBlock stores the provided block in the database if it is not already
// there. The full block data is written to ffldb.
func dbStoreBlock(dbTx database.Tx, block *btcutil.Block) error {
	hasBlock, err := dbTx.HasBlock(block.Hash())
	if err != nil {
		return err
	}
	if hasBlock {
		return nil
	}
	return dbTx.StoreBlock(block)
}

// blockIndexKey generates the binary key for an entry in the block index
// bucket. The key is composed of the block height encoded as a big-endian
// 32-bit unsigned int followed by the 32 byte block hash.
func blockIndexKey(blockHash *chainhash.Hash, blockHeight uint32) []byte {
	indexKey := make([]byte, chainhash.HashSize+4)
	binary.BigEndian.PutUint32(indexKey[0:4], blockHeight)
	copy(indexKey[4:chainhash.HashSize+4], blockHash[:])
	return indexKey
}

// BlocksByHeights returns the blocks at the given main-chain heights in a
// single database transaction, so the underlying ffldb FetchBlocks can sort
// the block-file accesses by filenum:offset and read them linearly instead
// of issuing one random access per block.  This is used by the sugar index
// rebuild to cut the 44M-block catch-up from per-block random reads to bulk
// linear reads.
//
// Heights that have no main-chain block are returned as nil entries (with a
// matching error in errs), so callers can skip them without aborting the
// whole batch -- e.g. the sugar index rebuild tolerates heights whose block
// has not been downloaded yet.  The returned slices are parallel to heights.
//
// This function is safe for concurrent access.
// BlocksByHeights 在单个数据库事务中返回给定主链高度的块,使底层 ffldb
// FetchBlocks 能按 filenum:offset 对块文件访问排序、线性读取,而不是每块
// 一次随机访问。sugar index 重建用它把 4400 万块的追赶从逐块随机读变成
// 批量线性读。
//
// 没有主链块的高度返回 nil 条目(对应错误在 errs),调用方可跳过而不中止
// 整批——如 sugar index 重建容忍尚未下载块的高度。返回切片与 heights 平行。
//
// 该函数可并发安全调用。
func (b *BlockChain) BlocksByHeights(heights []int32) ([]*btcutil.Block, []error) {
	b.chainLock.RLock()
	defer b.chainLock.RUnlock()

	blocks := make([]*btcutil.Block, len(heights))
	errs := make([]error, len(heights))

	// First resolve every height to its main-chain hash; heights without a
	// block (errNotInMainChain) are recorded and skipped.  The hash lookups
	// are cheap height-index reads; the expensive part (the block bytes) is
	// done in bulk below.
	// 先把每个高度解析为主链 hash;没有块的高度(errNotInMainChain)记录后
	// 跳过。hash 查询是廉价的高度索引读;昂贵部分(块字节)在下面批量完成。
	hashes := make([]chainhash.Hash, 0, len(heights))
	indexes := make([]int, 0, len(heights))
	err := b.db.View(func(dbTx database.Tx) error {
		for i, h := range heights {
			hash, herr := dbFetchHashByHeight(dbTx, h)
			if herr != nil {
				// Leave the entry nil so the caller skips it.
				// 保留 nil 条目,由调用方跳过。
				errs[i] = herr
				continue
			}
			hashes = append(hashes, *hash)
			indexes = append(indexes, i)
		}
		if len(hashes) == 0 {
			return nil
		}
		rawBlocks, ferr := dbTx.FetchBlocks(hashes)
		if ferr != nil {
			// A bulk read failed -- almost certainly a missing block
			// somewhere in the batch.  Fall back to per-block reads so the
			// missing heights are reported individually and the present
			// blocks are still returned.
			// 批量读失败——几乎肯定是批中某处缺块。回退到逐块读,让缺失
			// 高度被单独报告,存在的块仍被返回。
			for j := range hashes {
				raw, pErr := dbTx.FetchBlock(&hashes[j])
				if pErr != nil {
					errs[indexes[j]] = pErr
					continue
				}
				block, dErr := DBBlockFromBytes(raw, hashes[j])
				if dErr != nil {
					return dErr
				}
				block.SetHeight(heights[indexes[j]])
				blocks[indexes[j]] = block
			}
			return nil
		}
		for j, raw := range rawBlocks {
			block, dErr := DBBlockFromBytes(raw, hashes[j])
			if dErr != nil {
				return dErr
			}
			block.SetHeight(heights[indexes[j]])
			blocks[indexes[j]] = block
		}
		return nil
	})
	if err != nil {
		for i := range errs {
			if errs[i] == nil {
				errs[i] = err
			}
		}
	}
	return blocks, errs
}

// BlockByHeight returns the block at the given height in the main chain.
//
// This function is safe for concurrent access.
func (b *BlockChain) BlockByHeight(blockHeight int32) (*btcutil.Block, error) {
	// Lookup the block height in the best chain, falling back to a cold
	// materialization when the height has been evicted from the in-memory
	// header window.
	node := b.nodeAtHeight(blockHeight)
	if node == nil {
		str := fmt.Sprintf("no block at height %d exists", blockHeight)
		return nil, errNotInMainChain(str)
	}

	// Load the block from the database and return it.
	var block *btcutil.Block
	err := b.db.View(func(dbTx database.Tx) error {
		var err error
		block, err = dbFetchBlockByNode(dbTx, node)
		return err
	})
	return block, err
}

// fetchBlockByHeight loads the block at the given main-chain height directly
// from the database, using only the height index, without referencing any
// in-memory block node.  This is used by the UTXO reconstruction path, which
// must be able to replay blocks even when windowing has evicted every node at
// and below the reconstruction boundary from the in-memory index.
//
// This function is safe for concurrent access.
func (b *BlockChain) fetchBlockByHeight(height int32) (*btcutil.Block, error) {
	var block *btcutil.Block
	err := b.db.View(func(dbTx database.Tx) error {
		hash, err := dbFetchHashByHeight(dbTx, height)
		if err != nil {
			return err
		}
		block, err = dbFetchBlockByHeightHash(dbTx, hash, height)
		return err
	})
	return block, err
}

// dbFetchBlockByHeightHash loads the block for the given hash from the block
// store and returns it with the provided height set.
func dbFetchBlockByHeightHash(dbTx database.Tx, hash *chainhash.Hash, height int32) (*btcutil.Block, error) {
	blockBytes, err := dbTx.FetchBlock(hash)
	if err != nil {
		return nil, err
	}
	block, err := DBBlockFromBytes(blockBytes, *hash)
	if err != nil {
		return nil, err
	}
	block.SetHeight(height)
	return block, nil
}

// BlockByHash returns the block from the main chain with the given hash with
// the appropriate chain height set.
//
// This function is safe for concurrent access.
func (b *BlockChain) BlockByHash(hash *chainhash.Hash) (*btcutil.Block, error) {
	// Lookup the block hash in block index and ensure it is in the best
	// chain, falling back to a cold materialization when the block has been
	// evicted from the in-memory header window.
	node := b.index.LookupNode(hash)
	if node == nil {
		node = b.materializeColdNode(hash)
		if node == nil || !b.isMainChainHash(hash) {
			str := fmt.Sprintf("block %s is not in the main chain", hash)
			return nil, errNotInMainChain(str)
		}
	} else if !b.bestChain.Contains(node) {
		str := fmt.Sprintf("block %s is not in the main chain", hash)
		return nil, errNotInMainChain(str)
	}

	// Load the block from the database and return it.
	var block *btcutil.Block
	err := b.db.View(func(dbTx database.Tx) error {
		var err error
		block, err = dbFetchBlockByNode(dbTx, node)
		return err
	})
	return block, err
}

// FetchBlockByHash returns the block with the given hash from the database,
// regardless of whether it is currently part of the best chain.  This is
// intended for resuming a block download after a restart: blocks that were
// already downloaded and stored but never connected can be replayed through the
// connection logic from disk.
//
// This function is safe for concurrent access.
func (b *BlockChain) FetchBlockByHash(hash *chainhash.Hash) (*btcutil.Block, error) {
	// Lookup the block hash in the block index, falling back to a cold
	// materialization when the block has been evicted from the in-memory
	// header window.
	node := b.index.LookupNode(hash)
	if node == nil {
		node = b.materializeColdNode(hash)
		if node == nil {
			return nil, fmt.Errorf("block %s is not known", hash)
		}
	}

	// Load the block from the database and return it.
	var block *btcutil.Block
	err := b.db.View(func(dbTx database.Tx) error {
		var err error
		block, err = dbFetchBlockByNode(dbTx, node)
		return err
	})
	return block, err
}
