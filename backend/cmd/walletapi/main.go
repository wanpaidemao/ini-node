// walletapi is a thin REST gateway that exposes the btcd sugarindex RPC
// methods used by the web-wallet (and any REST client) as simple HTTP
// endpoints.  It forwards each request to the btcd JSON-RPC endpoint with
// Basic auth and shapes the response exactly like the original web-wallet
// REST backend: {"result": ..., "error": ...}, including the RPC -5 error
// code for addresses with no on-chain history.
//
// Endpoints:
//
//	GET  /balance/{address}            -> getaddressbalance
//	GET  /unspent/{address}?amount=N   -> getaddressutxos
//	GET  /fee                          -> estimatefee
//	GET  /transaction/{hash}           -> getrawtransaction
//	POST /broadcast  (form: raw=<hex>) -> sendrawtransaction
//
// This gateway is optional: web-wallet can also connect directly via
// rpcclient (config.Backend == "rpc").  The gateway exists so the REST
// backend can be pointed at a local btcd node without changing the wallet.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// rpcResponse mirrors the JSON-RPC response shape from btcd.
type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// rpcError mirrors an RPC error object (code + message).
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// restError is the error shape returned to REST clients.
type restError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// restReply is the response shape for every endpoint.
type restReply struct {
	Result interface{} `json:"result,omitempty"`
	Error  *restError  `json:"error,omitempty"`
}

// opts holds the gateway configuration.
type opts struct {
	listen   string
	rpcHost  string
	rpcUser  string
	rpcPass  string
}

// client is a minimal JSON-RPC client for the btcd node.
type client struct {
	url  string
	user string
	pass string
	http *http.Client
}

// call issues a JSON-RPC request and returns the raw result bytes.
func (c *client) call(method string, params []interface{}) (json.RawMessage, *rpcError) {
	payload, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "1.0",
		"id":      "walletapi",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, &rpcError{Code: -32700, Message: err.Error()}
	}

	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.user, c.pass)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &rpcError{Code: -32000, Message: fmt.Sprintf("http %d: %s", resp.StatusCode, string(body))}
	}

	var rr rpcResponse
	if err := json.Unmarshal(body, &rr); err != nil {
		return nil, &rpcError{Code: -32700, Message: err.Error()}
	}
	if rr.Error != nil {
		return nil, rr.Error
	}
	return rr.Result, nil
}

// handlers ----------------------------------------------------------------

