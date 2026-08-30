package main

// TokenService implements Step 6/6b (dev_doc/前端方案/ini-node前端内置HD钱包
// 方案-20260830.md §5.8) INSIDE the frontend (Wails) process: the token layer
// is an off-chain ledger at a REST endpoint (default tokenstest.sugar.wtf), so
// the node has NO token logic — we port web-wallet's internal/api/token.go
// TokenClient and the four token operations (transfer/create/issue/burn).
//
// Token transactions are ordinary SUGAR txs carrying an OP_RETURN payload
// (built by the token layer) plus an optional marker output:
//   transfer : marker pays the recipient  (tokens follow the marker output)
//   create   : marker pays the layer fee_address (cost.create by ticker type)
//   issue    : marker pays the layer fee_address (cost.issue by ticker type)
//   burn     : no marker output
//
// TokenService 在前端（Wails）进程内实现第 6/6b 步（方案 §5.8）：代币层是
// REST 端点上的链外账本（默认 tokenstest.sugar.wtf），节点本身没有代币逻辑
// ——这里移植 web-wallet internal/api/token.go 的 TokenClient 与四个代币
// 操作（转账/创建/增发/销毁）。
// 代币交易是携带 OP_RETURN 负载（由代币层构造）外加可选 marker 输出的普通
// SUGAR 交易：
//   transfer : marker 付给收款人（代币跟随 marker 输出）
//   create   : marker 付给代币层 fee_address（按 ticker 类型取 cost.create）
//   issue    : marker 付给代币层 fee_address（按 ticker 类型取 cost.issue）
//   burn     : 无 marker 输出

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/txscript/v2"
	"github.com/btcsuite/btcd/wallet"
	"github.com/btcsuite/btcd/wire/v2"
)

// DefaultTokenAPI is the default token layer endpoint (testnet ledger).
// Mainnet endpoint TBD. / DefaultTokenAPI 为默认代币层端点（测试网账本）。
// 主网端点待确认。
const DefaultTokenAPI = "https://tokenstest.sugar.wtf"

// TokenService is the Wails-bound token layer client + operations. It shares
// the WalletService session (same unlock/lock lifecycle) like SendService.
// TokenService 是 Wails 绑定的代币层客户端与操作集合。与 SendService 一样
// 共享 WalletService 会话（同一解锁/锁定生命周期）。
type TokenService struct {
	ws *WalletService
}

// newTokenService builds the service around the shared wallet session.
// newTokenService 基于共享钱包会话构建服务。
func newTokenService(ws *WalletService) *TokenService {
	return &TokenService{ws: ws}
}

// ---- Token layer REST client (ported from web-wallet internal/api/token.go)
// ---- 代币层 REST 客户端（移植自 web-wallet internal/api/token.go） ----

// TokenBalance is one entry of the address token balance list.
// TokenBalance 是地址代币余额列表中的一项。
type TokenBalance struct {
	Ticker   string `json:"ticker"`   // token ticker / 代币符号
	Value    int64  `json:"value"`    // amount in base units / 基本单位金额
	Decimals int    `json:"decimals"` // decimals for display / 显示小数位
}

// TokenInfo holds metadata of a single token.
// TokenInfo 保存单个代币的元数据。
type TokenInfo struct {
	Ticker     string `json:"ticker"`     // token ticker / 代币符号
	Decimals   int    `json:"decimals"`   // decimals / 小数位
	Reissuable bool   `json:"reissuable"` // can be reissued / 可增发
	Supply     int64  `json:"supply"`     // total supply / 总供应
}

// TokenLayerParams is the /layer/params response (marker costs + fee address).
// TokenLayerParams 是 /layer/params 的响应（marker 成本与手续费地址）。
type TokenLayerParams struct {
	Chain      string          `json:"chain"`       // chain name / 链名
	FeeAddress string          `json:"fee_address"` // marker recipient / marker 收款地址
	Cost       TokenLayerCost  `json:"cost"`        // marker costs by ticker type / 按类型的 marker 成本
}

// TokenLayerCost holds the marker cost tables for create and issue.
// TokenLayerCost 保存 create/issue 的 marker 成本表。
type TokenLayerCost struct {
	Create TokenCostTier `json:"create"` // cost.create / 创建成本
	Issue  TokenCostTier `json:"issue"`  // cost.issue / 增发成本
}

