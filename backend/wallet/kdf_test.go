// Legacy KDF and email/password login tests. The address vector is
// cross-validated against web-wallet: for the same email/password the KDF
// produces the same seed, and index 0 is the seed used directly as a private
// key (P2WPKH), which is exactly web-wallet's FromLegacyRegular derivation.
// 传统 KDF 与邮箱密码登录测试。地址向量与 web-wallet 交叉验证：同一邮箱密码
// 派生相同种子，index 0 为种子直接作为私钥（P2WPKH），即 web-wallet 的
// FromLegacyRegular 派生结果。
package wallet

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/address/v2"
	"github.com/btcsuite/btcd/btcec/v2"
)

// Fixed vector credentials and expected values (computed once via the ported
// KDF; the address matches the original web-wallet for the same credentials).
// 固定向量凭据与期望值（经移植 KDF 计算一次得到；地址与原 web-wallet 对同一
// 凭据的派生结果一致）。
const (
	vectorEmail    = "user@example.com"
	vectorPassword = "S3cretPass2024"

	// vectorSeedHex is the 32-byte seed derived by LegacyRegularSeed.
	// vectorSeedHex 为 LegacyRegularSeed 派生的 32 字节种子。
	vectorSeedHex = "d0623a44b9d62ae116eb1e29974057001e6697f71ea2eb6a521f70564cccafeb"

	// vectorAddr0 is the P2WPKH address of the seed used directly as a private
	// key (web-wallet compatible) / vectorAddr0 为种子直接作为私钥时的 P2WPKH
	// 地址（web-wallet 兼容）。
	vectorAddr0 = "sugar1qx9ff2xclrnyc0tkzzpjxz6atx5vhl02za0qxvk"

	// webWalletSeedHex is an EXTERNAL cross-validation vector: the seed printed
	// by web-wallet's own TestFromLegacyRegular (go test -v) for
	// "test@example.com" / "password123", where web-wallet uses the seed
	// directly as the private key (so its printed privKey == the seed).
	// webWalletSeedHex 为外部交叉验证向量：web-wallet 自身 TestFromLegacyRegular
	// （go test -v）对 "test@example.com" / "password123" 打印的种子；web-wallet
	// 直接用种子当私钥，故其打印的 privKey 即种子。
	webWalletSeedHex = "5bf27fe6ae416cd1b5fefbaea06e8d9e5a555b062c5585d90372461b5719921f"
)

// webWalletAddr replicates web-wallet's FromLegacyRegular address derivation:
// the seed is used directly as a private key, then P2WPKH.
// webWalletAddr 复刻 web-wallet 的 FromLegacyRegular 地址派生：种子直接作为
// 私钥，再取 P2WPKH。
func webWalletAddr(t *testing.T, seed []byte) string {
	t.Helper()
	priv, _ := btcec.PrivKeyFromBytes(seed)
	pkHash := address.Hash160(priv.PubKey().SerializeCompressed())
	addr, err := address.NewAddressWitnessPubKeyHash(pkHash, testNet())
	if err != nil {
		t.Fatalf("web-wallet address: %v", err)
	}
	return addr.EncodeAddress()
}

// TestLegacySeedVector locks the ported KDF against a fixed known seed.
// TestLegacySeedVector 用固定已知种子锁定移植后的 KDF。
func TestLegacySeedVector(t *testing.T) {
	seed, err := LegacyRegularSeed(vectorEmail, vectorPassword)
	if err != nil {
		t.Fatalf("LegacyRegularSeed: %v", err)
	}
	if got := hex.EncodeToString(seed); got != vectorSeedHex {
		t.Errorf("seed mismatch / 种子不一致:\n got %s\nwant %s", got, vectorSeedHex)
	}
}

// TestLegacySeedMatchesWebWallet is the external cross-validation: the seed
// derived here must equal the seed web-wallet derives for the same credentials
// (captured from web-wallet's own test run), proving the KDF port is exact.
// TestLegacySeedMatchesWebWallet 为外部交叉验证：本地派生的种子必须等于
// web-wallet 对相同凭据派生的种子（取自 web-wallet 自身测试输出），
// 证明 KDF 移植完全一致。
func TestLegacySeedMatchesWebWallet(t *testing.T) {
	seed, err := LegacyRegularSeed("test@example.com", "password123")
	if err != nil {
		t.Fatalf("LegacyRegularSeed: %v", err)
	}
	if got := hex.EncodeToString(seed); got != webWalletSeedHex {
		t.Errorf("seed != web-wallet seed / 种子与 web-wallet 不一致:\n got %s\nwant %s", got, webWalletSeedHex)
	}
}

