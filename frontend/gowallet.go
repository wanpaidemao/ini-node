package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/wallet"
)

// WalletService runs INSIDE the frontend (Wails) process and exposes the
// local wallet lifecycle over Wails bindings. Local operations — create,
// passphrase unlock, email/password login, lock, next address — are pure
// local operations of wallet.Manager, so they work WITHOUT the node running.
// Chain data (balance / history / UTXO / send) still goes through the RPC
// proxy and degrades gracefully when the node is offline.
//
// WalletService 运行在前端（Wails）进程内，通过 Wails bindings 暴露本地钱包
// 生命周期。本地操作——创建、口令解锁、邮箱密码登录、锁定、新地址——是
// wallet.Manager 的纯本地操作，节点未运行也可用。链上数据（余额/历史/UTXO/
// 发送）仍走 RPC 代理，节点离线时优雅降级。
type WalletService struct {
	mgr *wallet.Manager
}

// WalletStatus is the local wallet snapshot returned by Status.
// WalletStatus 是 Status 返回的本地钱包快照。
type WalletStatus struct {
	Exists   bool   `json:"exists"`   // wallet.db present on disk / 磁盘上存在 wallet.db
	Unlocked bool   `json:"unlocked"` // a wallet is unlocked in memory / 内存中有已解锁钱包
	Address  string `json:"address"`  // primary address when unlocked (index 0) / 解锁时的主地址（index 0）
	Name     string `json:"name"`     // display name (matches RPC getwalletinfo) / 显示名（与 RPC getwalletinfo 一致）
}

// WalletCreation is the one-time result of Create (the mnemonic is shown
// exactly once for backup).
// WalletCreation 是 Create 的一次性返回结果（助记词仅展示一次供备份）。
type WalletCreation struct {
	Mnemonic string `json:"mnemonic"`
	Address  string `json:"address"`
	Name     string `json:"name"`
}

// newWalletService resolves the wallet path from the runtime ini (datadir),
// falling back to btcd's default data directory. The network is fixed to
// sugarmainnet, matching the node's chainParams.
// newWalletService 从 runtime ini（datadir）解析钱包路径，找不到时回退
// btcd 默认数据目录。网络固定为 sugarmainnet，与节点 chainParams 一致。
func newWalletService() *WalletService {
	datadir := ""
	if iniPath := findIniPath(); iniPath != "" {
		datadir = strings.TrimSpace(parseIni(iniPath)["datadir"])
	}
	if datadir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			datadir = filepath.Join(home, "AppData", "Local", "Btcd")
		} else {
			datadir = "."
		}
	}
	return &WalletService{
		mgr: wallet.NewManager(
			filepath.Join(datadir, wallet.DefaultWalletName),
			&chaincfg.SugarMainNetParams),
	}
}

// Status reports the local wallet state. Pure local: no RPC involved.
// Status 报告本地钱包状态。纯本地，不涉及 RPC。
func (s *WalletService) Status() WalletStatus {
	st := WalletStatus{Name: "default"}
	st.Exists = s.mgr.Exists()
	if w := s.mgr.Wallet(); w != nil {
		st.Unlocked = true
		// Mirror getwalletinfo: show the index-0 primary address.
		// 与 getwalletinfo 一致：展示 index 0 主地址。
		if a, err := w.Address(0); err == nil {
			st.Address = a
		}
	}
	return st
}

// Create generates a fresh BIP39 wallet, saves it encrypted with the
// passphrase, and returns the mnemonic exactly once. Errors when a wallet
// already exists or one is already unlocked.
// Create 生成全新 BIP39 钱包，以口令加密保存，助记词仅返回一次。
// 钱包已存在或已有解锁钱包时报错。
func (s *WalletService) Create(passphrase string) (WalletCreation, error) {
	mnemonic, w, err := s.mgr.Create(passphrase)
	if err != nil {
		return WalletCreation{}, err
	}
	addr, err := w.Address(0)
	if err != nil {
		return WalletCreation{}, err
	}
	return WalletCreation{Mnemonic: mnemonic, Address: addr, Name: "default"}, nil
}

// Unlock decrypts wallet.db with the passphrase and leaves it unlocked.
// A wrong passphrase returns an error. Returns the primary address.
// Unlock 用口令解密 wallet.db 并保持解锁；口令错误返回错误。返回主地址。
func (s *WalletService) Unlock(passphrase string) (string, error) {
	w, err := s.mgr.Unlock(passphrase)
	if err != nil {
		return "", err
	}
	return w.Address(0)
}

// Login derives a wallet from the legacy email/password KDF purely in
// memory (web-wallet compatible address; never touches wallet.db) and
// leaves it unlocked. Returns the primary address.
// Login 用传统邮箱密码 KDF 纯内存派生钱包（web-wallet 兼容地址，不碰
// wallet.db）并保持解锁。返回主地址。
func (s *WalletService) Login(email, password string) (string, error) {
	w, err := s.mgr.Login(email, password)
	if err != nil {
		return "", err
	}
	return w.Address(0)
}

// Lock drops the in-memory key material. Idempotent.
// Lock 丢弃内存密钥材料。幂等。
func (s *WalletService) Lock() {
	s.mgr.Lock()
}

// NextAddress returns the next derived address and advances the persisted
// index (separate sidecars for BIP39 and legacy, so no address reuse).
// NextAddress 返回下一个派生地址并推进持久化索引（BIP39 与传统各自独立
// 旁车文件，避免地址复用）。
func (s *WalletService) NextAddress() (string, error) {
	return s.mgr.NextAddress()
}