// TokenCostTier is the cost table keyed by ticker type (root/sub/unique).
// TokenCostTier 是按 ticker 类型（root/sub/unique）分档的成本表。
type TokenCostTier struct {
	Root   int64 `json:"root"`   // plain ticker, e.g. "ABC" / 普通 ticker
	Sub    int64 `json:"sub"`    // sub ticker, e.g. "ABC/DEF" / 子 ticker
	Unique int64 `json:"unique"` // unique ticker, e.g. "ABC#1" / 唯一 ticker
}

// TickerType classifies a ticker (web-wallet parity): '#' -> unique,
// '/' -> sub, else root. / TickerType 对 ticker 分类（对齐 web-wallet）：
// 含 '#' → unique，含 '/' → sub，否则 root。
func TickerType(ticker string) string {
	switch {
	case strings.Contains(ticker, "#"):
		return "unique"
	case strings.Contains(ticker, "/"):
		return "sub"
	default:
		return "root"
	}
}

// CostFor picks the tier by ticker type. / CostFor 按 ticker 类型取档位。
func (c TokenCostTier) CostFor(tier string) int64 {
	switch tier {
	case "unique":
		return c.Unique
	case "sub":
		return c.Sub
	default:
		return c.Root
	}
}

// CreateCost returns the marker cost of creating this ticker.
// CreateCost 返回创建该 ticker 的 marker 成本。
func (c TokenCostTier) CreateCost(ticker string) int64 {
	return c.CostFor(TickerType(ticker))
}

// IssueCost returns the marker cost of issuing this ticker.
// IssueCost 返回增发该 ticker 的 marker 成本。
func (c TokenCostTier) IssueCost(ticker string) int64 {
	return c.CostFor(TickerType(ticker))
}

// tokenBase normalizes the endpoint URL (default when empty).
// tokenBase 归一化端点 URL（为空时用默认值）。
func tokenBase(api string) string {
	api = strings.TrimSpace(api)
	if api == "" {
		return DefaultTokenAPI
	}
	return strings.TrimRight(api, "/")
}

// tokenGet performs GET {api}{path} and decodes JSON into out.
// tokenGet 执行 GET {api}{path} 并把 JSON 解码到 out。
func tokenGet(api, path string, out interface{}) error {
	req, err := http.NewRequest(http.MethodGet, tokenBase(api)+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ini-node-wallet")
	resp, err := restClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return json.Unmarshal(body, out)
}

// tokenPostJSON POSTs a JSON payload and decodes the response into out.
// tokenPostJSON 发送 JSON 负载并把响应解码到 out。
func tokenPostJSON(api, path string, payload, out interface{}) error {
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := restClient.Post(tokenBase(api)+path, "application/json", bytes.NewReader(buf))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return json.Unmarshal(body, out)
}

// tokenMessage POSTs an operation payload and returns the hex `data` field
// (the OP_RETURN payload built by the token layer).
// tokenMessage 提交操作负载并返回十六进制 `data` 字段（代币层构造的
// OP_RETURN 负载）。
func tokenMessage(api, path string, payload interface{}) (string, error) {
	var resp struct {
		Data string `json:"data"`
	}
	if err := tokenPostJSON(api, path, payload, &resp); err != nil {
		return "", err
	}
	if resp.Data == "" {
		return "", fmt.Errorf("%s: empty payload in response / %s: 响应负载为空", path, path)
	}
	return resp.Data, nil
}

// ---- Read bindings ---- / ---- 只读 bindings ----

// Balances returns the token balances of the WHOLE wallet: every derived
// address (all three address types) is queried and the balances are merged
// by ticker (HD wallets spread funds across addresses).
// Balances 返回整个钱包的代币余额：查询全部派生地址（三种地址类型），
// 按代币符号合并（HD 钱包资金分散在多个地址）。
func (s *TokenService) Balances(tokenAPI string) ([]TokenBalance, error) {
	w, err := s.unlocked()
	if err != nil {
		return nil, err
	}
	addrs, err := walletAddressVariants(w)
	if err != nil {
		return nil, err
	}
	merged := map[string]*TokenBalance{}
	for _, a := range addrs {
		var resp struct {
			Balances []TokenBalance `json:"balances"`
		}
		if err := tokenGet(tokenAPI, "/layer/address/"+a, &resp); err != nil {
			// Per-address failure is not fatal (offline/unknown address);
			// only fail when EVERY address query failed.
			// 单地址失败不致命（离线/未知地址）；全部失败才报错。
			continue
		}
		for _, b := range resp.Balances {
			if cur, ok := merged[b.Ticker]; ok {
				cur.Value += b.Value
				if b.Decimals > cur.Decimals {
					cur.Decimals = b.Decimals
				}
			} else {
				bb := b
				merged[b.Ticker] = &bb
			}
		}
	}
	if len(merged) == 0 {
		// Distinguish "no tokens" from "service unreachable": probe /layer/params
		// once so the UI can show a connectivity error instead of an empty list.
		// 区分"无代币"与"服务不可达"：探测一次 /layer/params,
		// 让 UI 能显示连接错误而非空列表。
		var probe struct {
			Chain string `json:"chain"`
		}
		if err := tokenGet(tokenAPI, "/layer/params", &probe); err != nil {
			return nil, fmt.Errorf("token layer unreachable: %w / 代币层不可达：%w", err, err)
		}
	}
	out := make([]TokenBalance, 0, len(merged))
	for _, b := range merged {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ticker < out[j].Ticker })
	return out, nil
}

