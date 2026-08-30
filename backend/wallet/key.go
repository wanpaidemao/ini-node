package wallet

import (
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/chaincfg/v2"
)

// PrivateKey returns the private key at the external-chain index. For legacy
// hybrid wallets index 0 returns the seed used directly as a private key (so
// it matches web-wallet), while indices >= 1 use the standard BIP44 derivation.
// PrivateKey 返回外部链指定索引处的私钥。传统混合钱包中 index 0 直接返回种子作为
// 私钥（从而对齐 web-wallet），index 1+ 使用标准 BIP44 派生。
func (w *Wallet) PrivateKey(index uint32) (*btcec.PrivateKey, error) {
	if w.legacy && index == 0 {
		if err := w.locked(); err != nil {
			return nil, err
		}
		if len(w.seed) != 32 {
			// Guard against the lock window where the seed is already
			// zeroed but the master key has not been cleared yet.
			// 防御锁定窗口：种子已清零而主密钥尚未清空的时刻。
			return nil, fmt.Errorf("wallet is locked / 钱包已锁定")
		}
		// PrivKeyFromBytes returns (priv, pub); the pub is not needed here.
		// PrivKeyFromBytes 返回 (priv, pub)；此处不需要公钥。
		priv, _ := btcec.PrivKeyFromBytes(w.seed)
		return priv, nil
	}
	key, err := w.childKey(ExternalChain, index)
	if err != nil {
		return nil, err
	}
	return key.ECPrivKey()
}

// ExportWIF returns the WIF-encoded private key at the index (compressed).
// ExportWIF 返回指定索引私钥的 WIF 编码（压缩格式）。
func (w *Wallet) ExportWIF(index uint32) (string, error) {
	priv, err := w.PrivateKey(index)
	if err != nil {
		return "", err
	}
	wif, err := btcutil.NewWIF(priv, w.net, true)
	if err != nil {
		return "", err
	}
	return wif.String(), nil
}

// ImportWIF decodes a WIF-encoded private key and verifies it belongs to the
// given network. Used to import single keys from web-wallet.
// ImportWIF 解码 WIF 私钥并校验其网络归属，用于从 web-wallet 导入单钥。
func ImportWIF(wifStr string, net *chaincfg.Params) (*btcec.PrivateKey, error) {
	wif, err := btcutil.DecodeWIF(wifStr)
	if err != nil {
		return nil, fmt.Errorf("decode WIF / 解码 WIF: %w", err)
	}
	if !wif.IsForNet(net) {
		return nil, fmt.Errorf("WIF is not for the given network / WIF 不属于目标网络")
	}
	return wif.PrivKey, nil
}
