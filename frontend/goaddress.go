package main

// Address-type derivation for Step 9 (dev_doc/前端方案/ini-node前端内置HD钱包
// 方案-20260830.md §5.12), ported from web-wallet internal/address: the same
// key material yields three Sugarchain address types —
//   bech32 : native segwit P2WPKH      (default)
//   segwit : nested segwit P2SH-P2WPKH (old web-wallet users may hold funds)
//   legacy : traditional P2PKH
//
// The type switch takes effect IMMEDIATELY (web-wallet RefreshWallet parity):
// the wallet page address/QR/balance follow the configured type, and the send
// / token pipelines scan UTXOs of ALL THREE variants so old-type funds stay
// spendable.
//
// 地址三型派生（第 9 步，方案 §5.12），移植自 web-wallet internal/address：
// 同一密钥材料可派生三种糖链地址类型——
//   bech32 : 原生隔离见证 P2WPKH（默认）
//   segwit : 嵌套隔离见证 P2SH-P2WPKH（老 web 用户可能有历史余额）
//   legacy : 传统 P2PKH
// 类型切换即时生效（对齐 web-wallet RefreshWallet）：钱包页地址/二维码/余额
// 跟随配置类型，发送/代币流水线扫描全部三型变体的 UTXO，旧类型资金仍可花费。

import (
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/address/v2"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/txscript/v2"
	"github.com/btcsuite/btcd/wallet"
)

// AddressType strings accepted by AddressFor. / AddressFor 接受的地址类型串。
const (
	TypeBech32 = "bech32"
	TypeSegwit = "segwit"
	TypeLegacy = "legacy"
)

// sugarParams is the single network parameter set shared by every frontend
// service (wallet / send / token). / sugarParams 是前端各服务（钱包/发送/
// 代币）共享的唯一网络参数。
var sugarParams = &chaincfg.SugarMainNetParams

// deriveAddressFor computes the address of the given type for a private key
// (web-wallet address.Derive parity, on this repo's module paths).
// deriveAddressFor 用私钥计算指定类型的地址（对齐 web-wallet 的
// address.Derive，适配本仓库模块路径）。
func deriveAddressFor(priv *btcec.PrivateKey, t string) (string, error) {
	pubKeyCompressed := priv.PubKey().SerializeCompressed()
	pkHash := address.Hash160(pubKeyCompressed)
	switch t {
	case TypeBech32:
		// Native segwit p2wpkh. / 原生隔离见证 p2wpkh。
		addr, err := address.NewAddressWitnessPubKeyHash(pkHash, sugarParams)
		if err != nil {
			return "", fmt.Errorf("bech32 address: %w", err)
		}
		return addr.EncodeAddress(), nil
	case TypeSegwit:
		// Nested segwit: redeem = OP_0 <20-byte hash>, wrapped in P2SH.
		// 嵌套隔离见证：赎回脚本 = OP_0 <20字节哈希>，封装为 P2SH。
		redeem := make([]byte, 0, 22)
		redeem = append(redeem, 0x00, 0x14)
		redeem = append(redeem, pkHash...)
		scriptHash := address.Hash160(redeem)
		addr, err := address.NewAddressScriptHashFromHash(scriptHash, sugarParams)
		if err != nil {
			return "", fmt.Errorf("segwit address: %w", err)
		}
		return addr.EncodeAddress(), nil
	case TypeLegacy:
		// Traditional p2pkh. / 传统 p2pkh。
		addr, err := address.NewAddressPubKeyHash(pkHash, sugarParams)
		if err != nil {
			return "", fmt.Errorf("legacy address: %w", err)
		}
		return addr.EncodeAddress(), nil
	default:
		return "", fmt.Errorf("unknown address type %q (bech32/segwit/legacy) / 未知地址类型 %q（bech32/segwit/legacy）", t, t)
	}
}

// walletAddressVariants lists EVERY address of the live session the wallet
// can spend from: indices 0..nextIndex-1, each in all three types. The list
// is small (nextIndex is tens at most) and getaddressutxos accepts multiple
// addresses in one call, so the threefold fan-out costs one RPC.
// walletAddressVariants 列出当前会话钱包可花费的全部地址：索引
// 0..nextIndex-1，每个索引的三种类型。列表很小（nextIndex 至多数十个），
// getaddressutxos 一次调用即接受多地址，三倍展开只花一次 RPC。
func walletAddressVariants(w *wallet.Wallet) ([]string, error) {
	n := w.NextIndex()
	if n < 1 {
		n = 1 // index 0 is always the primary address / index 0 始终为主地址
	}
	out := make([]string, 0, n*3)
	for i := uint32(0); i < n; i++ {
		priv, err := w.PrivateKey(i)
		if err != nil {
			return nil, err
		}
		for _, t := range []string{TypeBech32, TypeSegwit, TypeLegacy} {
			a, err := deriveAddressFor(priv, t)
			if err != nil {
				return nil, err
			}
			out = append(out, a)
		}
	}
	return out, nil
}