// Info returns metadata of one ticker.
// Info 返回单个代币的元数据。
func (s *TokenService) Info(ticker, tokenAPI string) (TokenInfo, error) {
	var out TokenInfo
	if err := tokenGet(tokenAPI, "/layer/token/"+ticker, &out); err != nil {
		return TokenInfo{}, err
	}
	return out, nil
}

// LayerParams returns the token layer parameters (fee address + costs).
// LayerParams 返回代币层参数（手续费地址与成本）。
func (s *TokenService) LayerParams(tokenAPI string) (TokenLayerParams, error) {
	var out TokenLayerParams
	if err := tokenGet(tokenAPI, "/layer/params", &out); err != nil {
		return TokenLayerParams{}, err
	}
	return out, nil
}

// ---- Operation bindings ---- / ---- 操作 bindings ----

// Transfer sends `value` base units of `ticker` to `to`.
// marker is the SUGAR amount (sat) carried alongside the token output — the
// tokens follow the marker output, so it must reach the recipient. fee is the
// total miner fee (sat). lock <= 0 disables output locking.
// Transfer 将 value 个 ticker 基本单位发送到 to。marker 是随代币输出携带的
// SUGAR 金额（聪）——代币跟随 marker 输出，因此必须到达收款人。fee 为总矿工费
// （聪）。lock <= 0 表示不锁定输出。
func (s *TokenService) Transfer(ticker, to string, value, marker, fee, lock int64, tokenAPI, externalAPI string) (SendResult, error) {
	if marker <= 0 {
		return SendResult{}, errors.New("marker must be > 0 (tokens follow the marker output) / marker 必须 > 0（代币跟随 marker 输出）")
	}
	payload, err := tokenMessage(tokenAPI, "/message/transfer", map[string]interface{}{
		"ticker": ticker, "value": value, "lock": lock,
	})
	if err != nil {
		return SendResult{}, fmt.Errorf("build transfer payload: %w / 构造转账负载：%w", err, err)
	}
	return s.buildSignBroadcastToken(payload, to, marker, fee, externalAPI)
}

// Create creates a new token `ticker` with `value` base units. The marker
// (creation cost) is resolved from /layer/params cost.create by ticker type
// and paid to the layer fee_address (web-wallet parity).
// Create 创建新代币 ticker，发行 value 个基本单位。marker（创建成本）按
// ticker 类型取自 /layer/params cost.create，付给代币层 fee_address
// （对齐 web-wallet）。
func (s *TokenService) Create(ticker string, value int64, decimals int, reissuable bool, fee int64, tokenAPI, externalAPI string) (SendResult, error) {
	params, err := s.LayerParams(tokenAPI)
	if err != nil {
		return SendResult{}, fmt.Errorf("fetch layer params: %w / 获取代币层参数：%w", err, err)
	}
	if params.FeeAddress == "" {
		return SendResult{}, errors.New("token layer params missing fee_address / 代币层参数缺少 fee_address")
	}
	marker := params.Cost.Create.CreateCost(ticker)
	payload, err := tokenMessage(tokenAPI, "/message/create", map[string]interface{}{
		"ticker": ticker, "value": value, "decimals": decimals, "reissuable": reissuable,
	})
	if err != nil {
		return SendResult{}, fmt.Errorf("build create payload: %w / 构造创建负载：%w", err, err)
	}
	return s.buildSignBroadcastToken(payload, params.FeeAddress, marker, fee, externalAPI)
}

