// walletrpcserver.go implements the built-in HD wallet JSON-RPC commands on
// top of the wallet.Manager. The local (key) commands always work; the query
// commands (getwalletinfo / listtransactions / listunspent) additionally
// require the sugar index (--sugarindex) and report a clear error when it is
// not enabled.
// walletrpcserver.go 在 wallet.Manager 之上实现内置 HD 钱包的 JSON-RPC 命令。
// 本地（密钥）命令始终可用；查询命令（getwalletinfo / listtransactions /
// listunspent）还需启用 sugar 索引（--sugarindex），未启用时报清晰错误。
package main

import (
	"fmt"
	"sort"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/sugarindex"
	"github.com/btcsuite/btcd/wallet"
)

// sugarPerCoin is the number of satoshis per sugar unit.
// sugarPerCoin 为每糖单位对应的聪数。
const sugarPerCoin = 1e8

// satoshiToSugar converts satoshis to sugar units (float) for wallet RPCs.
// satoshiToSugar 将聪转换为糖单位（浮点）供钱包 RPC 使用。
func satoshiToSugar(sat int64) float64 {
	return float64(sat) / sugarPerCoin
}

// walletRPCError builds an RPCError with a wallet error code.
// walletRPCError 构造带钱包错误码的 RPCError。
func walletRPCError(code btcjson.RPCErrorCode, format string, a ...interface{}) error {
	return &btcjson.RPCError{Code: code, Message: fmt.Sprintf(format, a...)}
}

// unlockedWallet returns the currently unlocked wallet or an error explaining
// that the wallet is locked or not created.
// unlockedWallet 返回当前已解锁钱包，否则返回说明钱包锁定或未创建的错误。
func unlockedWallet(s *rpcServer) (*wallet.Wallet, error) {
	if s.cfg.Wallet == nil {
		return nil, walletRPCError(btcjson.ErrRPCWallet,
			"built-in wallet is not available / 内置钱包不可用")
	}
	w := s.cfg.Wallet.Wallet()
	if w == nil {
		return nil, walletRPCError(btcjson.ErrRPCWalletUnlockNeeded,
			"wallet is locked or not created / 钱包已锁定或未创建")
	}
	return w, nil
}

// walletAddresses lists the wallet's allocated addresses, always including the
// primary address at index 0.
// walletAddresses 列出钱包已分配的地址，始终包含索引 0 的主地址。
func walletAddresses(w *wallet.Wallet) ([]string, error) {
	n := w.NextIndex()
	if n == 0 {
		n = 1
	}
	addrs := make([]string, 0, n)
	for i := uint32(0); i < n; i++ {
		a, err := w.Address(i)
		if err != nil {
			return nil, err
		}
		addrs = append(addrs, a)
	}
	return addrs, nil
}

// handleCreateWallet implements the createwallet command. It generates a fresh
// HD wallet, saves it encrypted with the passphrase, and returns the mnemonic
// once so the caller can present it for backup.
// handleCreateWallet 实现 createwallet 命令：生成全新 HD 钱包，以口令加密保存，
// 返回助记词（一次性）供备份展示。
func handleCreateWallet(s *rpcServer, cmd interface{},
	closeChan <-chan struct{}) (interface{}, error) {

	if s.cfg.Wallet == nil {
		return nil, walletRPCError(btcjson.ErrRPCWallet,
			"built-in wallet is not available / 内置钱包不可用")
	}
	c := cmd.(*btcjson.CreateWalletCmd)
	passphrase := ""
	if c.Passphrase != nil {
		passphrase = *c.Passphrase
	}
	mnemonic, w, err := s.cfg.Wallet.Create(passphrase)
	if err != nil {
		return nil, walletRPCError(btcjson.ErrRPCWallet, "%v", err)
	}
	addr, err := w.Address(0)
	if err != nil {
		return nil, walletRPCError(btcjson.ErrRPCWallet, "%v", err)
	}
	return struct {
		Name     string `json:"name"`
		Mnemonic string `json:"mnemonic"`
		Address  string `json:"address"`
	}{
		Name:     "default",
		Mnemonic: mnemonic,
		Address:  addr,
	}, nil
}

// handleGetNewAddress implements the getnewaddress command, returning the next
// derived address and advancing the persisted index.
// handleGetNewAddress 实现 getnewaddress 命令，返回下一个派生地址并推进持久化索引。
func handleGetNewAddress(s *rpcServer, cmd interface{},
	closeChan <-chan struct{}) (interface{}, error) {

	if _, err := unlockedWallet(s); err != nil {
		return nil, err
	}
	addr, err := s.cfg.Wallet.NextAddress()
	if err != nil {
		return nil, walletRPCError(btcjson.ErrRPCWallet, "%v", err)
	}
	return addr, nil
}

