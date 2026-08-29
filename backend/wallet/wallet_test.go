package wallet

import (
	"bytes"
	"testing"

	"github.com/btcsuite/btcd/address/v2"
	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/chaincfg/v2"
)

// testNet returns the Sugar mainnet params for tests.
// testNet 返回测试用的糖链主网参数。
func testNet() *chaincfg.Params {
	return &chaincfg.SugarMainNetParams
}

// TestMnemonicValid tests generation and validation of mnemonics.
// TestMnemonicValid 测试助记词的生成与校验。
func TestMnemonicValid(t *testing.T) {
	for _, bits := range []WordCount{TwelveWords, TwentyFourWords} {
		m, err := GenerateMnemonic(bits)
		if err != nil {
			t.Fatalf("generate mnemonic(%d): %v", bits, err)
		}
		if !ValidateMnemonic(m) {
			t.Errorf("generated mnemonic is not valid / 生成的助记词无效: %s", m)
		}
	}
}

// TestMnemonicDeterministicSeed verifies the same mnemonic+passphrase always
// derives the same seed, and a different passphrase derives a different seed.
// TestMnemonicDeterministicSeed 验证相同助记词+口令派生相同种子，不同口令派生不同种子。
func TestMnemonicDeterministicSeed(t *testing.T) {
	m := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	seed1 := MnemonicToSeed(m, "")
	seed2 := MnemonicToSeed(m, "")
	if !bytes.Equal(seed1, seed2) {
		t.Fatal("same mnemonic must derive the same seed / 相同助记词应派生相同种子")
	}
	seed3 := MnemonicToSeed(m, "pass")
	if bytes.Equal(seed1, seed3) {
		t.Fatal("different passphrase must derive a different seed / 不同口令应派生不同种子")
	}
}

// TestDerivationDeterministic verifies the same seed yields the same address
// and different indices yield different addresses.
// TestDerivationDeterministic 验证相同种子派生相同地址、不同索引派生不同地址。
func TestDerivationDeterministic(t *testing.T) {
	seed := bytes.Repeat([]byte{0x42}, 32)
	w1, err := NewFromSeed(seed, testNet())
	if err != nil {
		t.Fatal(err)
	}
	w2, err := NewFromSeed(seed, testNet())
	if err != nil {
		t.Fatal(err)
	}

	a0, err := w1.Address(0)
	if err != nil {
		t.Fatal(err)
	}
	a0b, err := w2.Address(0)
	if err != nil {
		t.Fatal(err)
	}
	if a0 != a0b {
		t.Errorf("same seed must derive the same address / 相同种子应派生相同地址: %s != %s", a0, a0b)
	}
	a1, err := w1.Address(1)
	if err != nil {
		t.Fatal(err)
	}
	if a0 == a1 {
		t.Error("different indices must derive different addresses / 不同索引应派生不同地址")
	}
}

// TestAddressRoundTrip verifies the derived address decodes back and belongs
// to the Sugar network.
// TestAddressRoundTrip 验证派生地址可解码回环且属于糖链网络。
func TestAddressRoundTrip(t *testing.T) {
	seed := bytes.Repeat([]byte{0x11}, 32)
	w, err := NewFromSeed(seed, testNet())
	if err != nil {
		t.Fatal(err)
	}
	addrStr, err := w.Address(0)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := address.DecodeAddress(addrStr, testNet())
	if err != nil {
		t.Fatalf("decode address / 解码地址 %s: %v", addrStr, err)
	}
	if !addr.IsForNet(testNet()) {
		t.Errorf("address is not for the Sugar network / 地址不属于糖链网络: %s", addrStr)
	}
}

// TestWIFRoundTrip verifies WIF export/import round-trips to the same key.
// TestWIFRoundTrip 验证 WIF 导出/导入回环到同一私钥。
func TestWIFRoundTrip(t *testing.T) {
	seed := bytes.Repeat([]byte{0x33}, 32)
	w, err := NewFromSeed(seed, testNet())
	if err != nil {
		t.Fatal(err)
	}
	wifStr, err := w.ExportWIF(0)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := ImportWIF(wifStr, testNet())
	if err != nil {
		t.Fatal(err)
	}
	wif, err := btcutil.NewWIF(priv, testNet(), true)
	if err != nil {
		t.Fatal(err)
	}
	if wif.String() != wifStr {
		t.Error("WIF round-trip mismatch / WIF 回环不一致")
	}
}

// TestNewFromSeedRejectsBadSeed verifies seed length validation.
// TestNewFromSeedRejectsBadSeed 验证种子长度校验。
func TestNewFromSeedRejectsBadSeed(t *testing.T) {
	if _, err := NewFromSeed([]byte("short"), testNet()); err == nil {
		t.Error("short seed must be rejected / 过短种子应被拒绝")
	}
}
