package wallet

import (
	"os"
	"path/filepath"
	"testing"
)

// TestManagerLoginUnlocks verifies Login derives a wallet in memory, hands out
// index 0 (the web-wallet address) first, and does not create a wallet file.
// TestManagerLoginUnlocks 验证 Login 纯内存派生钱包、先分配 index 0（web-wallet
// 地址），且不创建钱包文件。
func TestManagerLoginUnlocks(t *testing.T) {
	m := NewManager(t.TempDir(), testNet())

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
	if names, _ := m.List(); len(names) != 0 {
		t.Errorf("Login must not create a wallet file / Login 不应创建钱包文件 (names=%v)", names)
	}
}

// TestManagerLoginLegacySidecar verifies legacy login persists its index in a
// separate sidecar, so a subsequent login resumes where it left off.
// TestManagerLoginLegacySidecar 验证传统登录将索引持久化到独立旁车，后续登录
// 从中断处继续。
func TestManagerLoginLegacySidecar(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, testNet())

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

	m2 := NewManager(dir, testNet())
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
// BIP39 wallet exists and never touches it (pure isolation).
// TestManagerLoginIsolatedFromBip39 验证即使已存在 BIP39 钱包，传统登录
// 仍可工作且不触碰它（完全隔离）。
func TestManagerLoginIsolatedFromBip39(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, testNet())

	// Create a BIP39 wallet first. / 先创建 BIP39 钱包。
	if _, _, err := m.Create("main", "bip39pass"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NextAddress(); err != nil { // advance BIP39 index / 推进 BIP39 索引
		t.Fatal(err)
	}
	m.Lock()

	// Login with legacy credentials; it must still return index 0 (the
	// web-wallet address) first and must not overwrite the BIP39 wallet.
	// 用传统凭据登录；仍应首先返回 index 0（web-wallet 地址），且不覆盖 BIP39 钱包。
	m2 := NewManager(dir, testNet())
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
	if _, err := os.Stat(filepath.Join(dir, "main.db")); err != nil {
		t.Errorf("BIP39 wallet file must remain / BIP39 钱包文件应保留: %v", err)
	}
}

// TestManagerCreateRestoreList verifies the multi-wallet directory layout:
// Create writes <name>.db, Restore rebuilds from a mnemonic under another
// name, and List returns both sorted.
// TestManagerCreateRestoreList 验证多钱包目录布局：Create 写入 <name>.db、
// Restore 从助记词以另一名字重建、List 返回两者（排序）。
func TestManagerCreateRestoreList(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, testNet())

	mnemonic, _, err := m.Create("alpha", "pass")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m.Lock()

	if _, err := m.Restore("beta", mnemonic, "pass"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	m.Lock()

	names, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("List mismatch / List 不一致: %v", names)
	}
}

// TestManagerRestoreInvalidMnemonic verifies an invalid mnemonic is rejected.
// TestManagerRestoreInvalidMnemonic 验证无效助记词被拒绝。
func TestManagerRestoreInvalidMnemonic(t *testing.T) {
	m := NewManager(t.TempDir(), testNet())
	if _, err := m.Restore("x", "not a valid mnemonic phrase", "pass"); err == nil {
		t.Error("invalid mnemonic must be rejected / 无效助记词应被拒绝")
	}
}

// TestManagerMigrateLegacy verifies the old <datadir>/wallet.db is migrated
// into <datadir>/wallet/default.db on first use.
// TestManagerMigrateLegacy 验证旧 <datadir>/wallet.db 首次使用时迁移为
// <datadir>/wallet/default.db。
func TestManagerMigrateLegacy(t *testing.T) {
	datadir := t.TempDir()
	legacy := filepath.Join(datadir, "wallet.db")
	seed := make([]byte, 32)
	w, err := NewFromSeed(seed, testNet())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SaveWallet(legacy, "pass"); err != nil {
		t.Fatal(err)
	}

	m := NewManager(filepath.Join(datadir, DefaultWalletDir), testNet())
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("legacy wallet.db should be gone after migration / 迁移后旧 wallet.db 应消失")
	}
	names, _ := m.List()
	if len(names) != 1 || names[0] != "default" {
		t.Errorf("migrated wallet not listed / 迁移后的钱包未列出: %v", names)
	}
	if _, err := os.Stat(filepath.Join(datadir, DefaultWalletDir, "default.db")); err != nil {
		t.Errorf("migrated default.db missing / 迁移后的 default.db 缺失: %v", err)
	}
}