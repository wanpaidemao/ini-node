// Copyright (c) 2026 The btcd developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package btcjson

// GetAddressBalanceCmd defines the getaddressbalance JSON-RPC command.
type GetAddressBalanceCmd struct {
	Address string
}

// NewGetAddressBalanceCmd returns a new instance which can be used to issue a
// getaddressbalance JSON-RPC command.
func NewGetAddressBalanceCmd(address string) *GetAddressBalanceCmd {
	return &GetAddressBalanceCmd{
		Address: address,
	}
}

// getAddressesBalanceParams mirrors the wire object passed as the first
// argument to getaddressesbalance.
type getAddressesBalanceParams struct {
	Addresses []string `json:"addresses"`
}

// GetAddressesBalanceCmd defines the getaddressesbalance JSON-RPC command.
type GetAddressesBalanceCmd struct {
	Addresses getAddressesBalanceParams
}

// NewGetAddressesBalanceCmd returns a new instance which can be used to issue
// a getaddressesbalance JSON-RPC command.
func NewGetAddressesBalanceCmd(addresses []string) *GetAddressesBalanceCmd {
	return &GetAddressesBalanceCmd{
		Addresses: getAddressesBalanceParams{Addresses: addresses},
	}
}

// getAddressUtxosParams mirrors the wire object passed as the first argument
// to getaddressutxos.
type getAddressUtxosParams struct {
	Addresses []string `json:"addresses"`
}

// GetAddressUtxosCmd defines the getaddressutxos JSON-RPC command.
type GetAddressUtxosCmd struct {
	Addresses getAddressUtxosParams
	Amount    *float64 `jsonrpcdefault:"0"`
	ChainInfo *bool    `jsonrpcdefault:"false"`
}

// NewGetAddressUtxosCmd returns a new instance which can be used to issue a
// getaddressutxos JSON-RPC command.
func NewGetAddressUtxosCmd(addresses []string, amount float64,
	chainInfo *bool) *GetAddressUtxosCmd {
	amt := amount
	return &GetAddressUtxosCmd{
		Addresses: getAddressUtxosParams{Addresses: addresses},
		Amount:    &amt,
		ChainInfo: chainInfo,
	}
}

// getAddressDeltasParams defines the wire object passed to getaddressdeltas.
type getAddressDeltasParams struct {
	Addresses []string `json:"addresses"`
	Start     int64    `json:"start"`
	End       int64    `json:"end"`
	ChainInfo *bool    `json:"chainInfo"`
}

// GetAddressDeltasCmd defines the getaddressdeltas JSON-RPC command.
type GetAddressDeltasCmd struct {
	Deltas *getAddressDeltasParams
}

// NewGetAddressDeltasCmd returns a new instance which can be used to issue a
// getaddressdeltas JSON-RPC command.
func NewGetAddressDeltasCmd(addresses []string, start, end int64,
	chainInfo *bool) *GetAddressDeltasCmd {
	return &GetAddressDeltasCmd{
		Deltas: &getAddressDeltasParams{
			Addresses: addresses,
			Start:     start,
			End:       end,
			ChainInfo: chainInfo,
		},
	}
}

// getAddressTxidsParams defines the wire object of getaddresstxids.
type getAddressTxidsParams struct {
	Addresses []string `json:"addresses"`
	Start     int64    `json:"start"`
	End       int64    `json:"end"`
}

// GetAddressTxidsCmd defines the getaddresstxids JSON-RPC command.
type GetAddressTxidsCmd struct {
	Txids *getAddressTxidsParams
}

// NewGetAddressTxidsCmd returns a new instance which can be used to issue a
// getaddresstxids JSON-RPC command.
func NewGetAddressTxidsCmd(addresses []string, start, end int64) *GetAddressTxidsCmd {
	return &GetAddressTxidsCmd{
		Txids: &getAddressTxidsParams{
			Addresses: addresses,
			Start:     start,
			End:       end,
		},
	}
}

// getAddressMempoolParams defines the wire object of getaddressmempool.
type getAddressMempoolParams struct {
	Addresses []string `json:"addresses"`
}

// GetAddressMempoolCmd defines the getaddressmempool JSON-RPC command.
type GetAddressMempoolCmd struct {
	Addresses *getAddressMempoolParams
}

// NewGetAddressMempoolCmd returns a new instance which can be used to issue a
// getaddressmempool JSON-RPC command.
func NewGetAddressMempoolCmd(addresses []string) *GetAddressMempoolCmd {
	return &GetAddressMempoolCmd{
		Addresses: &getAddressMempoolParams{Addresses: addresses},
	}
}

// GetBlockHashesCmd defines the getblockhashes JSON-RPC command.
type GetBlockHashesCmd struct {
	High    int64
	Low     int64
	Options *GetBlockHashesOptionsCmd
}

// GetBlockHashesOptionsCmd is the options object for getblockhashes.
type GetBlockHashesOptionsCmd struct {
	NoOrphans    bool `json:"noOrphans"`
	LogicalTimes bool `json:"logicalTimes"`
}