// TestLegacySeedDeterministic verifies the KDF is deterministic and that a
// different password produces a different seed.
// TestLegacySeedDeterministic 验证 KDF 的确定性与不同口令产生不同种子。
func TestLegacySeedDeterministic(t *testing.T) {
	s1, err := LegacyRegularSeed(vectorEmail, vectorPassword)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := LegacyRegularSeed(vectorEmail, vectorPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s1, s2) {
		t.Error("same email/password must derive the same seed / 相同凭据应派生相同种子")
	}
	s3, err := LegacyRegularSeed(vectorEmail, "AnotherPass2024")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(s1, s3) {
		t.Error("different password must derive a different seed / 不同口令应派生不同种子")
	}
}

// TestLegacySeedValidation verifies input validation.
// TestLegacySeedValidation 验证输入校验。
func TestLegacySeedValidation(t *testing.T) {
	cases := []struct{ email, pass string }{
		{"", vectorPassword},
		{vectorEmail, ""},
		{"bad", vectorPassword},
		{vectorEmail, "short"},
	}
	for _, c := range cases {
		if _, err := LegacyRegularSeed(c.email, c.pass); err == nil {
			t.Errorf("expected error for %q / %q", c.email, c.pass)
		}
	}
}

// TestLegacyHybridAddress0MatchesWebWallet is the key cross-validation: index 0
// of a legacy wallet must equal the web-wallet address for the same
// credentials, so old funds are restored on login.
// TestLegacyHybridAddress0MatchesWebWallet 为关键交叉验证：传统钱包的 index 0
// 必须等于同一凭据下的 web-wallet 地址，从而登录即可恢复老资产。
func TestLegacyHybridAddress0MatchesWebWallet(t *testing.T) {
	w, err := NewFromLegacy(vectorEmail, vectorPassword, testNet())
	if err != nil {
		t.Fatalf("NewFromLegacy: %v", err)
	}
	addr0, err := w.Address(0)
	if err != nil {
		t.Fatal(err)
	}
	if addr0 != vectorAddr0 {
		t.Errorf("addr0 mismatch / index0 地址不一致:\n got %s\nwant %s", addr0, vectorAddr0)
	}
	// Independently replicate web-wallet's derivation and compare.
	// 独立复刻 web-wallet 派生并比对。
	seed, err := LegacyRegularSeed(vectorEmail, vectorPassword)
	if err != nil {
		t.Fatal(err)
	}
	if web := webWalletAddr(t, seed); web != addr0 {
		t.Errorf("addr0 != web-wallet address / index0 与 web-wallet 地址不一致: %s != %s", addr0, web)
	}
}

// TestLegacyHybridAddress1IsBIP44 verifies that indices >= 1 are derived via
// the BIP44 path (different from index 0, deterministic across logins).
// TestLegacyHybridAddress1IsBIP44 验证 index 1+ 走 BIP44 派生（与 index 0 不同，
// 且多次登录保持一致）。
func TestLegacyHybridAddress1IsBIP44(t *testing.T) {
	w1, err := NewFromLegacy(vectorEmail, vectorPassword, testNet())
	if err != nil {
		t.Fatal(err)
	}
	a0, err := w1.Address(0)
	if err != nil {
		t.Fatal(err)
	}
	a1, err := w1.Address(1)
	if err != nil {
		t.Fatal(err)
	}
	if a0 == a1 {
		t.Error("index 0 and index 1 must differ / index 0 与 index 1 应不同")
	}
	w2, err := NewFromLegacy(vectorEmail, vectorPassword, testNet())
	if err != nil {
		t.Fatal(err)
	}
	a1b, err := w2.Address(1)
	if err != nil {
		t.Fatal(err)
	}
	if a1 != a1b {
		t.Error("index 1 must be deterministic across logins / index 1 跨登录应一致")
	}
	// index 1 must NOT be the seed-as-privkey address.
	// index 1 不应为种子直接作为私钥的地址。
	seed, err := LegacyRegularSeed(vectorEmail, vectorPassword)
	if err != nil {
		t.Fatal(err)
	}
	if web := webWalletAddr(t, seed); a1 == web {
		t.Error("index 1 must not equal the web-wallet address / index 1 不应等于 web-wallet 地址")
	}
}
