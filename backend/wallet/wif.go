package wallet

import (
	"fmt"

	"github.com/btcsuite/btcd/chaincfg/v2"
)

// NewFromWIF creates a single-key wallet from a WIF-encoded private key in
// hybrid mode: index 0 is the WIF key itself (the web-wallet address, so
// imported funds restore exactly), while indices >= 1 are BIP44 children of
// the key bytes used as the BIP32 master seed (same shape as the legacy
// login wallet — change/new-address capability is preserved). Like Login it
// is memory-only: it never touches wallet.db.
// NewFromWIF 用 WIF 私钥以混合模式创建单钥钱包：index 0 就是 WIF 私钥本身
// （即 web-wallet 地址，导入即可恢复原资产），index 1+ 则把私钥字节作为
// BIP32 主种子派生 BIP44 子地址（与邮箱登录钱包同构——保留找零/新地址能力）。
// 与 Login 一样纯内存：不碰 wallet.db。
func NewFromWIF(wifStr string, net *chaincfg.Params) (*Wallet, error) {
	priv, err := ImportWIF(wifStr, net)
	if err != nil {
		return nil, err
	}
	// The serialized private key is exactly 32 bytes — a valid BIP32 seed.
	// Serialize() allocates a fresh copy, so the wallet owns these bytes.
	// 序列化私钥恰为 32 字节——合法的 BIP32 种子。Serialize() 返回新副本，
	// 钱包独立持有这些字节。
	seed := priv.Serialize()
	w, err := NewFromSeed(seed, net)
	if err != nil {
		return nil, fmt.Errorf("WIF wallet / WIF 钱包: %w", err)
	}
	w.legacy = true // index 0 uses the seed (= the WIF key) directly / index 0 直接用种子（即 WIF 私钥）
	w.wif = true    // marks the WIF sidecar kind / 标记 WIF 旁车文件类别
	return w, nil
}

// IsWIF reports whether the wallet was created from an imported WIF key.
// IsWIF 报告钱包是否由导入的 WIF 私钥创建。
func (w *Wallet) IsWIF() bool {
	return w.wif
}

// WIFAddress returns the imported key's address (index 0). It is the exact
// web-wallet address the funds live on.
// WIFAddress 返回导入私钥的地址（index 0），即资产所在的 web-wallet 原地址。
func (w *Wallet) WIFAddress() (string, error) {
	return w.Address(0)
}
