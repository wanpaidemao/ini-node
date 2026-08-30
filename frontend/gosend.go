package main

// SendService implements the Step 8 send pipeline (dev_doc/前端方案/
// ini-node前端内置HD钱包方案-20260830.md §5.11) INSIDE the frontend (Wails)
// process, aligned with web-wallet's SendTransactionMulti.
//
// Pipeline:  UTXO query (node getaddressutxos → external REST /unspent)
//            → coin selection (UTXOs cover sum(outputs) + fee)
//            → build + sign (per-address private keys, P2WPKH/P2PKH/P2SH-P2WPKH)
//            → broadcast (node sendrawtransaction → external REST /broadcast)
//
// Broadcast failures do NOT fail Send: the raw hex is returned so the UI can
// retry via BroadcastRaw (web-wallet behavior).
//
// SendService 在前端（Wails）进程内实现第 8 步发送链路（方案 §5.11），
// 对齐 web-wallet 的 SendTransactionMulti。
// 流水线：UTXO 查询（节点 getaddressutxos → 外部 REST /unspent 降级）
//   → 选币（UTXO 覆盖 输出合计+手续费）
//   → 构造+签名（按地址各自的私钥，支持 P2WPKH/P2PKH/P2SH-P2WPKH）
//   → 广播（节点 sendrawtransaction → 外部 REST /broadcast 降级）
// 广播失败不使 Send 失败：返回裸十六进制，前端可用 BroadcastRaw 重试
// （与 web-wallet 行为一致）。

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/btcsuite/btcd/address/v2"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/txscript/v2"
	"github.com/btcsuite/btcd/wire/v2"
	"github.com/btcsuite/btcd/wallet"
)

// dustThreshold mirrors web-wallet tx.DustThreshold (546 sat, Bitcoin Core
// P2PKH relay dust): change below it is donated to miners.
// dustThreshold 对齐 web-wallet tx.DustThreshold（546 聪，Bitcoin Core P2PKH
// 中继粉尘阈值）：低于该值的找零捐赠给矿工。
const dustThreshold = 546

// SendService is the Wails-bound send pipeline. It shares the WalletService
// session (same unlock/lock lifecycle) and is stateless otherwise.
// SendService 是 Wails 绑定的发送服务。与 WalletService 共享同一会话
// （同一解锁/锁定生命周期），其余无状态。
type SendService struct {
	ws *WalletService
}

// newSendService builds the service around the shared wallet session.
// newSendService 基于共享钱包会话构建服务。
func newSendService(ws *WalletService) *SendService {
	return &SendService{ws: ws}
}

// SendOutput is one payment destination of a (possibly multi-output) send.
// Mirrors web-wallet tx.Output.
// SendOutput 是（可能是多输出的）发送的一个收款方。对齐 web-wallet tx.Output。
type SendOutput struct {
	Address string `json:"address"` // recipient address / 收款地址
	Amount  int64  `json:"amount"`  // satoshis / 聪
}

// UTXO is one spendable output of the wallet (node or external source).
// Mirrors web-wallet api.UTXO (txid/index/value/script) plus the owning
// address so multi-address HD wallets can match keys.
// UTXO 是钱包的一个可花费输出（节点或外部数据源）。对齐 web-wallet
// api.UTXO（txid/index/value/script），另附所属地址，多地址 HD 钱包据此匹配私钥。
type UTXO struct {
	TxID    string `json:"txid"`    // transaction id / 交易 id
	Index   uint32 `json:"index"`   // vout index / 输出索引
	Value   int64  `json:"value"`   // satoshis / 聪
	Script  string `json:"script"`  // hex scriptPubKey / 十六进制脚本
	Address string `json:"address"` // owning address / 所属地址
}

