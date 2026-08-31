package wallet

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestSaveUnlockRoundTrip verifies create → save → lock → unlock restores the
// same addresses and next index (the "build → lock → unlock → consistent" gate).
// TestSaveUnlockRoundTrip 验证 创建 → 保存 → 锁定 → 解锁 后地址与下一索引一致
// （即"建 → 锁 → 解锁 → 地址一致"验收点）。
func TestSaveUnlockRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	seed := bytes.Repeat([]byte{0x77}, 32)
	w, err := NewFromSeed(seed, testNet())
	if err != nil {
		t.Fatal(err)
	}
	w.SetNextIndex(3)
	addrBefore, err := w.Address(2)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.SaveWallet(path, "secret"); err != nil {
		t.Fatalf("save wallet / 保存钱包: %v", err)
	}

	w.Lock()
	unlocked, err := UnlockWallet(path, "secret", testNet())
	if err != nil {
		t.Fatalf("unlock wallet / 解锁钱包: %v", err)
	}
	addrAfter, err := unlocked.Address(2)
	if err != nil {
		t.Fatal(err)
	}
	if addrBefore != addrAfter {
		t.Errorf("address mismatch after unlock / 解锁后地址不一致: %s != %s", addrBefore, addrAfter)
	}
	if unlocked.NextIndex() != 3 {
		t.Errorf("next index not restored / 下一地址索引未恢复: got %d want 3", unlocked.NextIndex())
	}
}

// TestUnlockWrongPassphrase verifies a wrong passphrase is rejected.
// TestUnlockWrongPassphrase 验证错误口令被拒绝。
func TestUnlockWrongPassphrase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	w, err := NewFromSeed(bytes.Repeat([]byte{0x88}, 32), testNet())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SaveWallet(path, "right"); err != nil {
		t.Fatal(err)
	}
	if _, err := UnlockWallet(path, "wrong", testNet()); err == nil {
		t.Error("wrong passphrase must be rejected / 错误口令应被拒绝")
	}
}

// TestLockDisablesDerivation verifies a locked wallet cannot derive addresses.
// TestLockDisablesDerivation 验证锁定钱包不能派生地址。
func TestLockDisablesDerivation(t *testing.T) {
	w, err := NewFromSeed(bytes.Repeat([]byte{0x99}, 32), testNet())
	if err != nil {
		t.Fatal(err)
	}
	w.Lock()
	if _, err := w.Address(0); err == nil {
		t.Error("locked wallet must not derive addresses / 锁定钱包不应能派生地址")
	}
}

// TestSaveRequiresUnlocked verifies a locked wallet cannot be saved.
// TestSaveRequiresUnlocked 验证锁定钱包不能保存。
func TestSaveRequiresUnlocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	w, err := NewFromSeed(bytes.Repeat([]byte{0xaa}, 32), testNet())
	if err != nil {
		t.Fatal(err)
	}
	w.Lock()
	if err := w.SaveWallet(path, "secret"); err == nil {
		t.Error("locked wallet must not be saved / 锁定钱包不应能保存")
	}
}

// TestWalletFileCreated verifies the wallet file is written to disk.
// TestWalletFileCreated 验证钱包文件已写入磁盘。
func TestWalletFileCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	w, err := NewFromSeed(bytes.Repeat([]byte{0xbb}, 32), testNet())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SaveWallet(path, "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("wallet file should exist / 钱包文件应存在: %v", err)
	}
}