// walletInfoResult is the result of getwalletinfo, shaped for the ini-node
// frontend WalletState.
// walletInfoResult 为 getwalletinfo 的结果，按 ini-node 前端 WalletState 形状设计。
type walletInfoResult struct {
	Locked     bool    `json:"locked"`
	WalletName string  `json:"walletname"`
	Address    string  `json:"address"`
	Total      float64 `json:"total"`
	Confirmed  float64 `json:"confirmed"`
	Pending    float64 `json:"pending"`
	Immature   float64 `json:"immature"`
	WatchOnly  float64 `json:"watchonly"`
}

// handleGetWalletInfo implements the getwalletinfo command. The balance fields
// are aggregated from the sugar index; when the index is disabled the balances
// stay zero (no error) so the lifecycle commands remain usable.
// handleGetWalletInfo 实现 getwalletinfo 命令。余额字段由 sugar 索引聚合；
// 索引未启用时余额保持 0（不报错），以便生命周期命令保持可用。
func handleGetWalletInfo(s *rpcServer, cmd interface{},
	closeChan <-chan struct{}) (interface{}, error) {

	w, err := unlockedWallet(s)
	if err != nil {
		return nil, err
	}
	addr, err := w.Address(0)
	if err != nil {
		return nil, walletRPCError(btcjson.ErrRPCWallet, "%v", err)
	}

	res := walletInfoResult{
		WalletName: "default",
		Address:    addr,
	}
	if s.cfg.SugarIndex == nil {
		return &res, nil // no index → balances stay zero / 未启用索引 → 余额为 0
	}
	keys, err := decodeIndexKeys(s, []string{addr})
	if err != nil {
		return nil, err
	}
	bal, err := s.addressBalance(keys)
	if err != nil {
		return nil, err
	}
	res.Confirmed = satoshiToSugar(bal.BalanceSpendable)
	res.Immature = satoshiToSugar(bal.BalanceImmature)
	res.Total = satoshiToSugar(bal.Balance)
	return &res, nil
}

// handleListTransactions implements the listtransactions command, grouping the
// per-output address deltas of the sugar index into per-transaction entries,
// newest first, honoring the optional count parameter (default 10).
// handleListTransactions 实现 listtransactions 命令，把 sugar 索引中按输出的
// 地址增量聚合为按交易的条目（最新在前），并遵循可选 count 参数（默认 10）。
func handleListTransactions(s *rpcServer, cmd interface{},
	closeChan <-chan struct{}) (interface{}, error) {

	if s.cfg.SugarIndex == nil {
		return nil, sugarIndexNotEnabled()
	}
	w, err := unlockedWallet(s)
	if err != nil {
		return nil, err
	}
	c := cmd.(*btcjson.ListTransactionsCmd)
	count := 10
	if c.Count != nil && *c.Count >= 0 {
		count = *c.Count
	}

	addrs, err := walletAddresses(w)
	if err != nil {
		return nil, walletRPCError(btcjson.ErrRPCWallet, "%v", err)
	}
	keys, err := decodeIndexKeys(s, addrs)
	if err != nil {
		return nil, err
	}
	nHeight := s.cfg.Chain.BestSnapshot().Height

	// Group deltas by txid, summing the net amount per tx.
	// 按 txid 归组增量，汇总每笔交易净额。
	type txGroup struct {
		txid     string
		address  string
		height   int32
		satoshis int64
	}
	groupByTx := make(map[string]*txGroup)
	var order []string
	for _, k := range keys {
		addrStr, err := sugarindex.EncodeIndexAddress(k.addrType, k.hash, s.cfg.ChainParams)
		if err != nil {
			return nil, sugarIndexError(rpcInvalidAddressOrKey, "Unknown address type")
		}
		err = s.cfg.SugarIndex.ReadAddressIndex(k.addrType, k.hash, 0, 0,
			func(entry sugarindex.AddressIndexEntry) bool {
				txid := entry.TxHash.String()
				g, ok := groupByTx[txid]
				if !ok {
					g = &txGroup{txid: txid, address: addrStr, height: entry.BlockHeight}
					groupByTx[txid] = g
					order = append(order, txid)
				}
				g.satoshis += entry.Satoshis
				return true
			})
		if err != nil {
			return nil, sugarIndexError(rpcInvalidAddressOrKey,
				"No information available for address")
		}
	}

	// Sort by height descending (newest first), mirroring a wallet history.
	// 按高度降序（最新在前），对齐钱包历史。
	sort.SliceStable(order, func(i, j int) bool {
		return groupByTx[order[i]].height > groupByTx[order[j]].height
	})

	// Cache block timestamps per height to avoid re-reading full blocks.
	// 按高度缓存区块时间戳，避免重复读取整块。
	blockTimeCache := make(map[int32]int64)
	result := make([]btcjson.ListTransactionsResult, 0, len(order))
	for _, txid := range order {
		g := groupByTx[txid]
		if count > 0 && len(result) >= count {
			break
		}
		category := "receive"
		if g.satoshis < 0 {
			category = "send"
		}
		blockTime, ok := blockTimeCache[g.height]
		if !ok {
			if blk, err := s.cfg.Chain.BlockByHeight(g.height); err == nil {
				blockTime = blk.MsgBlock().Header.Timestamp.Unix()
				blockTimeCache[g.height] = blockTime
			}
		}
		result = append(result, btcjson.ListTransactionsResult{
			Category:      category,
			Address:       g.address,
			Amount:        satoshiToSugar(g.satoshis),
			TxID:          g.txid,
			BlockHeight:   &g.height,
			BlockTime:     blockTime,
			Time:          blockTime,
			Confirmations: int64(nHeight - g.height + 1),
			Trusted:       true,
		})
	}
	return result, nil
}

