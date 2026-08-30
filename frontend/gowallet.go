package main

import (
	"errors"
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
//
// If some wallet is already unlocked in this process, the session is
// swapped: the old one is locked first. Legacy login is deterministic
// derivation — there is no "wrong password" — so an existing session must
// never dead-end the login; the user's intent (open with THESE
// credentials) replaces it.
// Login 用传统邮箱密码 KDF 纯内存派生钱包（web-wallet 兼容地址，不碰
// wallet.db）并保持解锁，返回主地址。
//
// 若进程内已有解锁钱包，则换会话：先锁定旧的。传统登录是确定性派生——
// 不存在"密码错误"——已有会话绝不能让登录陷入死胡同；用户意图（用这套
// 凭据打开）直接替换旧会话。
func (s *WalletService) Login(email, password string) (string, error) {
	if s.mgr.Wallet() != nil {
		s.mgr.Lock()
	}
	w, err := s.mgr.Login(email, password)
	if err != nil {
		return "", err
	}
	return w.Address(0)
}

// LoginWIF imports a WIF-encoded private key and derives a single-key wallet
// purely in memory (hybrid mode: index 0 is the imported key's web-wallet
// address, index 1+ are BIP44 children — funds restore exactly and change
// still works). Never touches wallet.db; the index sidecar is wallet.db.wif.meta.
// Like Login, an already-unlocked session is swapped (deterministic import —
// no wrong-password dead end).
// LoginWIF 导入 WIF 私钥并纯内存派生单钥钱包（混合模式：index 0 即导入
// 私钥的 web-wallet 地址，index 1+ 为 BIP44 子地址——资产完整恢复且找零
// 仍可用）。不碰 wallet.db；索引旁车为 wallet.db.wif.meta。与 Login 一样，
// 已解锁会话直接替换（确定性导入——不存在密码错误的死胡同）。
func (s *WalletService) LoginWIF(wifStr string) (string, error) {
	if s.mgr.Wallet() != nil {
		s.mgr.Lock()
	}
	w, err := s.mgr.LoginWIF(strings.TrimSpace(wifStr))
	if err != nil {
		return "", err
	}
	return w.Address(0)
}

// ExportWIF returns the WIF-encoded private key at the given derivation
// index (compressed). The wallet must be unlocked. Exposing the key is
// intentional — the Keys tab offers per-address WIF export for backups and
// migration to other wallets (web-wallet parity).
// ExportWIF 返回指定派生索引的 WIF 私钥（压缩格式）。钱包须处于解锁状态。
// 暴露私钥是有意为之——Keys 标签页按地址导出 WIF，用于备份与迁移到其他
// 钱包（对齐 web-wallet）。
func (s *WalletService) ExportWIF(index uint32) (string, error) {
	w := s.mgr.Wallet()
	if w == nil {
		return "", errors.New("wallet is locked / 钱包已锁定")
	}
	return w.ExportWIF(index)
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

// WalletAddress is one row of the Keys tab: the derivation index and its
// bech32 address. / Keys 标签页的一行:派生索引与其 bech32 地址。
type WalletAddress struct {
	Index   uint32 `json:"index"`
	Address string `json:"address"`
}

// Addresses lists the derived addresses WITHOUT advancing the index:
// index 0 .. nextIndex-1 (index 0 is always listed — it is the primary
// receive address even before any NextAddress call). Read-only, local,
// works without the node. Errors when the wallet is locked.
// Addresses 只读列出已派生地址,不推进索引:index 0 .. nextIndex-1
// (index 0 始终列出——即使从未调用 NextAddress,它也是主收款地址)。
// 纯本地读操作,无需节点。钱包锁定时报错。
func (s *WalletService) Addresses() ([]WalletAddress, error) {
	w := s.mgr.Wallet()
	if w == nil {
		return nil, errors.New("wallet is locked / 钱包已锁定")
	}
	n := w.NextIndex()
	if n < 1 {
		n = 1 // index 0 is the primary address / index 0 为主地址
	}
	out := make([]WalletAddress, 0, n)
	for i := uint32(0); i < n; i++ {
		a, err := w.Address(i)
		if err != nil {
			return nil, err
		}
		out = append(out, WalletAddress{Index: i, Address: a})
	}
	return out, nil
}