// SendResult is the outcome of Send. Broadcast failure is reported in
// BroadcastErr with RawHex still set (retry path), not as a Go error.
// SendResult 是 Send 的结果。广播失败记录在 BroadcastErr 且 RawHex 仍有值
// （可走重试路径），不以 Go error 形式返回。
type SendResult struct {
	TxID         string `json:"txid"`         // broadcast txid ("" when broadcast failed) / 广播后的交易 id（广播失败时为空）
	RawHex       string `json:"rawHex"`       // signed tx hex (always set on success) / 已签名交易十六进制（成功时必有）
	TotalIn      int64  `json:"totalIn"`      // sum of inputs (sat) / 输入合计（聪）
	Amount       int64  `json:"amount"`       // sum of outputs (sat) / 输出合计（聪）
	Fee          int64  `json:"fee"`          // total miner fee (sat) / 总矿工费（聪）
	Change       int64  `json:"change"`       // change returned (0 when donated) / 找零（捐赠时为 0）
	InputCount   int    `json:"inputCount"`   // number of inputs / 输入数量
	BroadcastErr string `json:"broadcastErr"` // broadcast error, "" on success / 广播错误，成功时为空
}

// netParams returns the chain params shared with WalletService.
// netParams 返回与 WalletService 一致的网络参数。
func (s *SendService) netParams() *chaincfg.Params {
	return &chaincfg.SugarMainNetParams
}

// unlocked returns the live wallet or an error when locked.
// unlocked 返回当前解锁的钱包，锁定时报错。
func (s *SendService) unlocked() (*wallet.Wallet, error) {
	if w := s.ws.mgr.Wallet(); w != nil {
		return w, nil
	}
	return nil, errors.New("wallet is locked / 钱包已锁定")
}

// ---- UTXO sources (two-level chain) ---- / ---- UTXO 数据源（两级链） ----

// nodeRPC performs one JSON-RPC call against the local node using the ini
// credentials (same resolution as the /rpc proxy). / nodeRPC 使用 ini 凭据
// （与 /rpc 代理相同的解析）对本地节点执行一次 JSON-RPC 调用。
func nodeRPC(method string, params []interface{}) (json.RawMessage, error) {
	opts := map[string]string{}
	if p := findIniPath(); p != "" {
		opts = parseIni(p)
	}
	host := strings.TrimSpace(opts["rpclisten"])
	if host == "" {
		host = "127.0.0.1:8334"
	}
	if !strings.Contains(host, ":") {
		host = "127.0.0.1:" + host
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "1.0", "id": "send", "method": method, "params": params,
	})
	req, err := http.NewRequest(http.MethodPost, "http://"+host+"/", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(strings.TrimSpace(opts["rpcuser"]), strings.TrimSpace(opts["rpcpass"]))
	resp, err := rpcHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  interface{}     `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("RPC %s: %v", method, out.Error)
	}
	return out.Result, nil
}

// nodeUTXOs queries the node's sugarindex getaddressutxos for the given
// addresses. Works with the wallet unlocked in the FRONTEND process only
// (unlike listunspent, it does not need the node-side wallet).
// nodeUTXOs 按地址查询节点 sugarindex 的 getaddressutxos。仅需前端进程内
// 解锁钱包（不同于 listunspent，无需节点侧钱包解锁）。
func nodeUTXOs(addrs []string) ([]UTXO, error) {
	raw, err := nodeRPC("getaddressutxos", []interface{}{map[string]interface{}{
		"addresses": addrs,
	}})
	if err != nil {
		return nil, err
	}
	var res []struct {
		Address     string `json:"address"`
		Txid        string `json:"txid"`
		OutputIndex uint32 `json:"outputIndex"`
		Script      string `json:"script"`
		Satoshis    int64  `json:"satoshis"`
		Height      int32  `json:"height"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	out := make([]UTXO, 0, len(res))
	for _, u := range res {
		out = append(out, UTXO{TxID: u.Txid, Index: u.OutputIndex, Value: u.Satoshis, Script: u.Script, Address: u.Address})
	}
	return out, nil
}

// restClient is the HTTP client for the external REST data source.
// restClient 是外部 REST 数据源的 HTTP 客户端。
var restClient = &http.Client{Timeout: 15 * time.Second}

// restGet performs GET {api}{path} and decodes the JSON envelope
// {result, error} into out. / restGet 执行 GET {api}{path} 并把
// {result, error} 信封解码到 out。
func restGet(api, path string, out interface{}) error {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(api, "/")+path, nil)
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