// handleListUnspent implements the listunspent command backed by the sugar index.
// handleListUnspent 实现 listunspent 命令，由 sugar 索引支撑。
func handleListUnspent(s *rpcServer, cmd interface{},
	closeChan <-chan struct{}) (interface{}, error) {

	if s.cfg.SugarIndex == nil {
		return nil, sugarIndexNotEnabled()
	}
	w, err := unlockedWallet(s)
	if err != nil {
		return nil, err
	}
	addrs, err := walletAddresses(w)
	if err != nil {
		return nil, walletRPCError(btcjson.ErrRPCWallet, "%v", err)
	}
	keys, err := decodeIndexKeys(s, addrs)
	if err != nil {
		return nil, err
	}
	nHeight := s.cfg.Chain.BestSnapshot().Height

	var result []btcjson.ListUnspentResult
	for _, k := range keys {
		addrStr, err := sugarindex.EncodeIndexAddress(k.addrType, k.hash, s.cfg.ChainParams)
		if err != nil {
			return nil, sugarIndexError(rpcInvalidAddressOrKey, "Unknown address type")
		}
		outs, err := s.cfg.SugarIndex.ReadAddressUnspent(k.addrType, k.hash)
		if err != nil {
			return nil, sugarIndexError(rpcInvalidAddressOrKey,
				"No information available for address")
		}
		for _, o := range outs {
			result = append(result, btcjson.ListUnspentResult{
				TxID:          o.TxHash.String(),
				Vout:          o.Index,
				Address:       addrStr,
				ScriptPubKey:  fmt.Sprintf("%x", o.Script),
				Amount:        satoshiToSugar(o.Satoshis),
				Confirmations: int64(nHeight - o.BlockHeight + 1),
				Spendable:     true,
			})
		}
	}
	return result, nil
}

// handleWalletPassphrase implements the walletpassphrase command (unlock). The
// timeout argument is accepted for compatibility but unlocks are not timed.
// handleWalletPassphrase 实现 walletpassphrase 命令（解锁）。为兼容保留 timeout
// 参数，但解锁不做超时。
func handleWalletPassphrase(s *rpcServer, cmd interface{},
	closeChan <-chan struct{}) (interface{}, error) {

	if s.cfg.Wallet == nil {
		return nil, walletRPCError(btcjson.ErrRPCWallet,
			"built-in wallet is not available / 内置钱包不可用")
	}
	c := cmd.(*btcjson.WalletPassphraseCmd)
	if _, err := s.cfg.Wallet.Unlock(c.Passphrase); err != nil {
		return nil, walletRPCError(btcjson.ErrRPCWallet, "%v", err)
	}
	return nil, nil
}

// handleWalletLock implements the walletlock command.
// handleWalletLock 实现 walletlock 命令。
func handleWalletLock(s *rpcServer, cmd interface{},
	closeChan <-chan struct{}) (interface{}, error) {

	if s.cfg.Wallet == nil {
		return nil, walletRPCError(btcjson.ErrRPCWallet,
			"built-in wallet is not available / 内置钱包不可用")
	}
	s.cfg.Wallet.Lock()
	return nil, nil
}