// Issue mints `value` additional base units of `ticker`. Refuses
// non-reissuable tokens up front (web-wallet parity). The marker follows
// cost.issue to the layer fee_address.
// Issue 增发 value 个 ticker 基本单位。先拒绝不可增发的代币（对齐
// web-wallet）。marker 按 cost.issue 付给代币层 fee_address。
func (s *TokenService) Issue(ticker string, value, fee int64, tokenAPI, externalAPI string) (SendResult, error) {
	info, err := s.Info(ticker, tokenAPI)
	if err != nil {
		return SendResult{}, fmt.Errorf("fetch token info: %w / 获取代币信息：%w", err, err)
	}
	if !info.Reissuable {
		return SendResult{}, errors.New("token not reissuable / 该代币不可增发")
	}
	params, err := s.LayerParams(tokenAPI)
	if err != nil {
		return SendResult{}, fmt.Errorf("fetch layer params: %w / 获取代币层参数：%w", err, err)
	}
	if params.FeeAddress == "" {
		return SendResult{}, errors.New("token layer params missing fee_address / 代币层参数缺少 fee_address")
	}
	marker := params.Cost.Issue.IssueCost(ticker)
	payload, err := tokenMessage(tokenAPI, "/message/issue", map[string]interface{}{
		"ticker": ticker, "value": value,
	})
	if err != nil {
		return SendResult{}, fmt.Errorf("build issue payload: %w / 构造增发负载：%w", err, err)
	}
	return s.buildSignBroadcastToken(payload, params.FeeAddress, marker, fee, externalAPI)
}

// Burn burns `value` base units of `ticker`. No marker output (funds only
// cover the fee). / Burn 销毁 value 个 ticker 基本单位。无 marker 输出
// （资金仅覆盖手续费）。
func (s *TokenService) Burn(ticker string, value, fee int64, tokenAPI, externalAPI string) (SendResult, error) {
	payload, err := tokenMessage(tokenAPI, "/message/burn", map[string]interface{}{
		"ticker": ticker, "value": value,
	})
	if err != nil {
		return SendResult{}, fmt.Errorf("build burn payload: %w / 构造销毁负载：%w", err, err)
	}
	return s.buildSignBroadcastToken(payload, "", 0, fee, externalAPI)
}

// ---- Shared pipeline (web-wallet buildSignBroadcastToken, multi-key) ----
// ---- 共享流水线（web-wallet buildSignBroadcastToken，多私钥版） ----

// unlocked returns the live wallet or an error when locked.
// unlocked 返回当前解锁的钱包，锁定时报错。
func (s *TokenService) unlocked() (*wallet.Wallet, error) {
	if w := s.ws.mgr.Wallet(); w != nil {
		return w, nil
	}
	return nil, errors.New("wallet is locked / 钱包已锁定")
}

