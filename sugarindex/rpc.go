package sugarindex

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/address/v2"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// ---------------------------------------------------------------------------
// 地址 ↔ (type, hashBytes) 编解码(对应 umami index.cpp getIndexKey /
// getAddressFromIndex)。

// DecodeIndexKey 把一个地址字符串映射为 (索引类型, hashBytes)。
// 复刻 umami 的 getIndexKey。成功时返回 (scriptType, hashBytes, nil)。
//
// DecodeIndexKey maps an address string to (index type, hashBytes), mirroring
// umami's getIndexKey.
func DecodeIndexKey(addrStr string, params *chaincfg.Params) (int, []byte, error) {
	addr, err := address.DecodeAddress(addrStr, params)
	if err != nil {
		return 0, nil, err
	}

	switch a := addr.(type) {
	case *address.AddressPubKeyHash:
		return AddrIndtPubkeyAddress, a.ScriptAddress(), nil
	case *address.AddressScriptHash:
		return AddrIndtScriptAddress, a.ScriptAddress(), nil
	case *address.AddressSegWit:
		prog := a.ScriptAddress()
		switch {
		case a.WitnessVersion() == 0 && len(prog) == 20:
			return AddrIndtWitnessV0KeyHash, prog, nil
		case a.WitnessVersion() == 0 && len(prog) == 32:
			return AddrIndtWitnessV0ScriptHash, prog, nil
		case a.WitnessVersion() == 1 && len(prog) == 32:
			return AddrIndtWitnessV1Taproot, prog, nil
		}
	case *address.AddressTaproot:
		return AddrIndtWitnessV1Taproot, a.ScriptAddress(), nil
	}

	return AddrIndtUnknown, nil, errors.New("invalid address type for index")
}

// EncodeIndexAddress 把 (索引类型, hashBytes) 映射回地址字符串。
// 复刻 umami 的 getAddressFromIndex。
//
// EncodeIndexAddress maps (index type, hashBytes) back to an address string,
// mirroring umami's getAddressFromIndex.
func EncodeIndexAddress(scriptType int, hashBytes []byte,
	params *chaincfg.Params) (string, error) {

	var addrStr string
	var err error
	switch scriptType {
	case AddrIndtPubkeyAddress:
		var a *address.AddressPubKeyHash
		a, err = address.NewAddressPubKeyHash(hashBytes[:20], params)
		if err == nil {
			addrStr = a.String()
		}
	case AddrIndtScriptAddress:
		var a *address.AddressScriptHash
		a, err = address.NewAddressScriptHashFromHash(hashBytes[:20], params)
		if err == nil {
			addrStr = a.String()
		}
	case AddrIndtWitnessV0KeyHash:
		var a *address.AddressWitnessPubKeyHash
		a, err = address.NewAddressWitnessPubKeyHash(hashBytes[:20], params)
		if err == nil {
			addrStr = a.String()
		}
	case AddrIndtWitnessV0ScriptHash:
		var a *address.AddressWitnessScriptHash
		a, err = address.NewAddressWitnessScriptHash(hashBytes[:32], params)
		if err == nil {
			addrStr = a.String()
		}
	case AddrIndtWitnessV1Taproot:
		var a *address.AddressTaproot
		a, err = address.NewAddressTaproot(hashBytes[:32], params)
		if err == nil {
			addrStr = a.String()
		}
	default:
		return "", fmt.Errorf("unknown address type %d", scriptType)
	}
	if err != nil {
		return "", err
	}
	return addrStr, nil
}

// ---------------------------------------------------------------------------
// 索引读取(供 RPC)

// AddressIndexEntry 是地址索引的一行(键 + delta)。
// AddressIndexEntry is one address-index row (key + delta).
type AddressIndexEntry struct {
	Type        int
	HashBytes   []byte
	BlockHeight int32
	TxIndex     uint32
	TxHash      chainhash.Hash
	Index       uint32
	Spending    bool
	Satoshis    int64
}

// AddressUnspentEntry 是地址未花费索引的一行。
// AddressUnspentEntry is one address-unspent row.
type AddressUnspentEntry struct {
	Type        int
	HashBytes   []byte
	TxHash      chainhash.Hash
	Index       uint32
	Satoshis    int64
	Script      []byte
	BlockHeight int32
}

// TimestampEntry 是时间戳索引的一行(block hash + 区块时间)。
// TimestampEntry is one timestamp-index row.
type TimestampEntry struct {
	BlockHash chainhash.Hash
	Timestamp uint32
}

// addressSeekKey 构造迭代起始键:prefix + type + hashBytes(32),
// 若 start>0 再追加 height,对应 CAddressIndexIterator(Height)Key。
func addressSeekKey(prefix byte, addrType int, hashBytes []byte,
	start int32) []byte {

	e := &enc{}
	e.u8(prefix)
	e.u32(uint32(addrType))
	e.hashIndex(hashBytes)
	if start > 0 {
		e.i32(start)
	}
	return e.bytes()
}

