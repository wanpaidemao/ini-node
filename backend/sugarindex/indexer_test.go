package sugarindex

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/wire/v2"
	"github.com/syndtr/goleveldb/leveldb"
)

// p2pkhScript builds a P2PKH script (OP_DUP OP_HASH160 <20B> OP_EQUALVERIFY
// OP_CHECKSIG) using the first 20 bytes of hashOut.
func p2pkhScript(hashOut []byte) []byte {
	script := make([]byte, 0, 25)
	script = append(script, 0x76, 0xa9, 0x14)
	script = append(script, hashOut[:20]...)
	script = append(script, 0x88, 0xac)
	return script
}

// readI64 decodes the first 8 bytes of a deobfuscated delta value.
func readI64(t *testing.T, value []byte) int64 {
	t.Helper()
	d := &dec{b: value}
	v, err := d.i64()
	if err != nil {
		t.Fatalf("decode i64: %v", err)
	}
	return v
}

// readUnspent decodes an AddressUnspentValue.
func readUnspent(t *testing.T, value []byte) (*AddressUnspentValue, error) {
	t.Helper()
	d := &dec{b: value}
	sat, err := d.i64()
	if err != nil {
		return nil, err
	}
	script, err := d.script()
	if err != nil {
		return nil, err
	}
	height, err := d.i32()
	if err != nil {
		return nil, err
	}
	return &AddressUnspentValue{Satoshis: sat, Script: script, BlockHeight: height}, nil
}

// countPrefix returns the number of keys under a 1-byte prefix.
func countPrefix(t *testing.T, m *Manager, prefix byte) int {
	t.Helper()
	n := 0
	err := m.iteratePrefixDeobf([]byte{prefix}, func(_, _ []byte) bool {
		n++
		return true
	})
	if err != nil {
		t.Fatalf("iteratePrefix %c: %v", prefix, err)
	}
	return n
}

