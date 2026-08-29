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

// Wallet is a BIP44 HD wallet rooted at a BIP39 seed.
// Wallet 是基于 BIP39 种子的 BIP44 分层确定性钱包。
type Wallet struct {
	net       *chaincfg.Params        // network parameters / 网络参数
	masterKey *hdkeychain.ExtendedKey // BIP32 master key / BIP32 主密钥
	coinType  uint32                  // hardened coin type / 硬化币种类型
	account   uint32                  // hardened account / 硬化账户
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
	return &Wallet{net: net, masterKey: master, coinType: coinType, account: account}, nil
}

// childKey derives the extended key at path m/44'/coinType'/account'/chain/index.
// childKey 派生路径 m/44'/coinType'/account'/chain/index 的扩展密钥。
func (w *Wallet) childKey(chain, index uint32) (*hdkeychain.ExtendedKey, error) {
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
	key, err := w.childKey(ExternalChain, index)
	if err != nil {
		return "", fmt.Errorf("derive address key / 派生地址密钥: %w", err)
	}
	pub, err := key.ECPubKey()
	if err != nil {
		return "", fmt.Errorf("public key / 公钥: %w", err)
	}
	pkHash := address.Hash160(pub.SerializeCompressed())
	addr, err := address.NewAddressWitnessPubKeyHash(pkHash, w.net)
	if err != nil {
		return "", fmt.Errorf("bech32 address / bech32 地址: %w", err)
	}
	return addr.EncodeAddress(), nil
}