// buildSignBroadcastToken is the shared token-op pipeline: gather UTXOs →
// largest-first selection covering fee+marker → per-address key mapping →
// build (marker + OP_RETURN + change) + sign → broadcast (node first,
// external fallback). Broadcast failure keeps RawHex (retry path), matching
// the Send pipeline behavior.
// buildSignBroadcastToken 是代币操作共享流水线：汇集 UTXO → 大额优先选币
// 覆盖 fee+marker → 按地址映射私钥 → 构造（marker + OP_RETURN + 找零）并
// 签名 → 广播（先节点，后外部降级）。广播失败保留 RawHex（可重试），
// 与发送流水线行为一致。
func (s *TokenService) buildSignBroadcastToken(payloadHex, markerAddr string, markerAmount, fee int64, externalAPI string) (SendResult, error) {
	w, err := s.unlocked()
	if err != nil {
		return SendResult{}, err
	}
	if fee < 0 {
		return SendResult{}, errors.New("fee must be >= 0 / 手续费必须 >= 0")
	}
	payload, err := hex.DecodeString(payloadHex)
	if err != nil {
		return SendResult{}, fmt.Errorf("decode payload hex: %w / 解码负载十六进制：%w", err, err)
	}

	// 1) Gather UTXOs across all derived address variants.
	// 1) 汇集全部派生地址变体（三型）的 UTXO。
	utxos, nodeErr := nodeUTXOsAll(s.ws, externalAPI)
	if nodeErr != nil {
		return SendResult{}, nodeErr
	}

	// 2) Coin selection: largest first until fee+marker covered.
	// 2) 选币：金额大者优先，直到覆盖 fee+marker。
	need := fee
	if markerAddr != "" {
		need += markerAmount
	}
	sorted := append([]UTXO(nil), utxos...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Value > sorted[j].Value })
	var chosen []spendInput
	var totalIn int64
	for _, u := range sorted {
		if totalIn >= need {
			break
		}
		script, err := hex.DecodeString(u.Script)
		if err != nil {
			return SendResult{}, fmt.Errorf("decode script for %s:%d: %w", u.TxID, u.Index, err)
		}
		chosen = append(chosen, spendInput{TxID: u.TxID, Index: u.Index, Value: u.Value, Script: script, Address: u.Address})
		totalIn += u.Value
	}
	if len(chosen) == 0 || totalIn < need {
		return SendResult{}, fmt.Errorf("insufficient funds: have %d, need %d / 余额不足：有 %d，需 %d", totalIn, need, totalIn, need)
	}

	// 3) Map every involved address to its private key.
	// 3) 为每个涉及的地址取私钥。
	keys := addrKeys{}
	for _, in := range chosen {
		if _, ok := keys[in.Address]; ok {
			continue
		}
		idx, err := addressIndexFor(w, in.Address)
		if err != nil {
			return SendResult{}, err
		}
		priv, err := w.PrivateKey(idx)
		if err != nil {
			return SendResult{}, fmt.Errorf("key for %s: %w / 地址 %s 的私钥：%w", in.Address, err, in.Address, err)
		}
		keys[in.Address] = priv
	}

	// 4) Change goes to the primary (index 0, bech32) address.
	// 4) 找零回主（index 0，bech32）地址。
	changeAddr, err := w.Address(0)
	if err != nil {
		return SendResult{}, err
	}
	changeScript, err := scriptForAddress(changeAddr)
	if err != nil {
		return SendResult{}, err
	}

	// 5) Build: marker output → OP_RETURN output → change output.
	// 5) 构造：marker 输出 → OP_RETURN 输出 → 找零输出。
	mtx := wire.NewMsgTx(wire.TxVersion)
	if markerAddr != "" && markerAmount > 0 {
		mScript, err := scriptForAddress(markerAddr)
		if err != nil {
			return SendResult{}, fmt.Errorf("marker script: %w / marker 脚本：%w", err, err)
		}
		mtx.AddTxOut(wire.NewTxOut(markerAmount, mScript))
	}
	opReturnScript, err := txscript.NewScriptBuilder().
		AddOp(txscript.OP_RETURN).
		AddData(payload).
		Script()
	if err != nil {
		return SendResult{}, fmt.Errorf("build op_return script: %w / 构造 OP_RETURN 脚本：%w", err, err)
	}
	mtx.AddTxOut(wire.NewTxOut(0, opReturnScript))
	change := totalIn - need
	if change >= dustThreshold {
		mtx.AddTxOut(wire.NewTxOut(change, changeScript))
	} else {
		// Donate sub-dust change to miners. / 低于粉尘阈值的找零捐赠给矿工。
		change = 0
	}

	// 6) Add inputs + sign each with its address key (reuses gosend.go).
	// 6) 添加输入并逐个用所属地址私钥签名（复用 gosend.go）。
	for _, in := range chosen {
		hash, err := chainhash.NewHashFromStr(in.TxID)
		if err != nil {
			return SendResult{}, fmt.Errorf("parse txid %q: %w", in.TxID, err)
		}
		mtx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(hash, in.Index), nil, nil))
	}
	fetcher := newPrevOutFetcher(chosen)
	sigHashes := txscript.NewTxSigHashes(mtx, fetcher)
	for i, in := range chosen {
		priv, ok := keys[in.Address]
		if !ok {
			return SendResult{}, fmt.Errorf("no key for address %s / 地址 %s 没有对应私钥", in.Address, in.Address)
		}
		if err := signInput(mtx, sigHashes, i, in, priv, s.netParams()); err != nil {
			return SendResult{}, fmt.Errorf("sign input %d: %w / 签名输入 %d：%w", i, err, i, err)
		}
	}

	var buf bytes.Buffer
	if err := mtx.Serialize(&buf); err != nil {
		return SendResult{}, fmt.Errorf("serialize tx: %w / 序列化交易：%w", err, err)
	}
	res := &SendResult{
		RawHex:     hex.EncodeToString(buf.Bytes()),
		TotalIn:    totalIn,
		Amount:     markerAmount,
		Fee:        fee,
		Change:     change,
		InputCount: len(chosen),
	}

	// 7) Broadcast: node first, external fallback; failure keeps RawHex.
	// 7) 广播：先节点，后外部降级；失败保留 RawHex。
	txid, bErr := nodeBroadcast(res.RawHex)
	if bErr != nil && strings.TrimSpace(externalAPI) != "" {
		txid, bErr = externalBroadcast(externalAPI, res.RawHex)
	}
	if bErr != nil {
		res.BroadcastErr = bErr.Error()
		return *res, nil
	}
	res.TxID = txid
	return *res, nil
}

// netParams returns the chain params shared with WalletService.
// netParams 返回与 WalletService 一致的网络参数。
func (s *TokenService) netParams() *chaincfg.Params {
	return sugarParams
}