// balance maps getaddressbalance -> {confirmed, unconfirmed}.
func (c *client) handleBalance(w http.ResponseWriter, r *http.Request, addr string) {
	raw, rpcErr := c.call("getaddressbalance", []interface{}{addr})
	if rpcErr != nil {
		writeReply(w, nil, rpcErr)
		return
	}
	var res struct {
		Balance          int64 `json:"balance"`
		BalanceImmature  int64 `json:"balance_immature"`
		BalanceSpendable int64 `json:"balance_spendable"`
		Received         int64 `json:"received"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		writeReply(w, nil, &rpcError{Code: -32603, Message: err.Error()})
		return
	}
	writeReply(w, map[string]int64{
		"balance":     res.Balance,
		"unconfirmed": res.BalanceImmature,
	}, nil)
}

// history maps getaddressdeltas -> {items,total,offset} with pagination.
// history 转发 getaddressdeltas -> {items,total,offset}(支持分页)。
func (c *client) handleHistory(w http.ResponseWriter, r *http.Request, addr string) {
	raw, rpcErr := c.call("getaddressdeltas", []interface{}{
		map[string]interface{}{"addresses": []string{addr}},
	})
	if rpcErr != nil {
		// New address with no history -> empty set.
		if rpcErr.Code == -5 {
			writeReply(w, []interface{}{}, nil)
			return
		}
		writeReply(w, nil, rpcErr)
		return
	}
	var results []struct {
		Txid     string `json:"txid"`
		Height   int32  `json:"height"`
		Satoshis int64  `json:"satoshis"`
		Index    int32  `json:"index"`
	}
	if err := json.Unmarshal(raw, &results); err != nil {
		writeReply(w, nil, &rpcError{Code: -32603, Message: err.Error()})
		return
	}
	// Aggregate deltas by txid: one tx with several in/out for the same
	// address yields several deltas; show one row per tx (amount = net sum,
	// height = min). / 按 txid 聚合:同一地址在一笔交易的多笔输入/输出会生成
	// 多条 delta;每个 txid 只显示一行(金额=净和,高度=最小)。
	type aggT struct {
		Txid     string
		Height   int32
		Satoshis int64
	}
	agg := map[string]aggT{}
	for _, h := range results {
		a := agg[h.Txid]
		a.Txid = h.Txid
		if a.Height == 0 || h.Height < a.Height {
			a.Height = h.Height
		}
		a.Satoshis += h.Satoshis
		agg[h.Txid] = a
	}
	aggr := make([]aggT, 0, len(agg))
	for _, a := range agg {
		aggr = append(aggr, a)
	}
	// Go map iteration order is random: sort by height for a stable page
	// order (ascending = chain order, aligns with api-server/ElectrumX).
	// Go map 遍历顺序随机:按高度排序保证分页顺序稳定(升序=链上顺序)。
	sort.Slice(aggr, func(i, j int) bool {
		return aggr[i].Height < aggr[j].Height
	})
	items := make([]interface{}, 0, len(aggr))
	for _, h := range aggr {
		items = append(items, map[string]interface{}{
			"txid": h.Txid, "height": h.Height, "satoshis": h.Satoshis,
		})
	}
	writeReply(w, items, nil)
}

// unspent maps getaddressutxos -> [{txid,index,value,script}].
func (c *client) handleUnspent(w http.ResponseWriter, r *http.Request, addr string) {
	amount, _ := strconv.ParseInt(r.URL.Query().Get("amount"), 10, 64)

	// getaddressutxos expects an object {"addresses":[...]}, not a bare array.
	// getaddressutxos 参数是对象 {"addresses":[...]},不是裸数组。
	raw, rpcErr := c.call("getaddressutxos", []interface{}{
		map[string]interface{}{"addresses": []string{addr}},
	})
	if rpcErr != nil {
		// New address with no history -> empty set, like the original backend.
		if rpcErr.Code == -5 {
			writeReply(w, []interface{}{}, nil)
			return
		}
		writeReply(w, nil, rpcErr)
		return
	}
	var results []struct {
		Txid        string `json:"txid"`
		OutputIndex uint32 `json:"outputIndex"`
		Script      string `json:"script"`
		Satoshis    int64  `json:"satoshis"`
	}
	if err := json.Unmarshal(raw, &results); err != nil {
		writeReply(w, nil, &rpcError{Code: -32603, Message: err.Error()})
		return
	}
	utxos := make([]interface{}, 0, len(results))
	var total int64
	for _, u := range results {
		utxos = append(utxos, map[string]interface{}{
			"txid":   u.Txid,
			"index":  u.OutputIndex,
			"value":  u.Satoshis,
			"script": u.Script,
		})
		total += u.Satoshis
		if amount > 0 && total >= amount {
			break
		}
	}
	writeReply(w, utxos, nil)
}

// fee maps estimatefee (sat/kB) -> sat/byte.
func (c *client) handleFee(w http.ResponseWriter, r *http.Request) {
	raw, rpcErr := c.call("estimatefee", []interface{}{2})
	if rpcErr != nil {
		writeReply(w, nil, rpcErr)
		return
	}
	var fee float64
	if err := json.Unmarshal(raw, &fee); err != nil {
		writeReply(w, nil, &rpcError{Code: -32603, Message: err.Error()})
		return
	}
	if fee < 0 {
		fee = 0
	}
	writeReply(w, fee/1000, nil)
}

// transaction maps getrawtransaction (verbose) -> raw tx JSON.
func (c *client) handleTransaction(w http.ResponseWriter, r *http.Request, hash string) {
	raw, rpcErr := c.call("getrawtransaction", []interface{}{hash, 1})
	if rpcErr != nil {
		writeReply(w, nil, rpcErr)
		return
	}
	// Return the raw transaction object as-is.
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		writeReply(w, string(raw), nil)
		return
	}
	writeReply(w, v, nil)
}

// broadcast maps sendrawtransaction (form raw=<hex>) -> txid.
func (c *client) handleBroadcast(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeReply(w, nil, &rpcError{Code: -32600, Message: err.Error()})
		return
	}
	rawHex := r.FormValue("raw")
	if strings.TrimSpace(rawHex) == "" {
		writeReply(w, nil, &rpcError{Code: -32600, Message: "missing raw parameter"})
		return
	}
	res, rpcErr := c.call("sendrawtransaction", []interface{}{rawHex, true})
	if rpcErr != nil {
		writeReply(w, nil, rpcErr)
		return
	}
	writeReply(w, string(res), nil)
}

// writeReply serializes the REST response in the original backend shape.
func writeReply(w http.ResponseWriter, result interface{}, rpcErr *rpcError) {
	w.Header().Set("Content-Type", "application/json")
	reply := restReply{Result: result}
	if rpcErr != nil {
		reply.Error = &restError{Code: rpcErr.Code, Message: rpcErr.Message}
	}
	_ = json.NewEncoder(w).Encode(reply)
}

// route dispatches the path to the matching handler.
func route(c *client, w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.Split(path, "/")

	switch {
	case len(parts) == 2 && parts[0] == "balance":
		c.handleBalance(w, r, parts[1])
	case len(parts) == 2 && parts[0] == "unspent":
		c.handleUnspent(w, r, parts[1])
	case len(parts) == 2 && parts[0] == "history":
		c.handleHistory(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "fee":
		c.handleFee(w, r)
	case len(parts) == 2 && parts[0] == "transaction":
		c.handleTransaction(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "broadcast" && r.Method == http.MethodPost:
		c.handleBroadcast(w, r)
	default:
		writeReply(w, nil, &rpcError{Code: -32601, Message: "method not found"})
	}
}

func main() {
	var o opts
	flag.StringVar(&o.listen, "listen", "127.0.0.1:8335", "listen address")
	flag.StringVar(&o.rpcHost, "rpcserver", "127.0.0.1:8334", "btcd RPC host:port")
	flag.StringVar(&o.rpcUser, "rpcuser", "sugar", "btcd RPC username")
	flag.StringVar(&o.rpcPass, "rpcpass", "", "btcd RPC password")
	flag.Parse()

	if o.rpcPass == "" {
		log.Fatal("rpcpass is required (set -rpcpass or edit the launcher)")
	}

	c := &client{
		url:  "http://" + o.rpcHost,
		user: o.rpcUser,
		pass: o.rpcPass,
		http: &http.Client{},
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		route(c, w, r)
	})

	log.Printf("walletapi listening on %s, forwarding to btcd %s", o.listen, o.rpcHost)
	if err := http.ListenAndServe(o.listen, nil); err != nil {
		log.Fatal(err)
	}
}
