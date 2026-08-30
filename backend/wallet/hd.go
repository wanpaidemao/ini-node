// Package wallet implements a BIP32/BIP44 HD wallet for the built-in
// ini-node frontend wallet. It is the foundation layer of the built-in HD
// wallet plan (dev_doc/前端方案/ini-node前端内置HD钱包方案-20260830.md).
// 包 wallet 实现内置 HD 钱包（BIP32/BIP44），是 ini-node 前端内置 HD 钱包方案的
// 基础层（方案见 dev_doc/前端方案/ini-node前端内置HD钱包方案-20260830.md）。
package wallet

import (
	"fmt"

	"github.com/btcsuite/btcd/address/v2"
	"github.com/btcsuite/btcd/btcutil/v2/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg/v2"
)

// BIP44 path constants. / BIP44 派生路径常量。
const (
	// Purpose is the BIP44 purpose hardened index (44').
	// Purpose 为 BIP44 purpose 硬化索引（44'）。
	Purpose = 44

	// DefaultCoinType is the hardened coin type. Sugarchain has no registered
	// BIP44 coin type, so this is a configurable placeholder (0, same as Bitcoin).
	// 覆盖：Sugarchain 未注册 BIP44 币种类型，此处为可配置占位（0，同 Bitcoin）。
	DefaultCoinType = 0

	// DefaultAccount is the hardened account index (0').
	// DefaultAccount 为硬化账户索引（0'）。
	DefaultAccount = 0

	// ExternalChain selects the external (receiving) chain.
	// ExternalChain 选择外部（收款）链。
	ExternalChain = 0

	// InternalChain selects the internal (change) chain.
	// InternalChain 选择内部（找零）链。
	InternalChain = 1
)

// Wallet is a BIP44 HD wallet rooted at a BIP39 seed, optionally operating in
// legacy (email/password) hybrid mode.
// Wallet 是基于 BIP39 种子的 BIP44 分层确定性钱包，可选传统（邮箱密码）混合模式。
type Wallet struct {
	net       *chaincfg.Params        // network parameters / 网络参数
	masterKey *hdkeychain.ExtendedKey // BIP32 master key (nil while locked) / BIP32 主密钥（锁定时为 nil）
	coinType  uint32                  // hardened coin type / 硬化币种类型
	account   uint32                  // hardened account / 硬化账户
	seed      []byte                  // BIP39 seed, cleared on lock / BIP39 种子（锁定时清空）
	nextIndex uint32                  // next external address index / 下一个外部地址索引
	legacy    bool                    // legacy hybrid mode: index 0 is the seed used directly as a private key / 传统混合模式：index 0 直接用种子作为私钥
	wif       bool                    // WIF import mode: legacy hybrid wallet created from an imported WIF key / WIF 导入模式：由导入 WIF 私钥创建的传统混合钱包
}

// sidecarSuffix returns the meta sidecar suffix of the wallet kind: "" for
// BIP39, ".legacy" for email/password login, ".wif" for WIF import. Keeping
// the three kinds in separate sidecars guarantees each wallet hands out its
// own index 0 first and never skips addresses because of another kind.
// sidecarSuffix 返回钱包类别的旁车文件后缀：BIP39 为 ""、邮箱密码登录为
// ".legacy"、WIF 导入为 ".wif"。三种类别各自独立旁车，保证每类钱包都从
// 自己的 index 0 起分配，绝不因其他类别而跳号。
func (w *Wallet) sidecarSuffix() string {
	switch {
	case w.wif:
		return ".wif"
	case w.legacy:
		return ".legacy"
	default:
		return ""
	}
}

// NewFromSeed creates a Wallet from a BIP39-derived seed using the default
// BIP44 path m/44'/coinType'/account'.
// NewFromSeed 使用 BIP39 种子创建钱包，默认路径 m/44'/coinType'/account'。
func NewFromSeed(seed []byte, net *chaincfg.Params) (*Wallet, error) {
	return NewFromSeedPath(seed, net, DefaultCoinType, DefaultAccount)
}

// NewFromSeedPath creates a Wallet with a custom coin type and account.
// NewFromSeedPath 使用自定义币种类型与账户创建钱包。
func NewFromSeedPath(seed []byte, net *chaincfg.Params, coinType, account uint32) (*Wallet, error) {
	if len(seed) < 16 || len(seed) > 64 {
		return nil, fmt.Errorf("seed must be 16-64 bytes / 种子长度需为 16-64 字节")
	}
	master, err := hdkeychain.NewMaster(seed, net)
	if err != nil {
		return nil, fmt.Errorf("new master key / 创建主密钥: %w", err)
	}
	return &Wallet{
		net:       net,
		masterKey: master,
		coinType:  coinType,
		account:   account,
		seed:      append([]byte(nil), seed...),
	}, nil
}

