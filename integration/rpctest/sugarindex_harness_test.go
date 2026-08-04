package rpctest

import (
	"encoding/json"
	"testing"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/txscript/v2"
	"github.com/btcsuite/btcd/wire/v2"
)

// rawCall issues an arbitrary JSON-RPC request to the harness node and returns
// the raw result.
func rawCall(t *testing.T, h *Harness, method string,
	params ...interface{}) json.RawMessage {

	t.Helper()
	rawParams := make([]json.RawMessage, 0, len(params))
	for _, p := range params {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal param for %s: %v", method, err)
		}
		rawParams = append(rawParams, b)
	}
	res, err := h.Client.RawRequest(method, rawParams)
	if err != nil {
		t.Fatalf("%s failed: %v", method, err)
	}
	return res
}

// TestSugarIndexRPC spins up a node with --sugarindex enabled, mines a small
// chain, and exercises every index RPC back-fitted from umami, cross-checking
// the returned data against getaddressbalance etc.
func TestSugarIndexRPC(t *testing.T) {
	h, err := New(&chaincfg.SimNetParams, nil, []string{"--sugarindex"}, "")
	if err != nil {
		t.Fatalf("unable to create harness: %v", err)
	}
	defer h.TearDown()

	if err := h.SetUp(true, 5); err != nil {
		t.Fatalf("unable to setup test chain: %v", err)
	}

	coinbaseAddr := h.wallet.coinbaseAddr.String()
	addrKey := map[string]interface{}{"addresses": []string{coinbaseAddr}}

	// getaddressbalance -- the coinbase address mined numMatureOutputs+100
	// blocks, so it must have a positive balance.
	var bal btcjson.GetAddressBalanceResult
	if err := json.Unmarshal(rawCall(t, h, "getaddressbalance", coinbaseAddr), &bal); err != nil {
		t.Fatalf("unmarshal getaddressbalance: %v", err)
	}
	if bal.Balance <= 0 || bal.Received <= 0 {
		t.Fatalf("getaddressbalance: expected positive balance/received, got %+v", bal)
	}
	if bal.BalanceImmature+bal.BalanceSpendable != bal.Balance {
		t.Fatalf("getaddressbalance: immature+spendable != balance: %+v", bal)
	}

	// getaddressesbalance -- same wallet address, object form.
	var balMulti btcjson.GetAddressBalanceResult
	if err := json.Unmarshal(rawCall(t, h, "getaddressesbalance", addrKey), &balMulti); err != nil {
		t.Fatalf("getaddressesbalance failed: %v", err)
	}
	if balMulti.Balance != bal.Balance {
		t.Fatalf("getaddressesbalance balance mismatch: got %d want %d",
			balMulti.Balance, bal.Balance)
	}

	// getaddressutxos -- unspent outputs for the coinbase address, sorted by
	// height ascending.
	var utxos []btcjson.GetAddressUtxosResult
	if err := json.Unmarshal(rawCall(t, h, "getaddressutxos", addrKey), &utxos); err != nil {
		t.Fatalf("unmarshal getaddressutxos: %v", err)
	}
	if len(utxos) == 0 {
		t.Fatalf("getaddressutxos: expected unspent outputs, got none")
	}
	var prevHeight int32 = -1
	for _, u := range utxos {
		if u.Address != coinbaseAddr {
			t.Fatalf("getaddressutxos: wrong address %q want %q", u.Address, coinbaseAddr)
		}
		if u.Height <= prevHeight {
			t.Fatalf("getaddressutxos: not sorted by height: %d after %d", u.Height, prevHeight)
		}
		prevHeight = u.Height
	}

	// getaddressdeltas -- deltas ascending by height, all positive (coinbase).
	var deltas []btcjson.GetAddressDeltasResult
	if err := json.Unmarshal(rawCall(t, h, "getaddressdeltas", addrKey), &deltas); err != nil {
		t.Fatalf("unmarshal getaddressdeltas: %v", err)
	}
	if len(deltas) != len(utxos) {
		t.Fatalf("getaddressdeltas: expected %d deltas, got %d", len(utxos), len(deltas))
	}
	for _, d := range deltas {
		if d.Address != coinbaseAddr || d.Satoshis <= 0 {
			t.Fatalf("getaddressdeltas: unexpected delta: %+v", d)
		}
	}

	// getaddresstxids -- one txid per coinbase block.
	var txids []string
	if err := json.Unmarshal(rawCall(t, h, "getaddresstxids", addrKey), &txids); err != nil {
		t.Fatalf("unmarshal getaddresstxids: %v", err)
	}
	if len(txids) != len(utxos) {
		t.Fatalf("getaddresstxids: expected %d txids, got %d", len(utxos), len(txids))
	}

	// getblockhashes -- the tip block's timestamp must land in [ts-1, ts+1].
	tipHash, err := h.Client.GetBlockHash(int64(h.ActiveNet.CoinbaseMaturity + 5))
	if err != nil {
		t.Fatalf("GetBlockHash: %v", err)
	}
	header, err := h.Client.GetBlockHeaderVerbose(tipHash)
	if err != nil {
		t.Fatalf("GetBlockHeaderVerbose: %v", err)
	}
	var bh []string
	if err := json.Unmarshal(rawCall(t, h, "getblockhashes", header.Time+1, header.Time-1), &bh); err != nil {
		t.Fatalf("unmarshal getblockhashes: %v", err)
	}
	found := false
	for _, bhHash := range bh {
		if bhHash == tipHash.String() {
			found = true
		}
	}
	if !found {
		t.Fatalf("getblockhashes: tip hash %q not in %v", tipHash, bh)
	}
	var bhLogical []btcjson.GetBlockHashesResult
	if err := json.Unmarshal(rawCall(t, h, "getblockhashes",
		header.Time+1, header.Time-1, map[string]bool{"logicalTimes": true}), &bhLogical); err != nil {
		t.Fatalf("unmarshal getblockhashes logicalTimes: %v", err)
	}

	// Build a transaction paying a fresh address, but do NOT mine it so it
	// lives in the mempool for the getaddressmempool check.
	spendAddr, err := h.NewAddress()
	if err != nil {
		t.Fatalf("NewAddress: %v", err)
	}
	spendAddrStr := spendAddr.String()
	pkScript, err := txscript.PayToAddrScript(spendAddr)
	if err != nil {
		t.Fatalf("PayToAddrScript: %v", err)
	}
	sendTxid, err := h.SendOutputs([]*wire.TxOut{wire.NewTxOut(100000, pkScript)}, 10)
	if err != nil {
		t.Fatalf("SendOutputs: %v", err)
	}

	// getaddressmempool -- the fresh address has one receiving delta.
	var mp []btcjson.GetAddressMempoolResult
	if err := json.Unmarshal(rawCall(t, h, "getaddressmempool",
		map[string]interface{}{"addresses": []string{spendAddrStr}}), &mp); err != nil {
		t.Fatalf("unmarshal getaddressmempool: %v", err)
	}
	if len(mp) != 1 {
		t.Fatalf("getaddressmempool: expected 1 delta, got %d", len(mp))
	}
	if mp[0].Address != spendAddrStr || mp[0].Satoshis != 100000 || mp[0].Timestamp <= 0 {
		t.Fatalf("getaddressmempool: unexpected delta: %+v", mp[0])
	}

	// Resolve the coinbase outpoint the mempool tx spends while it is still
	// in the mempool, before mining it.
	rawTx, err := h.Client.GetRawTransaction(sendTxid)
	if err != nil {
		t.Fatalf("GetRawTransaction: %v", err)
	}
	spentOp := rawTx.MsgTx().TxIn[0].PreviousOutPoint

	// Mine the spending tx.
	if _, err := h.Client.Generate(1); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// getspentinfo on the spent coinbase outpoint.
	var spent btcjson.GetSpentInfoResult
	if err := json.Unmarshal(rawCall(t, h, "getspentinfo",
		map[string]interface{}{"txid": spentOp.Hash.String(), "index": spentOp.Index}), &spent); err != nil {
		t.Fatalf("unmarshal getspentinfo: %v", err)
	}
	if spent.Txid != sendTxid.String() {
		t.Fatalf("getspentinfo: wrong spending txid %q want %q", spent.Txid, sendTxid)
	}
	if spent.Height <= 0 {
		t.Fatalf("getspentinfo: expected positive height, got %d", spent.Height)
	}

	// After mining, the fresh address mempool delta is gone.
	if err := json.Unmarshal(rawCall(t, h, "getaddressmempool",
		map[string]interface{}{"addresses": []string{spendAddrStr}}), &mp); err != nil {
		t.Fatalf("unmarshal getaddressmempool (post-mine): %v", err)
	}
	if len(mp) != 0 {
		t.Fatalf("getaddressmempool: expected 0 deltas after confirmation, got %d", len(mp))
	}
}