// ReadAddressIndex 遍历某地址的全部地址增量,按 height 升序。
// start/end>0 时限定高度区间(闭区间)。
//
// ReadAddressIndex iterates all address deltas for one address, ascending by
// height, optionally restricted to [start, end].
func (m *Manager) ReadAddressIndex(addrType int, hashBytes []byte,
	start, end int32, cb func(AddressIndexEntry) bool) error {

	seek := addressSeekKey(DBAddressIndex, addrType, hashBytes, start)
	return m.iteratePrefixDeobf(seek, func(k, v []byte) bool {
		d := &dec{b: k}
		if _, err := d.u8(); err != nil {
			return false
		}
		typ, err := d.u32()
		if err != nil {
			return false
		}
		hb, err := d.hash()
		if err != nil {
			return false
		}
		// 防止 type/hashBytes 错位(理论上 seek 已限定)。
		if int(typ) != addrType || !bytes.Equal(hb[:len(hashBytes)], hashBytes) {
			return false
		}
		height, err := d.i32()
		if err != nil {
			return false
		}
		if end > 0 && height > end {
			return false
		}
		txIndex, err := d.u32()
		if err != nil {
			return false
		}
		txHash, err := d.hash()
		if err != nil {
			return false
		}
		index, err := d.u32()
		if err != nil {
			return false
		}
		spending, err := d.boolean()
		if err != nil {
			return false
		}
		if len(v) < 8 {
			return false
		}
		delta := int64(binary.LittleEndian.Uint64(v[:8]))

		return cb(AddressIndexEntry{
			Type:        int(typ),
			HashBytes:   append([]byte{}, hb[:]...),
			BlockHeight: height,
			TxIndex:     txIndex,
			TxHash:      txHash,
			Index:       index,
			Spending:    spending,
			Satoshis:    delta,
		})
	})
}

// ReadAddressUnspent 读取某地址的全部未花费输出。
// ReadAddressUnspent reads all unspent outputs for one address.
func (m *Manager) ReadAddressUnspent(addrType int,
	hashBytes []byte) ([]AddressUnspentEntry, error) {

	seek := addressSeekKey(DBAddressUnspent, addrType, hashBytes, 0)
	var out []AddressUnspentEntry
	err := m.iteratePrefixDeobf(seek, func(k, v []byte) bool {
		d := &dec{b: k}
		if _, err := d.u8(); err != nil {
			return false
		}
		typ, err := d.u32()
		if err != nil {
			return false
		}
		hb, err := d.hash()
		if err != nil {
			return false
		}
		if int(typ) != addrType || !bytes.Equal(hb[:len(hashBytes)], hashBytes) {
			return false
		}
		txHash, err := d.hash()
		if err != nil {
			return false
		}
		index, err := d.u32()
		if err != nil {
			return false
		}

		dv := &dec{b: v}
		satoshis, err := dv.i64()
		if err != nil {
			return false
		}
		script, err := dv.script()
		if err != nil {
			return false
		}
		height, err := dv.i32()
		if err != nil {
			return false
		}

		out = append(out, AddressUnspentEntry{
			Type:        int(typ),
			HashBytes:   append([]byte{}, hb[:]...),
			TxHash:      txHash,
			Index:       index,
			Satoshis:    satoshis,
			Script:      script,
			BlockHeight: height,
		})
		return true
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReadSpentIndex 读取花费索引;未找到时返回 (nil, nil)。
// ReadSpentIndex reads the spent-index entry, (nil, nil) when absent.
func (m *Manager) ReadSpentIndex(key *SpentIndexKey) (*SpentIndexValue, error) {
	raw, err := m.getValue(key.Key())
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}

	d := &dec{b: raw}
	txID, err := d.hash()
	if err != nil {
		return nil, err
	}
	inputIndex, err := d.u32()
	if err != nil {
		return nil, err
	}
	blockHeight, err := d.i32()
	if err != nil {
		return nil, err
	}
	satoshis, err := d.i64()
	if err != nil {
		return nil, err
	}
	addrType, err := d.i32()
	if err != nil {
		return nil, err
	}
	addrHash, err := d.hash()
	if err != nil {
		return nil, err
	}

	return &SpentIndexValue{
		TxID:        txID,
		InputIndex:  inputIndex,
		BlockHeight: blockHeight,
		Satoshis:    satoshis,
		AddressType: addrType,
		AddressHash: append([]byte{}, addrHash[:]...),
	}, nil
}

// ReadTimestampIndex 返回 [low, high) 时间范围内的区块哈希。
// ReadTimestampIndex returns block hashes whose timestamp is in [low, high).
func (m *Manager) ReadTimestampIndex(low, high uint32) ([]TimestampEntry, error) {
	seek := &enc{}
	seek.u8(DBTimestampIndex)
	seek.u32(low)

	// The limit must be a key at ts == high: any key whose timestamp is
	// below high sorts strictly below it, whereas prefixSuccessor (which
	// increments the last byte) would jump past the low+1 bucket, because
	// the last byte of the LE-encoded timestamp is its high byte.
	limit := &enc{}
	limit.u8(DBTimestampIndex)
	limit.u32(high)

	iter := m.db.NewIterator(&util.Range{Start: seek.bytes(), Limit: limit.bytes()}, nil)
	defer iter.Release()

	var out []TimestampEntry
	for iter.Next() {
		k := iter.Key()
		d := &dec{b: k}
		if _, err := d.u8(); err != nil {
			return nil, fmt.Errorf("timestamp index: %w", err)
		}
		ts, err := d.u32()
		if err != nil {
			return nil, fmt.Errorf("timestamp index: %w", err)
		}
		if ts >= high {
			break
		}
		blockHash, err := d.hash()
		if err != nil {
			return nil, fmt.Errorf("timestamp index: %w", err)
		}
		out = append(out, TimestampEntry{BlockHash: blockHash, Timestamp: ts})
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("timestamp index: %w", err)
	}
	return out, nil
}