// NewFromLegacy creates a Wallet from the legacy email/password KDF seed in
// hybrid mode: the 32-byte seed is used directly as the private key for address
// index 0 (so it matches the original web-wallet address exactly, restoring old
// funds), and as the BIP32 master seed for indices >= 1 (standard BIP44 path,
// preserving multi-address and change support). The seed is only kept in memory.
// NewFromLegacy 用传统邮箱密码 KDF 种子以混合模式创建钱包：32 字节种子直接作为
// index 0 的私钥（从而与原 web-wallet 地址完全一致、可恢复老资产），同时作为
// BIP32 主种子用于 index 1+（标准 BIP44 路径，保留多地址与找零能力）。种子仅存内存。
func NewFromLegacy(email, password string, net *chaincfg.Params) (*Wallet, error) {
	seed, err := LegacyRegularSeed(email, password)
	if err != nil {
		return nil, err
	}
	w, err := NewFromSeed(seed, net)
	zeroBytes(seed) // clear the temporary seed copy / 清空临时种子副本
	if err != nil {
		return nil, err
	}
	w.legacy = true
	return w, nil
}

// Lock zeroes the in-memory key material (seed and master key), leaving the
// wallet unable to derive addresses until unlocked again via UnlockWallet.
// Lock 清空内存中的密钥材料（种子与主密钥），钱包进入锁定态，需重新解锁后才能派生地址。
func (w *Wallet) Lock() {
	if w.masterKey != nil {
		w.masterKey.Zero()
	}
	if w.seed != nil {
		for i := range w.seed {
			w.seed[i] = 0
		}
		w.seed = nil
	}
	w.masterKey = nil
}

// locked returns a descriptive error when the wallet is locked.
// locked 在钱包锁定状态下返回描述性错误。
func (w *Wallet) locked() error {
	if w.masterKey == nil {
		return fmt.Errorf("wallet is locked / 钱包已锁定")
	}
	return nil
}

// NextIndex returns the next external address index to hand out.
// NextIndex 返回下一个待分配的外部地址索引。
func (w *Wallet) NextIndex() uint32 {
	return w.nextIndex
}

// SetNextIndex advances the next address index (persisted on save).
// SetNextIndex 推进下一个地址索引（保存时持久化）。
func (w *Wallet) SetNextIndex(i uint32) {
	w.nextIndex = i
}

// childKey derives the extended key at path m/44'/coinType'/account'/chain/index.
// childKey 派生路径 m/44'/coinType'/account'/chain/index 的扩展密钥。
func (w *Wallet) childKey(chain, index uint32) (*hdkeychain.ExtendedKey, error) {
	if err := w.locked(); err != nil {
		return nil, err
	}
	purposeKey, err := w.masterKey.Derive(uint32(Purpose) + hdkeychain.HardenedKeyStart) // 44'
	if err != nil {
		return nil, err
	}
	coinKey, err := purposeKey.Derive(w.coinType + hdkeychain.HardenedKeyStart) // coinType'
	if err != nil {
		return nil, err
	}
	acctKey, err := coinKey.Derive(w.account + hdkeychain.HardenedKeyStart) // account'
	if err != nil {
		return nil, err
	}
	chainKey, err := acctKey.Derive(chain)
	if err != nil {
		return nil, err
	}
	return chainKey.Derive(index)
}

// Address returns the bech32 (P2WPKH) address at the given index on the
// external (receiving) chain. Bech32 is the default as Segwit is always
// active on the Sugar chain.
// Address 返回外部链上指定索引的 bech32（P2WPKH）地址。糖链始终启用隔离见证，
// 故 bech32 为默认地址类型。
func (w *Wallet) Address(index uint32) (string, error) {
	priv, err := w.PrivateKey(index)
	if err != nil {
		return "", err
	}
	pub := priv.PubKey()
	pkHash := address.Hash160(pub.SerializeCompressed())
	addr, err := address.NewAddressWitnessPubKeyHash(pkHash, w.net)
	if err != nil {
		return "", fmt.Errorf("bech32 address / bech32 地址: %w", err)
	}
	return addr.EncodeAddress(), nil
}