// TestConnectDisconnectBlockIndex 端到端验证:连接区块写出全部索引字节,断开后
// 全部复原。
func TestConnectDisconnectBlockIndex(t *testing.T) {
	m, err := NewManager(filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	const height int32 = 100
	hash20 := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa,
		0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x01, 0x02, 0x03, 0x04}
	p2pkh := p2pkhScript(hash20)

	prevTxID := chainhash.Hash{0xff, 0xee, 0xdd, 0xcc, 0xbb, 0xaa, 0x99, 0x88,
		0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 0x00,
		0xff, 0xee, 0xdd, 0xcc, 0xbb, 0xaa, 0x99, 0x88,
		0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 0x00}
	prevOut := wire.NewOutPoint(&prevTxID, 0)
	stxo := blockchain.SpentTxOut{
		Amount:   1000,
		PkScript: p2pkh,
		Height:   90,
	}

	// 区块:coinbase (txIdx 0) + 一笔花费交易 (txIdx 1)。
	header := &wire.BlockHeader{Timestamp: time.Unix(1590000000, 0)}
	msg := wire.NewMsgBlock(header)

	coinbase := wire.NewMsgTx(1)
	coinbase.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Index: ^uint32(0)}})
	coinbase.AddTxOut(&wire.TxOut{Value: 500, PkScript: p2pkh})
	if err := msg.AddTransaction(coinbase); err != nil {
		t.Fatal(err)
	}

	spend := wire.NewMsgTx(1)
	spend.AddTxIn(&wire.TxIn{PreviousOutPoint: *prevOut})
	spend.AddTxOut(&wire.TxOut{Value: 600, PkScript: p2pkh})
	if err := msg.AddTransaction(spend); err != nil {
		t.Fatal(err)
	}

	blk := btcutil.NewBlock(msg)
	blk.SetHeight(height)
	spendTxHash := spend.TxHash()
	blockHash := *blk.Hash()

	// ---- 连接 ----
	if err := m.ConnectBlock(nil, blk, []blockchain.SpentTxOut{stxo}); err != nil {
		t.Fatalf("ConnectBlock: %v", err)
	}

	// 1) Address index: 3 条 —— coinbase 输出 +500, spend 输出 +600,
	// spend 输入 -1000。
	if n := countPrefix(t, m, DBAddressIndex); n != 3 {
		t.Fatalf("address index count = %d, want 3", n)
	}

	coinbaseKey := (&AddressIndexKey{
		Type: uint32(AddrIndtPubkeyAddress), HashBytes: hash20,
		BlockHeight: 100, TxIndex: 0, TxHash: coinbase.TxHash(),
		Index: 0, Spending: false,
	}).Key()
	if v, err := m.getValue(coinbaseKey); err != nil {
		t.Fatal(err)
	} else if readI64(t, v) != 500 {
		t.Errorf("coinbase delta = %d, want 500", readI64(t, v))
	}

	recvKey := (&AddressIndexKey{
		Type: uint32(AddrIndtPubkeyAddress), HashBytes: hash20,
		BlockHeight: 100, TxIndex: 1, TxHash: spendTxHash,
		Index: 0, Spending: false,
	}).Key()
	spentKey := (&AddressIndexKey{
		Type: uint32(AddrIndtPubkeyAddress), HashBytes: hash20,
		BlockHeight: 100, TxIndex: 1, TxHash: spendTxHash,
		Index: 0, Spending: true,
	}).Key()

	if v, err := m.getValue(recvKey); err != nil {
		t.Fatal(err)
	} else if readI64(t, v) != 600 {
		t.Errorf("recv delta = %d, want 600", readI64(t, v))
	}
	if v, err := m.getValue(spentKey); err != nil {
		t.Fatal(err)
	} else if readI64(t, v) != -1000 {
		t.Errorf("spent delta = %d, want -1000", readI64(t, v))
	}

	// 2) Address unspent: 输出在,被花费的 prevout 被移除。
	recvUnspentKey := (&AddressUnspentKey{
		Type: uint32(AddrIndtPubkeyAddress), HashBytes: hash20,
		TxHash: spendTxHash, Index: 0,
	}).Key()
	if v, err := m.getValue(recvUnspentKey); err != nil {
		t.Fatal(err)
	} else if v == nil {
		t.Error("expected received output in unspent index")
	} else if u, _ := readUnspent(t, v); u.Satoshis != 600 || !bytes.Equal(u.Script, p2pkh) || u.BlockHeight != 100 {
		t.Errorf("received unspent = %+v", u)
	}

	spentUnspentKey := (&AddressUnspentKey{
		Type: uint32(AddrIndtPubkeyAddress), HashBytes: hash20,
		TxHash: prevTxID, Index: 0,
	}).Key()
	if v, err := m.getValue(spentUnspentKey); err != nil {
		t.Fatal(err)
	} else if v != nil {
		t.Error("spent input unspent entry should be absent")
	}

	// 3) Spent index: prevout -> 该交易。
	spentIdx := (&SpentIndexKey{TxID: prevTxID, OutputIndex: 0}).Key()
	if v, err := m.getValue(spentIdx); err != nil {
		t.Fatal(err)
	} else if v == nil {
		t.Error("expected spent index entry")
	} else {
		d := &dec{b: v}
		spentTx, _ := d.hash()
		inIdx, _ := d.u32()
		blkHeight, _ := d.i32()
		sat, _ := d.i64()
		typ, _ := d.i32()
		if spentTx != spendTxHash || inIdx != 0 || blkHeight != height ||
			sat != 1000 || typ != int32(AddrIndtPubkeyAddress) {
			t.Errorf("spent index value mismatch: %x", v)
		}
	}

	// 4) Timestamp index。
	tk := (&TimestampIndexKey{
		Timestamp: 1590000000,
		BlockHash: blockHash,
	}).Key()
	if v, err := m.getValue(tk); err != nil {
		t.Fatal(err)
	} else if v == nil || len(v) != 1 || v[0] != 0 {
		t.Errorf("timestamp entry wrong: %x", v)
	}

	// 5) 尖点。
	tipHash, tipHeight, err := m.fetchIndexTip()
	if err != nil {
		t.Fatal(err)
	}
	if tipHash == nil || *tipHash != blockHash || tipHeight != 100 {
		t.Errorf("tip = %v height %d, want %v 100", tipHash, tipHeight, blockHash)
	}

	// ---- 断开 ----
	if err := m.DisconnectBlock(nil, blk, []blockchain.SpentTxOut{stxo}); err != nil {
		t.Fatalf("DisconnectBlock: %v", err)
	}

	if n := countPrefix(t, m, DBAddressIndex); n != 0 {
		t.Errorf("address index after disconnect = %d, want 0", n)
	}
	if n := countPrefix(t, m, DBAddressUnspent); n != 1 {
		t.Errorf("unspent after disconnect = %d, want 1 (restored prevout)", n)
	}
	// prevout 恢复到 unspent。
	if v, err := m.getValue(spentUnspentKey); err != nil {
		t.Fatal(err)
	} else if v == nil {
		t.Error("spent input unspent should be restored after disconnect")
	} else if u, _ := readUnspent(t, v); u.Satoshis != 1000 || u.BlockHeight != 90 {
		t.Errorf("restored unspent = %+v", u)
	}
	// spent index 被删。
	if v, _ := m.getValue(spentIdx); v != nil {
		t.Error("spent index should be removed after disconnect")
	}
	// 尖点回退到前一区块。
	tipHash, tipHeight, _ = m.fetchIndexTip()
	if tipHash == nil || *tipHash != blk.MsgBlock().Header.PrevBlock {
		t.Errorf("tip after disconnect = %v, want prev block", tipHash)
	}
	if tipHeight != 99 {
		t.Errorf("tip height after disconnect = %d, want 99", tipHeight)
	}
}

// TestWipeIndex 验证整体重建会清空所有命名空间。
func TestWipeIndex(t *testing.T) {
	m, err := NewManager(filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	// 先随意塞一条地址与花费记录。
	hash20 := make([]byte, 20)
	ak := (&AddressIndexKey{
		Type: 1, HashBytes: hash20, BlockHeight: 1, TxIndex: 0,
		TxHash: chainhash.Hash{1}, Index: 0, Spending: false,
	}).Key()
	batch := new(leveldb.Batch)
	m.putObfuscated(batch, ak, (&enc{}).bytes())
	if err := m.db.Write(batch, nil); err != nil {
		t.Fatal(err)
	}

	if n := countPrefix(t, m, DBAddressIndex); n != 1 {
		t.Fatalf("setup: address index count = %d", n)
	}

	if err := m.wipeIndex(); err != nil {
		t.Fatalf("wipeIndex: %v", err)
	}
	for _, prefix := range []byte{DBAddressIndex, DBAddressUnspent, DBTimestampIndex, DBSpentIndex} {
		if n := countPrefix(t, m, prefix); n != 0 {
			t.Errorf("prefix %c not wiped: %d", prefix, n)
		}
	}
	if v, _ := m.getValue(indexTipKey); v != nil {
		t.Error("tip marker should be wiped")
	}
}
