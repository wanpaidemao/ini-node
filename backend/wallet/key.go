package wallet

import (
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/chaincfg/v2"
)

// PrivateKey returns the compressed private key at the external-chain index.
// PrivateKey 返回外部链指定索引处的压缩私钥。
func (w *Wallet) PrivateKey(index uint32) (*btcec.PrivateKey, error) {
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
