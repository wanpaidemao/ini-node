package wallet

import (
	"os"
	"path/filepath"
	"testing"
)

// TestManagerLoginUnlocks verifies Login derives a wallet in memory, hands out
// index 0 (the web-wallet address) first, and does not create wallet.db.
// TestManagerLoginUnlocks 验证 Login 纯内存派生钱包、先分配 index 0（web-wallet
// 地址），且不创建 wallet.db。
func TestManagerLoginUnlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultWalletName)
	m := NewManager(path, testNet())

	if _, err := m.Login(vectorEmail, vectorPassword); err != nil {
		t.Fatalf("Login: %v", err)
	}
	w := m.Wallet()
	if w == nil {
		t.Fatal("wallet should be unlocked / 钱包应已解锁")
	}
	addr0, err := m.NextAddress()
	if err != nil {
		t.Fatal(err)
	}
	if addr0 != vectorAddr0 {
		t.Errorf("first legacy address mismatch / 首个传统地址不一致: %s != %s", addr0, vectorAddr0)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Login must not create wallet.db / Login 不应创建 wallet.db (err=%v)", err)
	}
}

// TestManagerLoginLegacySidecar verifies legacy login persists its index in a
// separate sidecar, so a subsequent login resumes where it left off.
// TestManagerLoginLegacySidecar 验证传统登录将索引持久化到独立旁车，后续登录
// 从中断处继续。
func TestManagerLoginLegacySidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultWalletName)
	m := NewManager(path, testNet())

	if _, err := m.Login(vectorEmail, vectorPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NextAddress(); err != nil { // index 0 / 索引 0
		t.Fatal(err)
	}
	if _, err := m.NextAddress(); err != nil { // index 1 / 索引 1
		t.Fatal(err)
	}
	m.Lock()

	m2 := NewManager(path, testNet())
	if _, err := m2.Login(vectorEmail, vectorPassword); err != nil {
		t.Fatal(err)
	}
	// After resuming at index 2, the next address must be index 2, not 0.
	// 恢复至索引 2 后，下一地址应为索引 2 而非 0。
	addr2, err := m2.NextAddress()
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewFromLegacy(vectorEmail, vectorPassword, testNet())
	if err != nil {
		t.Fatal(err)
	}
	want2, err := w.Address(2)
	if err != nil {
		t.Fatal(err)
	}
	if addr2 != want2 {
		t.Errorf("legacy sidecar resume mismatch / 传统旁车恢复不一致: %s != %s", addr2, want2)
	}
}

// TestManagerLoginIsolatedFromBip39 verifies legacy login works even when a
// BIP39 wallet.db exists and never touches it (pure isolation).
// TestManagerLoginIsolatedFromBip39 验证即使已存在 BIP39 wallet.db，传统登录
// 仍可工作且不触碰它（完全隔离）。
func TestManagerLoginIsolatedFromBip39(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultWalletName)
	m := NewManager(path, testNet())

	// Create a BIP39 wallet first. / 先创建 BIP39 钱包。
	if _, _, err := m.Create("bip39pass"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NextAddress(); err != nil { // advance BIP39 index / 推进 BIP39 索引
		t.Fatal(err)
	}
	m.Lock()

	// Login with legacy credentials; it must still return index 0 (the
	// web-wallet address) first and must not overwrite wallet.db.
	// 用传统凭据登录；仍应首先返回 index 0（web-wallet 地址），且不覆盖 wallet.db。
	m2 := NewManager(path, testNet())
	w, err := m2.Login(vectorEmail, vectorPassword)
	if err != nil {
		t.Fatalf("legacy login after BIP39 create must work / BIP39 创建后传统登录应可用: %v", err)
	}
	a0, err := w.Address(0)
	if err != nil {
		t.Fatal(err)
	}
	if a0 != vectorAddr0 {
		t.Errorf("legacy index 0 must stay the web-wallet address / 传统 index 0 应保持 web-wallet 地址: %s", a0)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("BIP39 wallet.db must remain / BIP39 wallet.db 应保留: %v", err)
	}
}
