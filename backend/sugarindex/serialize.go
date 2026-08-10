// Package sugarindex 实现与 umami (Bitcoin Core 派生) 逐字节一致的
// 地址/花费/时间戳索引序列化,供 btcd 节点写入 raw LevelDB。
//
// Package sugarindex implements byte-exact serialization of the Sugarchain
// address/spent/timestamp index, mirroring the umami (Bitcoin Core fork)
// block-tree DB format so the index can be read verbatim by umami tooling.
package sugarindex

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/chainhash/v2"
)

// 序列化前缀 / DB prefixes (mirror src/txdb.cpp)
const (
	DBAddressIndex      = 'a' // CAddressIndexKey  -> int64 delta
	DBAddressUnspent    = 'u' // CAddressUnspentKey -> CAddressUnspentValue
	DBTimestampIndex    = 's' // CTimestampIndexKey -> 0
	DBSpentIndex        = 'p' // CSpentIndexKey    -> CSpentIndexValue
	obfuscateKeyNumByte = 8
)

// obfuscateKeyName 是混淆密钥在 LevelDB 中的明文键,与 umami dbwrapper.cpp 一致。
// obfuscateKeyName is the plaintext key under which the obfuscation key is
// stored, matching umami's src/dbwrapper.cpp.
var obfuscateKeyName = []byte("\x00obfuscate_key")

// AddressIndexType 对应 umami 的 enum AddressIndexType (src/validation.h)。
// AddressIndexType mirrors umami's enum AddressIndexType.
const (
	AddrIndtUnknown               = 0
	AddrIndtPubkeyAddress         = 1
	AddrIndtScriptAddress         = 2
	AddrIndtWitnessV0KeyHash      = 5
	AddrIndtWitnessV0ScriptHash   = 6
	AddrIndtWitnessV1Taproot      = 7
)

// enc 是字节序列化的增序写入器,严格按 ummi 的 serialize.h 布局(little-endian)。
// enc is an immutable-ish byte append-writer following umami's serialize.h (LE).
type enc struct{ b []byte }

func (e *enc) u8(v byte)     { e.b = append(e.b, v) }
func (e *enc) u32(v uint32)  { e.b = binary.LittleEndian.AppendUint32(e.b, v) }
func (e *enc) i32(v int32)   { e.u32(uint32(v)) }
func (e *enc) i64(v int64)   { e.b = binary.LittleEndian.AppendUint64(e.b, uint64(v)) }
func (e *enc) boolean(v bool) {
	if v {
		e.u8(1)
	} else {
		e.u8(0)
	}
}

// compactSize 按 Bitcoin CompactSize 编码(serialize.h)。
// compactSize encodes per Bitcoin CompactSize.
func (e *enc) compactSize(n uint64) {
	switch {
	case n < 253:
		e.u8(byte(n))
	case n <= 0xffff:
		e.u8(0xfd)
		e.u32(uint32(n))
	case n <= 0xffffffff:
		e.u8(0xfe)
		e.u32(uint32(n))
	default:
		e.u8(0xff)
		e.b = binary.LittleEndian.AppendUint64(e.b, n)
	}
}

// hash 写入 32 字节原始 bytes(对应 C++ uint256 内存序)。
// hash writes 32 raw bytes (the uint256 internal byte order).
func (e *enc) hash(h chainhash.Hash) { e.b = append(e.b, h[:]...) }

// hash20 写入 20 字节 hashbytes,并补零到 32 字节(对应 uint256(hashBytes, len))。
// hashBytes writes the 20 or 32-byte index hash, zero-padded to 32 bytes to
// mirror uint256(hashBytes.data(), hashBytes.size()).
func (e *enc) hashIndex(hashBytes []byte) {
	if len(hashBytes) > 32 {
		panic(fmt.Sprintf("sugarindex: hashBytes too large: %d", len(hashBytes)))
	}
	e.b = append(e.b, hashBytes...)
	for i := len(hashBytes); i < 32; i++ {
		e.b = append(e.b, 0)
	}
}

// script 按 CScript 序列化:CompactSize 长度前缀 + 原始脚本字节。
// script serializes like a CScript: CompactSize length prefix + raw bytes.
func (e *enc) script(s []byte) { e.compactSize(uint64(len(s))); e.b = append(e.b, s...) }

// bytes 返回已序列化的结果/ key 部分。
// bytes returns the accumulated bytes.
func (e *enc) bytes() []byte { return e.b }

// xorObfuscate 对值字节按 8 字节混淆 key 循环异或,复刻 streams.h 的 XOR 流程。
// xorObfuscate XOR-cycles the value bytes over the 8-byte key (streams.h).
func xorObfuscate(key, value []byte) []byte {
	out := make([]byte, len(value))
	for i := 0; i < len(value); i++ {
		out[i] = value[i] ^ key[i%len(key)]
	}
	return out
}

// ---------------------------------------------------------------------------
// 反序列化(供 RPC 读取)

// dec 是一个字节流读取器,按同样格式解码。RPC 读取时不关心混淆(值在读取时已异或还原)。
type dec struct {
	b   []byte
	off int
}

func (d *dec) remaining() int { return len(d.b) - d.off }

var errShort = errors.New("sugarindex: unexpected end of data")

func (d *dec) u8() (byte, error) {
	if d.remaining() < 1 {
		return 0, errShort
	}
	v := d.b[d.off]
	d.off++
	return v, nil
}

func (d *dec) u32() (uint32, error) {
	if d.remaining() < 4 {
		return 0, errShort
	}
	v := binary.LittleEndian.Uint32(d.b[d.off:])
	d.off += 4
	return v, nil
}

func (d *dec) i32() (int32, error) {
	v, err := d.u32()
	return int32(v), err
}

func (d *dec) i64() (int64, error) {
	if d.remaining() < 8 {
		return 0, errShort
	}
	v := int64(binary.LittleEndian.Uint64(d.b[d.off:]))
	d.off += 8
	return v, nil
}

func (d *dec) boolean() (bool, error) {
	v, err := d.u8()
	return v != 0, err
}

func (d *dec) compactSize() (uint64, error) {
	first, err := d.u8()
	if err != nil {
		return 0, err
	}
	switch first {
	case 0xfd:
		v, err := d.u16()
		return uint64(v), err
	case 0xfe:
		v, err := d.u32()
		return uint64(v), err
	case 0xff:
		return d.u64()
	default:
		return uint64(first), nil
	}
}

func (d *dec) u16() (uint16, error) {
	if d.remaining() < 2 {
		return 0, errShort
	}
	v := binary.LittleEndian.Uint16(d.b[d.off:])
	d.off += 2
	return v, nil
}

func (d *dec) u64() (uint64, error) {
	if d.remaining() < 8 {
		return 0, errShort
	}
	v := binary.LittleEndian.Uint64(d.b[d.off:])
	d.off += 8
	return v, nil
}

// hash reads 32 bytes (fixed, no length prefix).
func (d *dec) hash() (chainhash.Hash, error) {
	if d.remaining() < 32 {
		return chainhash.Hash{}, errShort
	}
	var h chainhash.Hash
	copy(h[:], d.b[d.off:d.off+32])
	d.off += 32
	return h, nil
}

// script reads a CompactSize-length-prefixed byte string.
func (d *dec) script() ([]byte, error) {
	n, err := d.compactSize()
	if err != nil {
		return nil, err
	}
	if uint64(d.remaining()) < n {
		return nil, errShort
	}
	s := append([]byte{}, d.b[d.off:d.off+int(n)]...)
	d.off += int(n)
	return s, nil
}