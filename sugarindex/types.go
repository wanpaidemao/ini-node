package sugarindex

import (
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/txscript/v2"
)

// ExtractIndexInfo 复刻 umami src/validation.cpp 的 ExtractIndexInfo:
// 由 scriptPubKey 推导地址索引类型 (ADDR_INDT) 与 hashBytes。
// ExtractIndexInfo mirrors umami's ExtractIndexInfo (src/validation.cpp): it
// derives the address-index type and hash bytes from a scriptPubKey.
func ExtractIndexInfo(pkScript []byte) (scriptType int, hashBytes []byte) {
	if txscript.IsPayToPubKeyHash(pkScript) {
		return AddrIndtPubkeyAddress, pkScript[3:23]
	}
	if txscript.IsPayToScriptHash(pkScript) {
		return AddrIndtScriptAddress, pkScript[2:22]
	}
	if txscript.IsPayToWitnessScriptHash(pkScript) {
		return AddrIndtWitnessV0ScriptHash, pkScript[2:34]
	}
	if version, program, err := txscript.ExtractWitnessProgramInfo(pkScript); err == nil {
		switch version {
		case 0:
			return AddrIndtWitnessV0KeyHash, program
		case 1:
			return AddrIndtWitnessV1Taproot, program
		}
	}
	return AddrIndtUnknown, nil
}

// ---------------------------------------------------------------------------
// AddressIndexKey 地址增量索引键(对应 CAddressIndexKey)。
// 每笔输出的 +delta、每笔输入花费的 -delta 各写一条。

type AddressIndexKey struct {
	Type        uint32
	HashBytes   []byte // 20 或 32 字节,存盘时补零到 32
	BlockHeight int32
	TxIndex     uint32 // 交易在区块内序号
	TxHash      chainhash.Hash
	Index       uint32 // vin 或 vout 序号
	Spending    bool   // true=花费(输入)/false=收到(输出)
}

// Key 返回 DB key(前缀 'a' + 81 字节结构)。
// Key returns the DB key ('a' prefix + 81-byte struct).
func (k *AddressIndexKey) Key() []byte {
	e := &enc{}
	e.u8(DBAddressIndex)
	e.u32(k.Type)
	e.hashIndex(k.HashBytes)
	e.i32(k.BlockHeight)
	e.u32(k.TxIndex)
	e.hash(k.TxHash)
	e.u32(k.Index)
	e.boolean(k.Spending)
	return e.bytes()
}

// ---------------------------------------------------------------------------
// AddressUnspentKey/Value 地址未花费索引(对应 CAddressUnspentKey/Value)。

type AddressUnspentKey struct {
	Type      uint32
	HashBytes []byte
	TxHash    chainhash.Hash
	Index     uint32
}

// Key 返回 DB key('u' + 72 字节)。Key returns the DB key.
func (k *AddressUnspentKey) Key() []byte {
	e := &enc{}
	e.u8(DBAddressUnspent)
	e.u32(k.Type)
	e.hashIndex(k.HashBytes)
	e.hash(k.TxHash)
	e.u32(k.Index)
	return e.bytes()
}

type AddressUnspentValue struct {
	Satoshis    int64
	Script      []byte
	BlockHeight int32
}

// Encode 序列化值(不含混淆,由调用方混淆)。
// Encode serializes the value (without obfuscation).
func (v *AddressUnspentValue) Encode() []byte {
	e := &enc{}
	e.i64(v.Satoshis)
	e.script(v.Script)
	e.i32(v.BlockHeight)
	return e.bytes()
}

// IsNull 对应 C++ satoshis==-1,表示该条目已被清除(花费)。
// IsNull mirrors C++ (satoshis == -1): the entry was erased.
func (v *AddressUnspentValue) IsNull() bool { return v.Satoshis == -1 }

// ---------------------------------------------------------------------------
// SpentIndexKey/Value 花费索引对应 CSpentIndexKey/Value。

type SpentIndexKey struct {
	TxID        chainhash.Hash
	OutputIndex uint32
}

// Key 返回 DB key('p' + 36 字节)。Key returns the DB key.
func (k *SpentIndexKey) Key() []byte {
	e := &enc{}
	e.u8(DBSpentIndex)
	e.hash(k.TxID)
	e.u32(k.OutputIndex)
	return e.bytes()
}

type SpentIndexValue struct {
	TxID        chainhash.Hash // 花费交易
	InputIndex  uint32
	BlockHeight int32
	Satoshis    int64
	AddressType int32
	AddressHash []byte
}

// Encode 序列化值(不含混淆)。Encode serializes (without obfuscation).
func (v *SpentIndexValue) Encode() []byte {
	e := &enc{}
	e.hash(v.TxID)
	e.u32(v.InputIndex)
	e.i32(v.BlockHeight)
	e.i64(v.Satoshis)
	e.i32(v.AddressType)
	e.hashIndex(v.AddressHash)
	return e.bytes()
}

// ---------------------------------------------------------------------------
// TimestampIndexKey 时间戳索引对应 CTimestampIndexKey。

type TimestampIndexKey struct {
	Timestamp uint32 // block time
	BlockHash chainhash.Hash
}

// Key 返回 DB key('s' + 36 字节)。Key returns the DB key.
func (k *TimestampIndexKey) Key() []byte {
	e := &enc{}
	e.u8(DBTimestampIndex)
	e.u32(k.Timestamp)
	e.hash(k.BlockHash)
	return e.bytes()
}