// truncate shortens s for error messages. / truncate 为错误信息截短字符串。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// externalUTXOs queries the external REST /unspent/{addr} for each address
// (web-wallet api.SugarClient.Unspent protocol).
// externalUTXOs 按地址查询外部 REST /unspent/{addr}（web-wallet
// api.SugarClient.Unspent 协议）。
func externalUTXOs(api string, addrs []string) ([]UTXO, error) {
	var out []UTXO
	for _, a := range addrs {
		var resp struct {
			Result []struct {
				TxID   string `json:"txid"`
				Index  uint32 `json:"index"`
				Value  int64  `json:"value"`
				Script string `json:"script"`
			} `json:"result"`
			Error json.RawMessage `json:"error"`
		}
		if err := restGet(api, "/unspent/"+a, &resp); err != nil {
			return nil, fmt.Errorf("external /unspent: %w", err)
		}
		for _, u := range resp.Result {
			out = append(out, UTXO{TxID: u.TxID, Index: u.Index, Value: u.Value, Script: u.Script, Address: a})
		}
	}
	return out, nil
}

// UTXOs returns the wallet's spendable outputs. Two-level chain: local node
// getaddressutxos first, external REST fallback second (when externalAPI is
// non-empty). Each UTXO carries its owning address so signing can pick the
// right key per input. Since Step 9 the scan covers ALL THREE address types
// per derivation index (old-type funds stay spendable after a type switch).
// UTXOs 返回钱包的可花费输出。两级链：先本地节点 getaddressutxos，再外部
// REST 降级（externalAPI 非空时）。每个 UTXO 附带所属地址，签名时按输入
// 匹配对应私钥。第 9 步起扫描覆盖每个派生索引的全部三种地址类型
// （类型切换后旧类型资金仍可花费）。
func (s *SendService) UTXOs(externalAPI string) ([]UTXO, error) {
	w, err := s.unlocked()
	if err != nil {
		return nil, err
	}
	return gatherUTXOs(w, strings.TrimSpace(externalAPI))
}