// NewGetBlockHashesCmd returns a new instance which can be used to issue a
// getblockhashes JSON-RPC command.
func NewGetBlockHashesCmd(high, low int64,
	options *GetBlockHashesOptionsCmd) *GetBlockHashesCmd {
	return &GetBlockHashesCmd{
		High:    high,
		Low:     low,
		Options: options,
	}
}

// getSpentInfoParams defines the wire object of getspentinfo.
type getSpentInfoParams struct {
	Txid  string `json:"txid"`
	Index uint32 `json:"index"`
}

// GetSpentInfoCmd defines the getspentinfo JSON-RPC command.
type GetSpentInfoCmd struct {
	Inputs *getSpentInfoParams `json:"inputs"`
}

// NewGetSpentInfoCmd returns a new instance which can be used to issue a
// getspentinfo JSON-RPC command.
func NewGetSpentInfoCmd(txid string, index uint32) *GetSpentInfoCmd {
	return &GetSpentInfoCmd{
		Inputs: &getSpentInfoParams{
			Txid:  txid,
			Index: index,
		},
	}
}

// GetAddressBalanceResult models the data returned from the getaddressbalance
// command.
type GetAddressBalanceResult struct {
	Balance          int64 `json:"balance"`
	BalanceImmature  int64 `json:"balance_immature"`
	BalanceSpendable int64 `json:"balance_spendable"`
	Received         int64 `json:"received"`
}

// GetAddressUtxosResult models the data returned from the getaddressutxos
// command (one entry per unspent output).
type GetAddressUtxosResult struct {
	Address     string `json:"address"`
	Txid        string `json:"txid"`
	OutputIndex uint32 `json:"outputIndex"`
	Script      string `json:"script"`
	Satoshis    int64  `json:"satoshis"`
	Height      int32  `json:"height"`
}

// GetAddressUtxosChainInfoResult models the getaddressutxos result when the
// chainInfo option is set.
type GetAddressUtxosChainInfoResult struct {
	Utxos  []GetAddressUtxosResult `json:"utxos"`
	Hash   string                  `json:"hash"`
	Height int32                   `json:"height"`
}

// GetAddressDeltasResult models the data returned from the getaddressdeltas
// command (one entry per address delta).
type GetAddressDeltasResult struct {
	Satoshis   int64  `json:"satoshis"`
	Txid       string `json:"txid"`
	Index      uint32 `json:"index"`
	Blockindex uint32 `json:"blockindex"`
	Height     int32  `json:"height"`
	Address    string `json:"address"`
}

// GetAddressDeltasChainInfoResult models the getaddressdeltas result when the
// chainInfo option is set.
type GetAddressDeltasChainInfoResult struct {
	Deltas []GetAddressDeltasResult `json:"deltas"`
	Start  GetAddressDeltaEndResult `json:"start"`
	End    GetAddressDeltaEndResult `json:"end"`
}

// GetAddressDeltaEndResult models one start/end entry of the chainInfo
// getaddressdeltas result.
type GetAddressDeltaEndResult struct {
	Hash   string `json:"hash"`
	Height int32  `json:"height"`
}

// GetAddressMempoolResult models the data returned from the
// getaddressmempool command.
type GetAddressMempoolResult struct {
	Address   string  `json:"address"`
	Txid      string  `json:"txid"`
	Index     uint32  `json:"index"`
	Satoshis  int64   `json:"satoshis"`
	Timestamp int64   `json:"timestamp"`
	Prevtxid  *string `json:"prevtxid,omitempty"`
	Prevout   *uint32 `json:"prevout,omitempty"`
}

// GetBlockHashesResult models one entry of the getblockhashes result when the
// logicalTimes option is set.
type GetBlockHashesResult struct {
	Blockhash string `json:"blockhash"`
	Logicalts uint32 `json:"logicalts"`
}

// GetSpentInfoResult models the data returned from the getspentinfo command.
type GetSpentInfoResult struct {
	Txid   string `json:"txid"`
	Index  uint32 `json:"index"`
	Height int32  `json:"height"`
}

func init() {
	flags := UsageFlag(0)

	MustRegisterCmd("getaddressbalance", (*GetAddressBalanceCmd)(nil), flags)
	MustRegisterCmd("getaddressesbalance", (*GetAddressesBalanceCmd)(nil), flags)
	MustRegisterCmd("getaddressutxos", (*GetAddressUtxosCmd)(nil), flags)
	MustRegisterCmd("getaddressdeltas", (*GetAddressDeltasCmd)(nil), flags)
	MustRegisterCmd("getaddresstxids", (*GetAddressTxidsCmd)(nil), flags)
	MustRegisterCmd("getaddressmempool", (*GetAddressMempoolCmd)(nil), flags)
	MustRegisterCmd("getblockhashes", (*GetBlockHashesCmd)(nil), flags)
	MustRegisterCmd("getspentinfo", (*GetSpentInfoCmd)(nil), flags)
}
