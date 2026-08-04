package sugarindex

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/chainhash/v2"
)

// hexHash 解析 32 字节 hex 为 chainhash.Hash。
// hexHash parses a 32-byte hex string into a chainhash.Hash.
func hexHash(t *testing.T, s string) chainhash.Hash {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		t.Fatalf("invalid 32-byte hex: %q err=%v", s, err)
	}
	return chainhash.Hash(b)
}

// TestAddressIndexKeyKey 验证 CAddressIndexKey 的逐字节布局。
// TestAddressIndexKeyKey verifies the byte-exact CAddressIndexKey layout.
func TestAddressIndexKeyKey(t *testing.T) {
	hashBytes := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	txHash := hexHash(t, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")

	k := &AddressIndexKey{
		Type:        1,
		HashBytes:   hashBytes,
		BlockHeight: 100,
		TxIndex:     3,
		TxHash:      txHash,
		Index:       7,
		Spending:    true,
	}

	// 期望:前缀 'a' + type(LE4) + hashBytes补零到32 + height(LE4) +
	// txindex(LE4) + txhash(32) + index(LE4) + bool(1)
	want := []byte{0x61}
	want = append(want, 0x01, 0x00, 0x00, 0x00) // type=1 LE
	want = append(want, hashBytes...)            // 20B hash
	want = append(want, make([]byte, 12)...)     // 补零到32
	want = append(want, 0x64, 0x00, 0x00, 0x00)  // height=100 LE
	want = append(want, 0x03, 0x00, 0x00, 0x00)  // txindex=3 LE
	want = append(want, txHash[:]...)            // 32B txhash
	want = append(want, 0x07, 0x00, 0x00, 0x00)  // index=7 LE
	want = append(want, 0x01)                    // spending=true

	got := k.Key()
	if !bytes.Equal(got, want) {
		t.Fatalf("AddressIndexKey.Key:\n got  %x\n want %x", got, want)
	}
	if len(got) != 1+81 {
		t.Errorf("key length = %d, want %d", len(got), 82)
	}
}

// TestAddressUnspentKeyAndValue 验证 'u' 前缀键与值布局。
// TestAddressUnspentKeyAndValue verifies the 'u'-prefixed key and value layout.
func TestAddressUnspentKeyAndValue(t *testing.T) {
	hashBytes := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14}
	txHash := hexHash(t, "101112131415161718191a1b1c1d1e1f202122232425262728292a2b2c2d2e2f")

	k := &AddressUnspentKey{
		Type:      2,
		HashBytes: hashBytes,
		TxHash:    txHash,
		Index:     5,
	}
	keyWant := []byte{0x75} // 'u'
	keyWant = append(keyWant, 0x02, 0x00, 0x00, 0x00)
	keyWant = append(keyWant, hashBytes...)
	keyWant = append(keyWant, make([]byte, 12)...)
	keyWant = append(keyWant, txHash[:]...)
	keyWant = append(keyWant, 0x05, 0x00, 0x00, 0x00)
	if got := k.Key(); !bytes.Equal(got, keyWant) {
		t.Fatalf("AddressUnspentKey.Key:\n got  %x\n want %x", got, keyWant)
	}

	// 值:sats(LE8) + compactsize(script len) + script + height(LE4)
	// P2PKH: OP_DUP OP_HASH160 <20B> OP_EQUALVERIFY OP_CHECKSIG
	p2pkh := append([]byte{0x76, 0xa9, 0x14}, hashBytes...)
	p2pkh = append(p2pkh, 0x88, 0xac)
	v := &AddressUnspentValue{Satoshis: 1234567890, Script: p2pkh, BlockHeight: 42}
	valWant := append(le64(1234567890), byte(len(p2pkh))) // len<253 -> 1 byte
	valWant = append(valWant, p2pkh...)
	valWant = append(valWant, 42, 0x00, 0x00, 0x00)
	if got := v.Encode(); !bytes.Equal(got, valWant) {
		t.Fatalf("AddressUnspentValue.Encode:\n got  %x\n want %x", got, valWant)
	}
}