// EstimateFee returns the fee suggestion from the external REST /fee
// endpoint (web-wallet api.SugarClient.Fee protocol; satoshis as float).
// The local node has no fee-estimate RPC, so there is no local fallback.
// EstimateFee 从外部 REST /fee 端点获取手续费建议（web-wallet
// api.SugarClient.Fee 协议；聪，浮点）。本地节点无费率估算 RPC，故无本地降级。
func (s *SendService) EstimateFee(externalAPI string) (float64, error) {
	if strings.TrimSpace(externalAPI) == "" {
		return 0, errors.New("no external API configured / 未配置外部 API")
	}
	var resp struct {
		Result float64         `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := restGet(externalAPI, "/fee", &resp); err != nil {
		return 0, fmt.Errorf("external /fee: %w", err)
	}
	return resp.Result, nil
}

// ---- Build + sign (ported from web-wallet internal/tx, multi-key) ----
// ---- 构造+签名（移植自 web-wallet internal/tx，扩展多私钥） ----

// spendInput is a tx.Input plus the address it pays to (for key lookup).
// spendInput 是 tx.Input 加上其支付的地址（用于查找对应私钥）。
type spendInput struct {
	TxID    string
	Index   uint32
	Value   int64
	Script  []byte // raw scriptPubKey / 原始脚本公钥
	Address string // owning wallet address / 所属钱包地址
}

// addrKeys maps each wallet address to its private key (one entry per
// derived address that actually has inputs).
// addrKeys 把每个钱包地址映射到其私钥（仅为真正有输入的地址建立条目）。
type addrKeys map[string]*btcec.PrivateKey

// buildAndSign is BuildAndSignMulti ported from web-wallet internal/tx with
// one change: every input is signed with the private key of ITS address
// (HD wallets spread funds across derived addresses). Change goes back to
// the primary (index 0) address, dust change is donated.
// buildAndSign 移植自 web-wallet internal/tx 的 BuildAndSignMulti，改动一处：
// 每个输入用它所属地址的私钥签名（HD 钱包资金分散在多个派生地址）。
// 找零回到主（index 0）地址，低于粉尘阈值的找零捐赠给矿工。
func (s *SendService) buildAndSign(
	net *chaincfg.Params,
	keys addrKeys,
	inputs []spendInput,
	outputs []SendOutput,
	fee int64,
	changeAddr string,
) (*SendResult, error) {
	if len(outputs) == 0 {
		return nil, errors.New("no outputs / 没有输出")
	}
	if fee < 0 {
		return nil, errors.New("fee must be >= 0 / 手续费必须 >= 0")
	}
	if len(inputs) == 0 {
		return nil, errors.New("no inputs / 没有输入")
	}

	// Parse every destination up front. / 预先解析全部收款地址。
	type parsedOutput struct {
		script []byte
		amount int64
	}
	parsed := make([]parsedOutput, 0, len(outputs))
	var totalOut int64
	for _, o := range outputs {
		if o.Amount <= 0 {
			return nil, errors.New("output amount must be > 0 / 输出金额必须 > 0")
		}
		dest, err := address.DecodeAddress(o.Address, net)
		if err != nil {
			return nil, fmt.Errorf("decode destination %q: %w", o.Address, err)
		}
		script, err := txscript.PayToAddrScript(dest)
		if err != nil {
			return nil, fmt.Errorf("build destination script for %q: %w", o.Address, err)
		}
		parsed = append(parsed, parsedOutput{script, o.Amount})
		totalOut += o.Amount
	}

	// Parse change address. / 解析找零地址。
	chAddr, err := address.DecodeAddress(changeAddr, net)
	if err != nil {
		return nil, fmt.Errorf("decode change address: %w", err)
	}
	chScript, err := txscript.PayToAddrScript(chAddr)
	if err != nil {
		return nil, fmt.Errorf("build change script: %w", err)
	}

	// Tally inputs. / 累加输入。
	var totalIn int64
	for _, in := range inputs {
		totalIn += in.Value
	}
	need := totalOut + fee
	if totalIn < need {
		return nil, fmt.Errorf("insufficient funds: have %d, need %d / 余额不足：有 %d，需 %d", totalIn, need, totalIn, need)
	}

	// Build tx: payments first, change last. / 构造交易：付款在前，找零殿后。
	mtx := wire.NewMsgTx(wire.TxVersion)
	for _, p := range parsed {
		mtx.AddTxOut(wire.NewTxOut(p.amount, p.script))
	}
	change := totalIn - need
	if change >= dustThreshold {
		mtx.AddTxOut(wire.NewTxOut(change, chScript))
	} else {
		// Donate sub-dust change to miners. / 低于粉尘阈值的找零捐赠给矿工。
		change = 0
	}

	// Add inputs (unsigned). / 添加输入（未签名）。
	for _, in := range inputs {
		hash, err := chainhash.NewHashFromStr(in.TxID)
		if err != nil {
			return nil, fmt.Errorf("parse txid %q: %w", in.TxID, err)
		}
		mtx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(hash, in.Index), nil, nil))
	}

	// Sign each input with the key of its address. / 用各地址私钥逐输入签名。
	fetcher := newPrevOutFetcher(inputs)
	sigHashes := txscript.NewTxSigHashes(mtx, fetcher)
	for i, in := range inputs {
		priv, ok := keys[in.Address]
		if !ok {
			return nil, fmt.Errorf("no key for address %s / 地址 %s 没有对应私钥", in.Address, in.Address)
		}
		if err := signInput(mtx, sigHashes, i, in, priv, net); err != nil {
			return nil, fmt.Errorf("sign input %d: %w", i, err)
		}
	}

	var buf bytes.Buffer
	if err := mtx.Serialize(&buf); err != nil {
		return nil, fmt.Errorf("serialize tx: %w", err)
	}
	return &SendResult{
		RawHex:     hex.EncodeToString(buf.Bytes()),
		TotalIn:    totalIn,
		Amount:     totalOut,
		Fee:        fee,
		Change:     change,
		InputCount: len(inputs),
	}, nil
}

// signInput signs one input by scriptPubKey class. Ported from web-wallet
// internal/tx signInput (P2PKH / P2WPKH / P2SH-P2WPKH + heuristic fallback).
// signInput 按脚本类型签名单个输入。移植自 web-wallet internal/tx 的
// signInput（P2PKH / P2WPKH / P2SH-P2WPKH + 启发式回退）。
func signInput(mtx *wire.MsgTx, sigHashes *txscript.TxSigHashes, idx int, in spendInput, priv *btcec.PrivateKey, net *chaincfg.Params) error {
	pubKeyCompressed := priv.PubKey().SerializeCompressed()
	pubKeyHash := address.Hash160(pubKeyCompressed)

	s := in.Script
	// Manual classification (same as web-wallet signByHeuristic; the standard
	// classifiers can be picky about custom net params).
	// 手动判定（同 web-wallet signByHeuristic；标准分类器对自定义网络参数较苛刻）。
	switch {
	case len(s) == 22 && s[0] == 0x00 && s[1] == 0x14:
		return signP2WPKH(mtx, sigHashes, idx, in, priv, pubKeyCompressed, pubKeyHash)
	case len(s) == 23 && s[0] == 0xa9 && s[1] == 0x14 && s[22] == 0x87:
		return signP2SHP2WPKH(mtx, sigHashes, idx, in, priv, pubKeyCompressed, pubKeyHash)
	case len(s) == 25 && s[0] == 0x76 && s[1] == 0xa9 && s[2] == 0x14 && s[23] == 0x88 && s[24] == 0xac:
		return signP2PKH(mtx, sigHashes, idx, in, priv, pubKeyCompressed)
	default:
		return fmt.Errorf("unsupported scriptPubKey (len=%d) / 不支持的脚本类型（长度=%d）", len(s), len(s))
	}
}

// signP2PKH signs a legacy P2PKH input. / signP2PKH 签名 legacy P2PKH 输入。
func signP2PKH(mtx *wire.MsgTx, _ *txscript.TxSigHashes, idx int, in spendInput, priv *btcec.PrivateKey, pubKeyCompressed []byte) error {
	sigHash, err := txscript.CalcSignatureHash(in.Script, txscript.SigHashAll, mtx, idx)
	if err != nil {
		return fmt.Errorf("legacy sighash: %w", err)
	}
	sig := signDigest(priv, sigHash)
	scriptSig, err := txscript.NewScriptBuilder().
		AddData(append(sig, byte(txscript.SigHashAll))).
		AddData(pubKeyCompressed).
		Script()
	if err != nil {
		return fmt.Errorf("build scriptSig: %w", err)
	}
	mtx.TxIn[idx].SignatureScript = scriptSig
	mtx.TxIn[idx].Witness = nil
	return nil
}

// signP2WPKH signs a native bech32 P2WPKH input via the witness stack.
// signP2WPKH 通过 witness 签名原生 bech32 P2WPKH 输入。
func signP2WPKH(mtx *wire.MsgTx, sigHashes *txscript.TxSigHashes, idx int, in spendInput, priv *btcec.PrivateKey, pubKeyCompressed, pubKeyHash []byte) error {
	pkScript := buildP2WPKHScript(pubKeyHash)
	sigHash, err := txscript.CalcWitnessSigHash(pkScript, sigHashes, txscript.SigHashAll, mtx, idx, in.Value)
	if err != nil {
		return fmt.Errorf("witness v0 sighash: %w", err)
	}
	sig := signDigest(priv, sigHash)
	mtx.TxIn[idx].Witness = wire.TxWitness{
		append(sig, byte(txscript.SigHashAll)),
		pubKeyCompressed,
	}
	mtx.TxIn[idx].SignatureScript = nil
	return nil
}

// signP2SHP2WPKH signs a nested segwit input: scriptSig pushes the redeem
// script, witness mirrors P2WPKH. / signP2SHP2WPKH 签名嵌套隔离见证输入：
// scriptSig 推入赎回脚本，witness 同 P2WPKH。
func signP2SHP2WPKH(mtx *wire.MsgTx, sigHashes *txscript.TxSigHashes, idx int, in spendInput, priv *btcec.PrivateKey, pubKeyCompressed, pubKeyHash []byte) error {
	redeem := buildP2WPKHScript(pubKeyHash)
	sigHash, err := txscript.CalcWitnessSigHash(redeem, sigHashes, txscript.SigHashAll, mtx, idx, in.Value)
	if err != nil {
		return fmt.Errorf("nested segwit sighash: %w", err)
	}
	sig := signDigest(priv, sigHash)
	scriptSig, err := txscript.NewScriptBuilder().AddData(redeem).Script()
	if err != nil {
		return fmt.Errorf("build p2sh scriptSig: %w", err)
	}
	mtx.TxIn[idx].SignatureScript = scriptSig
	mtx.TxIn[idx].Witness = wire.TxWitness{
		append(sig, byte(txscript.SigHashAll)),
		pubKeyCompressed,
	}
	return nil
}

// buildP2WPKHScript returns 0x00 || 0x14 || <20-byte pubkey hash>.
// buildP2WPKHScript 返回 0x00 || 0x14 || <20 字节公钥哈希>。
func buildP2WPKHScript(pubKeyHash []byte) []byte {
	out := make([]byte, 0, 22)
	out = append(out, 0x00, 0x14)
	return append(out, pubKeyHash...)
}

// signDigest signs a 32-byte digest with RFC6979 deterministic nonce.
// signDigest 用 RFC6979 确定性 nonce 对 32 字节摘要签名。
func signDigest(priv *btcec.PrivateKey, digest []byte) []byte {
	sig := ecdsa.Sign(priv, digest)
	return sig.Serialize() // DER (r,s)
}

// prevOutFetcher implements txscript.PrevOutputFetcher over spendInputs.
// prevOutFetcher 基于 spendInput 实现 txscript.PrevOutputFetcher。
type prevOutFetcher struct {
	outs map[wire.OutPoint]*wire.TxOut
}

// FetchPrevOutput returns the referenced previous output or nil.
// FetchPrevOutput 返回被引用的前置输出，找不到为 nil。
func (f *prevOutFetcher) FetchPrevOutput(op wire.OutPoint) *wire.TxOut {
	return f.outs[op]
}

// newPrevOutFetcher builds a fetcher keyed by OutPoint.
// newPrevOutFetcher 构建以 OutPoint 为键的 fetcher。
func newPrevOutFetcher(inputs []spendInput) *prevOutFetcher {
	outs := make(map[wire.OutPoint]*wire.TxOut, len(inputs))
	for _, in := range inputs {
		hash, err := chainhash.NewHashFromStr(in.TxID)
		if err != nil {
			continue // signer fails later with a clearer error / 签名时会给出更清晰错误
		}
		outs[*wire.NewOutPoint(hash, in.Index)] = wire.NewTxOut(in.Value, in.Script)
	}
	return &prevOutFetcher{outs: outs}
}

// ---- Broadcast (two-level chain) ---- / ---- 广播（两级链） ----

// nodeBroadcast pushes the raw tx via the node's sendrawtransaction RPC.
// nodeBroadcast 通过节点 sendrawtransaction RPC 广播裸交易。
func nodeBroadcast(rawHex string) (string, error) {
	raw, err := nodeRPC("sendrawtransaction", []interface{}{rawHex})
	if err != nil {
		return "", err
	}
	var txid string
	if err := json.Unmarshal(raw, &txid); err != nil {
		return "", err
	}
	return txid, nil
}

// externalBroadcast pushes the raw tx via external REST /broadcast
// (form-encoded POST raw=<hex>, web-wallet api.SugarClient.Broadcast).
// externalBroadcast 通过外部 REST /broadcast 广播（表单编码 POST
// raw=<hex>，对齐 web-wallet api.SugarClient.Broadcast）。
func externalBroadcast(api, rawHex string) (string, error) {
	form := url.Values{}
	form.Set("raw", rawHex)
	resp, err := restClient.Post(strings.TrimRight(api, "/")+"/broadcast",
		"application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var out struct {
		Result string          `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		// Backend may answer plain text on success. / 后端成功时可能返回纯文本。
		return strings.TrimSpace(string(body)), nil
	}
	if len(out.Error) > 0 && string(out.Error) != "null" {
		return "", fmt.Errorf("%s", truncate(string(out.Error), 200))
	}
	if out.Result != "" {
		return out.Result, nil
	}
	return strings.TrimSpace(string(body)), nil
}