// addressIndexFor finds the derivation index of a wallet address across all
// three type variants (linear scan; the list is small).
// addressIndexFor 在三种类型变体中查找钱包地址的派生索引（线性查找，
// 列表很小）。
func addressIndexFor(w *wallet.Wallet, addr string) (uint32, error) {
	n := w.NextIndex()
	if n < 1 {
		n = 1
	}
	for i := uint32(0); i < n; i++ {
		priv, err := w.PrivateKey(i)
		if err != nil {
			return 0, err
		}
		for _, t := range []string{TypeBech32, TypeSegwit, TypeLegacy} {
			a, err := deriveAddressFor(priv, t)
			if err != nil {
				return 0, err
			}
			if a == addr {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("address %s not derived by this wallet / 地址 %s 不是本钱包派生的", addr, addr)
}

// scriptForAddress decodes an address and builds its scriptPubKey.
// scriptForAddress 解码地址并构造其脚本公钥。
func scriptForAddress(addr string) ([]byte, error) {
	dest, err := address.DecodeAddress(addr, sugarParams)
	if err != nil {
		return nil, fmt.Errorf("decode address %q: %w", addr, err)
	}
	return txscript.PayToAddrScript(dest)
}

// gatherUTXOs collects the wallet's spendable outputs across ALL address
// variants (three types per index). Two-level chain: local node
// getaddressutxos first, external REST fallback second (externalAPI
// non-empty). Shared by the send and token pipelines.
// gatherUTXOs 汇集钱包在全部地址变体（每索引三型）上的可花费输出。
// 两级链：先本地节点 getaddressutxos，再外部 REST 降级（externalAPI
// 非空时）。发送与代币流水线共用。
func gatherUTXOs(w *wallet.Wallet, externalAPI string) ([]UTXO, error) {
	addrs, err := walletAddressVariants(w)
	if err != nil {
		return nil, err
	}
	utxos, nodeErr := nodeUTXOs(addrs)
	if nodeErr == nil {
		return utxos, nil
	}
	if externalAPI == "" {
		return nil, fmt.Errorf("node offline and no external API configured (node error: %v) / 节点离线且未配置外部 API（节点错误：%v）", nodeErr, nodeErr)
	}
	utxos, extErr := externalUTXOs(externalAPI, addrs)
	if extErr != nil {
		return nil, fmt.Errorf("node: %v; external: %v / 节点：%v；外部：%v", nodeErr, extErr, nodeErr, extErr)
	}
	return utxos, nil
}

// nodeUTXOsAll is the TokenService wrapper over gatherUTXOs (unlocked check
// + shared chain). / nodeUTXOsAll 是 TokenService 对 gatherUTXOs 的封装
// （解锁检查 + 共享两级链）。
func nodeUTXOsAll(ws *WalletService, externalAPI string) ([]UTXO, error) {
	w := ws.mgr.Wallet()
	if w == nil {
		return nil, errors.New("wallet is locked / 钱包已锁定")
	}
	return gatherUTXOs(w, externalAPI)
}

// AddressFor is the Step 9 Wails binding: it returns the wallet address of
// addrType ("bech32" / "segwit" / "legacy") at the derivation index. The
// switch takes effect immediately — the caller re-invokes with the new type
// and gets the new address without any restart (web-wallet RefreshWallet
// parity). / AddressFor 是第 9 步的 Wails 绑定：返回派生索引上指定类型
// （"bech32" / "segwit" / "legacy"）的钱包地址。切换即时生效——调用方用
// 新类型再次调用即得到新地址，无需任何重启（对齐 web-wallet 的
// RefreshWallet）。
func (s *WalletService) AddressFor(index uint32, addrType string) (string, error) {
	w := s.mgr.Wallet()
	if w == nil {
		return "", errors.New("wallet is locked / 钱包已锁定")
	}
	priv, err := w.PrivateKey(index)
	if err != nil {
		return "", err
	}
	return deriveAddressFor(priv, addrType)
}