// TestSpentIndexKeyAndValue 验证 'p' 前缀键与 84 字节值布局。
// TestSpentIndexKeyAndValue verifies the 'p'-prefixed key and 84-byte value.
func TestSpentIndexKeyAndValue(t *testing.T) {
	txid := hexHash(t, "0000000000000000000000000000000000000000000000000000000000000001")
	spent := hexHash(t, "1111111111111111111111111111111111111111111111111111111111111111")

	k := &SpentIndexKey{TxID: txid, OutputIndex: 9}
	keyWant := append([]byte{0x70}, txid[:]...)
	keyWant = append(keyWant, 0x09, 0x00, 0x00, 0x00)
	if got := k.Key(); !bytes.Equal(got, keyWant) {
		t.Fatalf("SpentIndexKey.Key:\n got  %x\n want %x", got, keyWant)
	}

	v := &SpentIndexValue{
		TxID:        spent,
		InputIndex:  2,
		BlockHeight: 555,
		Satoshis:    999,
		AddressType: 1,
		AddressHash: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
	}
	got := v.Encode()
	if len(got) != 84 {
		t.Fatalf("SpentIndexValue.Encode len = %d, want 84", len(got))
	}
	// 逐字段断言
	if !bytes.Equal(got[0:32], spent[:]) {
		t.Errorf("spent txid mismatch")
	}
	if got[32] != 2 || got[33] != 0 || got[34] != 0 || got[35] != 0 {
		t.Errorf("inputIndex mismatch: %x", got[32:36])
	}
	if !bytes.Equal(got[36:40], []byte{0x2b, 0x02, 0x00, 0x00}) { // 555 LE
		t.Errorf("blockHeight mismatch: %x", got[36:40])
	}
	if !bytes.Equal(got[40:48], []byte{0xe7, 0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}) { // 999 LE
		t.Errorf("satoshis mismatch: %x", got[40:48])
	}
	if !bytes.Equal(got[48:52], []byte{1, 0, 0, 0}) {
		t.Errorf("addressType mismatch: %x", got[48:52])
	}
	if !bytes.Equal(got[52:72], v.AddressHash) || !bytes.Equal(got[72:84], make([]byte, 12)) {
		t.Errorf("addressHash padding mismatch: %x", got[52:84])
	}
}

// TestTimestampIndexKey 验证 's' 前缀键。
// TestTimestampIndexKey verifies the 's'-prefixed key.
func TestTimestampIndexKey(t *testing.T) {
	blk := hexHash(t, "2222222222222222222222222222222222222222222222222222222222222222")
	k := &TimestampIndexKey{Timestamp: 1565913601, BlockHash: blk}
	want := []byte{0x73}
	want = append(want, 0x01, 0xf2, 0x55, 0x5d) // 1565913601 LE (0x5D55F201)
	want = append(want, blk[:]...)
	if got := k.Key(); !bytes.Equal(got, want) {
		t.Fatalf("TimestampIndexKey.Key:\n got  %x\n want %x", got, want)
	}
}

// TestXorObfuscate 验证 XOR 混淆与 umami streams.h 一致。
// TestXorObfuscate verifies XOR obfuscation matches umami's streams.h.
func TestXorObfuscate(t *testing.T) {
	key := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	value := []byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8, 0xf7, 0xf6}
	obf := xorObfuscate(key, value)
	// obf[i] = value[i] ^ key[i%8]
	want := []byte{
		0xff ^ 0x01, 0xfe ^ 0x02, 0xfd ^ 0x03, 0xfc ^ 0x04,
		0xfb ^ 0x05, 0xfa ^ 0x06, 0xf9 ^ 0x07, 0xf8 ^ 0x08,
		0xf7 ^ 0x01, 0xf6 ^ 0x02,
	}
	if !bytes.Equal(obf, want) {
		t.Fatalf("xorObfuscate:\n got  %x\n want %x", obf, want)
	}
	// 还原 (XOR 自反)
	if got := xorObfuscate(key, obf); !bytes.Equal(got, value) {
		t.Fatalf("xor round-trip mismatch: %x", got)
	}
}

// TestExtractIndexInfo 验证 scriptPubKey 到 ADDR_INDT 的映射。
// TestExtractIndexInfo verifies the scriptPubKey to ADDR_INDT mapping.
func TestExtractIndexInfo(t *testing.T) {
	hash20 := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11, 0x22, 0x33, 0x44}
	hash32 := make([]byte, 32)
	for i := range hash32 {
		hash32[i] = byte(i)
	}
	p2pkh := append([]byte{0x76, 0xa9, 0x14}, hash20...)
	p2pkh = append(p2pkh, 0x88, 0xac)
	p2sh := append([]byte{0xa9, 0x14}, hash20...)
	p2sh = append(p2sh, 0x87)
	p2wpkh := append([]byte{0x00, 0x14}, hash20...)
	p2wsh := append([]byte{0x00, 0x20}, hash32...)

	cases := []struct {
		name      string
		script    []byte
		wantType  int
		wantBytes int
	}{
		{"p2pkh", p2pkh, AddrIndtPubkeyAddress, 20},
		{"p2sh", p2sh, AddrIndtScriptAddress, 20},
		{"p2wpkh", p2wpkh, AddrIndtWitnessV0KeyHash, 20},
		{"p2wsh", p2wsh, AddrIndtWitnessV0ScriptHash, 32},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, hb := ExtractIndexInfo(c.script)
			if st != c.wantType {
				t.Errorf("type = %d, want %d", st, c.wantType)
			}
			if len(hb) != c.wantBytes {
				t.Errorf("hashBytes len = %d, want %d", len(hb), c.wantBytes)
			}
		})
	}
}

func le64(v int64) []byte {
	var b [8]byte
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (8 * i))
	}
	return b[:]
}