// BroadcastRaw retries a previously signed raw tx (the Send result card's
// retry button). Node first, external fallback. / BroadcastRaw 重试先前已签名
// 的裸交易（Send 结果卡的重试按钮）。先节点，后外部降级。
func (s *SendService) BroadcastRaw(rawHex, externalAPI string) (string, error) {
	txid, nodeErr := nodeBroadcast(rawHex)
	if nodeErr == nil {
		return txid, nil
	}
	if strings.TrimSpace(externalAPI) == "" {
		return "", nodeErr
	}
	txid, extErr := externalBroadcast(externalAPI, rawHex)
	if extErr != nil {
		return "", fmt.Errorf("node: %v; external: %v / 节点：%v；外部：%v", nodeErr, extErr, nodeErr, extErr)
	}
	return txid, nil
}

// ---- Send pipeline ---- / ---- 发送流水线 ----

// Send runs the full pipeline for one (possibly multi-output) payment.
// Outputs max 10 (web-wallet limit). fee is the TOTAL miner fee in satoshis.
// Coin selection: largest-first until total+fee is covered, minimum inputs.
// Broadcast failure is reported in SendResult.BroadcastErr (RawHex kept).
// Send 执行一笔（可能多输出的）支付完整流水线。输出最多 10 个（对齐
// web-wallet）。fee 为总矿工费（聪）。选币：按金额从大到小直到覆盖
// 合计+手续费，输入数最少。广播失败记录在 SendResult.BroadcastErr
// （RawHex 保留）。
func (s *SendService) Send(outputs []SendOutput, fee int64, externalAPI string) (SendResult, error) {
	if len(outputs) == 0 {
		return SendResult{}, errors.New("no outputs / 没有输出")
	}
	if len(outputs) > 10 {
		return SendResult{}, errors.New("too many outputs (max 10) / 输出过多（上限 10）")
	}
	w, err := s.unlocked()
	if err != nil {
		return SendResult{}, err
	}

	// 1) Gather UTXOs across all derived addresses. / 1) 汇集全部派生地址的 UTXO。
	utxos, err := s.UTXOs(externalAPI)
	if err != nil {
		return SendResult{}, err
	}

	// 2) Coin selection: largest first. / 2) 选币：金额大者优先。
	sorted := append([]UTXO(nil), utxos...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Value > sorted[j].Value })
	var totalOut int64
	for _, o := range outputs {
		totalOut += o.Amount
	}
	need := totalOut + fee
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
	if totalIn < need {
		return SendResult{}, fmt.Errorf("insufficient funds: have %d, need %d / 余额不足：有 %d，需 %d", totalIn, need, totalIn, need)
	}

	// 3) Map every involved address to its private key. / 3) 为每个涉及的地址取私钥。
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
			return SendResult{}, fmt.Errorf("key for %s: %w", in.Address, err)
		}
		keys[in.Address] = priv
	}

	// 4) Change goes to the primary (index 0) address. / 4) 找零回主（index 0）地址。
	changeAddr, err := w.Address(0)
	if err != nil {
		return SendResult{}, err
	}

	// 5) Build + sign. / 5) 构造 + 签名。
	res, err := s.buildAndSign(s.netParams(), keys, chosen, outputs, fee, changeAddr)
	if err != nil {
		return SendResult{}, err
	}

	// 6) Broadcast; failure keeps RawHex for retry. / 6) 广播；失败保留 RawHex 供重试。
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

// sugarFloatToSat converts a coin-unit float (RPC listunspent style) to
// satoshis. Kept for callers that receive float amounts.
// sugarFloatToSat 把币单位浮点（RPC listunspent 风格）换算为聪。
// 供收到浮点金额的调用方使用。
func sugarFloatToSat(v float64) int64 {
	return int64(math.Round(v * 1e8))
}
