// Copyright (c) 2026 The btcd developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/hex"
	"sort"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/sugarindex"
	"github.com/btcsuite/btcd/wire/v2"
)

// RPC error codes used by the umami address index RPCs.
const (
	// rpcMiscError mirrors bitcoin's RPC_MISC_ERROR.
	rpcMiscError = -1
	// rpcInvalidAddressOrKey mirrors bitcoin's RPC_INVALID_ADDRESS_OR_KEY.
	rpcInvalidAddressOrKey = -5
)

// sugarIndexError builds a JSON-RPC error with a raw code, mirroring the
// bitcoin core style error used by umami.
func sugarIndexError(code int, message string) *btcjson.RPCError {
	return &btcjson.RPCError{
		Code:    btcjson.RPCErrorCode(code),
		Message: message,
	}
}

// sugarIndexNotEnabled returns the error used when the address index is not
// enabled, mirroring umami's fAddressIndex check.
func sugarIndexNotEnabled() *btcjson.RPCError {
	return sugarIndexError(rpcMiscError, "Address index is not enabled.")
}

// indexKey identifies an address within the index by its type and hashBytes.
type indexKey struct {
	addrType int
	hash     []byte
}

// decodeIndexKeys maps a list of address strings to their index keys.
func decodeIndexKeys(s *rpcServer, addrs []string) ([]indexKey, error) {
	keys := make([]indexKey, 0, len(addrs))
	for _, a := range addrs {
		addrType, hashBytes, err := sugarindex.DecodeIndexKey(a, s.cfg.ChainParams)
		if err != nil {
			return nil, sugarIndexError(rpcInvalidAddressOrKey, "Invalid address")
		}
		keys = append(keys, indexKey{
			addrType: addrType,
			hash:     hashBytes,
		})
	}
	return keys, nil
}

// addressBalance computes the balance/immature/spendable/received for a set of
// index keys, mirroring the shared logic in umami's getaddressbalance and
// getaddressesbalance.
func (s *rpcServer) addressBalance(keys []indexKey) (*btcjson.GetAddressBalanceResult, error) {
	var deltas []sugarindex.AddressIndexEntry
	for _, k := range keys {
		err := s.cfg.SugarIndex.ReadAddressIndex(k.addrType, k.hash, 0, 0,
			func(entry sugarindex.AddressIndexEntry) bool {
				deltas = append(deltas, entry)
				return true
			})
		if err != nil {
			return nil, sugarIndexError(rpcInvalidAddressOrKey,
				"No information available for address")
		}
	}

	const coinbaseMaturity = 100
	nHeight := s.cfg.Chain.BestSnapshot().Height

	var balance, balanceSpendable, balanceImmature, received int64
	for _, d := range deltas {
		if d.Satoshis > 0 {
			received += d.Satoshis
		}
		if d.TxIndex == 0 && nHeight-d.BlockHeight < coinbaseMaturity {
			balanceImmature += d.Satoshis
		} else {
			balanceSpendable += d.Satoshis
		}
		balance += d.Satoshis
	}

	return &btcjson.GetAddressBalanceResult{
		Balance:          balance,
		BalanceImmature:  balanceImmature,
		BalanceSpendable: balanceSpendable,
		Received:         received,
	}, nil
}

// handleGetAddressBalance implements the getaddressbalance command.
func handleGetAddressBalance(s *rpcServer, cmd interface{},
	closeChan <-chan struct{}) (interface{}, error) {

	if s.cfg.SugarIndex == nil {
		return nil, sugarIndexNotEnabled()
	}

	c := cmd.(*btcjson.GetAddressBalanceCmd)
	addrType, hashBytes, err := sugarindex.DecodeIndexKey(c.Address, s.cfg.ChainParams)
	if err != nil {
		return nil, sugarIndexError(rpcInvalidAddressOrKey, "Invalid address")
	}

	return s.addressBalance([]indexKey{{addrType: addrType, hash: hashBytes}})
}

// handleGetAddressesBalance implements the getaddressesbalance command.
func handleGetAddressesBalance(s *rpcServer, cmd interface{},
	closeChan <-chan struct{}) (interface{}, error) {

	if s.cfg.SugarIndex == nil {
		return nil, sugarIndexNotEnabled()
	}

	c := cmd.(*btcjson.GetAddressesBalanceCmd)
	keys, err := decodeIndexKeys(s, c.Addresses.Addresses)
	if err != nil {
		return nil, err
	}

	return s.addressBalance(keys)
}

// handleGetAddressUtxos implements the getaddressutxos command.
func handleGetAddressUtxos(s *rpcServer, cmd interface{},
	closeChan <-chan struct{}) (interface{}, error) {

	if s.cfg.SugarIndex == nil {
		return nil, sugarIndexNotEnabled()
	}

	c := cmd.(*btcjson.GetAddressUtxosCmd)

	var requiredAmount int64
	if c.Amount != nil && *c.Amount > 0 {
		requiredAmount = int64(*c.Amount * 1e8)
	}
	includeChainInfo := c.ChainInfo != nil && *c.ChainInfo

	keys, err := decodeIndexKeys(s, c.Addresses.Addresses)
	if err != nil {
		return nil, err
	}

	var unspent []sugarindex.AddressUnspentEntry
	for _, k := range keys {
		outs, err := s.cfg.SugarIndex.ReadAddressUnspent(k.addrType, k.hash)
		if err != nil {
			return nil, sugarIndexError(rpcInvalidAddressOrKey,
				"No information available for address")
		}
		unspent = append(unspent, outs...)
	}

	// Sort ascending by block height, mirroring umami's heightSort.
	sort.SliceStable(unspent, func(i, j int) bool {
		return unspent[i].BlockHeight < unspent[j].BlockHeight
	})

	utxos := make([]btcjson.GetAddressUtxosResult, 0, len(unspent))
	var total int64
	for _, u := range unspent {
		if requiredAmount > 0 && total >= requiredAmount {
			break
		}

		addrStr, err := sugarindex.EncodeIndexAddress(u.Type, u.HashBytes,
			s.cfg.ChainParams)
		if err != nil {
			return nil, sugarIndexError(rpcInvalidAddressOrKey, "Unknown address type")
		}

		utxos = append(utxos, btcjson.GetAddressUtxosResult{
			Address:     addrStr,
			Txid:        u.TxHash.String(),
			OutputIndex: u.Index,
			Script:      hex.EncodeToString(u.Script),
			Satoshis:    u.Satoshis,
			Height:      u.BlockHeight,
		})
		total += u.Satoshis
	}

	if includeChainInfo {
		best := s.cfg.Chain.BestSnapshot()
		return &btcjson.GetAddressUtxosChainInfoResult{
			Utxos:  utxos,
			Hash:   best.Hash.String(),
			Height: best.Height,
		}, nil
	}

	return utxos, nil
}

// handleGetAddressDeltas implements the getaddressdeltas command.
func handleGetAddressDeltas(s *rpcServer, cmd interface{},
	closeChan <-chan struct{}) (interface{}, error) {

	if s.cfg.SugarIndex == nil {
		return nil, sugarIndexNotEnabled()
	}

	c := cmd.(*btcjson.GetAddressDeltasCmd)
	if c.Deltas == nil {
		return nil, sugarIndexError(rpcInvalidAddressOrKey, "Invalid address")
	}
	includeChainInfo := c.Deltas.ChainInfo != nil && *c.Deltas.ChainInfo

	start := int32(c.Deltas.Start)
	end := int32(c.Deltas.End)
	if start > 0 || end > 0 {
		if start <= 0 || end <= 0 {
			return nil, sugarIndexError(rpcInvalidAddressOrKey,
				"Start and end is expected to be greater than zero")
		}
		if end < start {
			return nil, sugarIndexError(rpcInvalidAddressOrKey,
				"End value is expected to be greater than start")
		}
	}

	keys, err := decodeIndexKeys(s, c.Deltas.Addresses)
	if err != nil {
		return nil, err
	}

	var deltas []sugarindex.AddressIndexEntry
	for _, k := range keys {
		err := s.cfg.SugarIndex.ReadAddressIndex(k.addrType, k.hash, start, end,
			func(entry sugarindex.AddressIndexEntry) bool {
				deltas = append(deltas, entry)
				return true
			})
		if err != nil {
			return nil, sugarIndexError(rpcInvalidAddressOrKey,
				"No information available for address")
		}
	}

	result := make([]btcjson.GetAddressDeltasResult, 0, len(deltas))
	for _, d := range deltas {
		addrStr, err := sugarindex.EncodeIndexAddress(d.Type, d.HashBytes,
			s.cfg.ChainParams)
		if err != nil {
			return nil, sugarIndexError(rpcInvalidAddressOrKey, "Unknown address type")
		}

		result = append(result, btcjson.GetAddressDeltasResult{
			Satoshis:   d.Satoshis,
			Txid:       d.TxHash.String(),
			Index:      d.Index,
			Blockindex: d.TxIndex,
			Height:     d.BlockHeight,
			Address:    addrStr,
		})
	}

	if includeChainInfo && start > 0 && end > 0 {
		tipHeight := s.cfg.Chain.BestSnapshot().Height
		if start > tipHeight || end > tipHeight {
			return nil, sugarIndexError(rpcInvalidAddressOrKey,
				"Start or end is outside chain range")
		}

		startHash, err := s.cfg.Chain.BlockHashByHeight(start)
		if err != nil {
			return nil, sugarIndexError(rpcInvalidAddressOrKey,
				"Start or end is outside chain range")
		}
		endHash, err := s.cfg.Chain.BlockHashByHeight(end)
		if err != nil {
			return nil, sugarIndexError(rpcInvalidAddressOrKey,
				"Start or end is outside chain range")
		}

		return &btcjson.GetAddressDeltasChainInfoResult{
			Deltas: result,
			Start: btcjson.GetAddressDeltaEndResult{
				Hash:   startHash.String(),
				Height: start,
			},
			End: btcjson.GetAddressDeltaEndResult{
				Hash:   endHash.String(),
				Height: end,
			},
		}, nil
	}

	return result, nil
}

// handleGetAddressTxids implements the getaddresstxids command.
func handleGetAddressTxids(s *rpcServer, cmd interface{},
	closeChan <-chan struct{}) (interface{}, error) {

	if s.cfg.SugarIndex == nil {
		return nil, sugarIndexNotEnabled()
	}

	c := cmd.(*btcjson.GetAddressTxidsCmd)
	if c.Txids == nil {
		return nil, sugarIndexError(rpcInvalidAddressOrKey, "Invalid address")
	}
	start := int32(c.Txids.Start)
	end := int32(c.Txids.End)

	keys, err := decodeIndexKeys(s, c.Txids.Addresses)
	if err != nil {
		return nil, err
	}

	// (height, txid) pairs are collected into a set for deduplication,
	// mirroring umami's std::set<std::pair<int, std::string>>.
	type heightTxid struct {
		height int32
		txid   string
	}
	seen := make(map[heightTxid]struct{})
	for _, k := range keys {
		err := s.cfg.SugarIndex.ReadAddressIndex(k.addrType, k.hash, start, end,
			func(entry sugarindex.AddressIndexEntry) bool {
				seen[heightTxid{
					height: entry.BlockHeight,
					txid:   entry.TxHash.String(),
				}] = struct{}{}
				return true
			})
		if err != nil {
			return nil, sugarIndexError(rpcInvalidAddressOrKey,
				"No information available for address")
		}
	}

	if len(c.Txids.Addresses) == 1 {
		// Single address: preserve the delta iteration order (already
		// ascending by height), deduplicating as we go.
		var result []string
		lastTxid := ""
		for _, k := range keys {
			err := s.cfg.SugarIndex.ReadAddressIndex(k.addrType, k.hash, start, end,
				func(entry sugarindex.AddressIndexEntry) bool {
					txid := entry.TxHash.String()
					if txid != lastTxid {
						result = append(result, txid)
						lastTxid = txid
					}
					return true
				})
			if err != nil {
				return nil, sugarIndexError(rpcInvalidAddressOrKey,
					"No information available for address")
			}
		}
		return result, nil
	}

	// Multiple addresses: sort the set by (height, txid).
	pairs := make([]heightTxid, 0, len(seen))
	for pair := range seen {
		pairs = append(pairs, pair)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].height != pairs[j].height {
			return pairs[i].height < pairs[j].height
		}
		return pairs[i].txid < pairs[j].txid
	})

	result := make([]string, 0, len(pairs))
	for _, p := range pairs {
		result = append(result, p.txid)
	}
	return result, nil
}

// handleGetAddressMempool implements the getaddressmempool command.
func handleGetAddressMempool(s *rpcServer, cmd interface{},
	closeChan <-chan struct{}) (interface{}, error) {

	if s.cfg.SugarIndex == nil {
		return nil, sugarIndexNotEnabled()
	}

	c := cmd.(*btcjson.GetAddressMempoolCmd)
	if c.Addresses == nil {
		return nil, sugarIndexError(rpcInvalidAddressOrKey, "Invalid address")
	}
	keys, err := decodeIndexKeys(s, c.Addresses.Addresses)
	if err != nil {
		return nil, err
	}

	type deltaKey struct {
		addrType int
		hash     string
		txid     chainhash.Hash
		index    uint32
	}
	type deltaVal struct {
		amount   int64
		time     int64
		prevtxid *chainhash.Hash
		prevout  *uint32
	}

	matchingKey := func(addrType int, hashBytes []byte) (int, string, bool) {
		for _, k := range keys {
			if addrType == k.addrType && bytes.Equal(hashBytes, k.hash) {
				return addrType, string(hashBytes), true
			}
		}
		return 0, "", false
	}

	indexes := make(map[deltaKey]deltaVal)
	for _, desc := range s.cfg.TxMemPool.TxDescs() {
		tx := desc.Tx.MsgTx()
		added := desc.Added.Unix()

		// Receiving outputs.
		for i, out := range tx.TxOut {
			addrType, hashBytes := sugarindex.ExtractIndexInfo(out.PkScript)
			if addrType == sugarindex.AddrIndtUnknown {
				continue
			}
			if mt, hb, ok := matchingKey(addrType, hashBytes); ok {
				indexes[deltaKey{
					addrType: mt,
					hash:     hb,
					txid:     tx.TxHash(),
					index:    uint32(i),
				}] = deltaVal{amount: out.Value, time: added}
			}
		}

		// Spending inputs.
		for i, in := range tx.TxIn {
			prevScript, prevValue, found := s.prevOutScript(in.PreviousOutPoint)
			if !found {
				continue
			}
			addrType, hashBytes := sugarindex.ExtractIndexInfo(prevScript)
			if addrType == sugarindex.AddrIndtUnknown {
				continue
			}
			if mt, hb, ok := matchingKey(addrType, hashBytes); ok {
				prevHash := in.PreviousOutPoint.Hash
				prevIndex := in.PreviousOutPoint.Index
				indexes[deltaKey{
					addrType: mt,
					hash:     hb,
					txid:     tx.TxHash(),
					index:    uint32(i),
				}] = deltaVal{
					amount:   -prevValue,
					time:     added,
					prevtxid: &prevHash,
					prevout:  &prevIndex,
				}
			}
		}
	}

	// Sort ascending by timestamp, mirroring umami's timestampSort.
	keys2 := make([]deltaKey, 0, len(indexes))
	for k := range indexes {
		keys2 = append(keys2, k)
	}
	sort.SliceStable(keys2, func(i, j int) bool {
		return indexes[keys2[i]].time < indexes[keys2[j]].time
	})

	result := make([]btcjson.GetAddressMempoolResult, 0, len(keys2))
	for _, k := range keys2 {
		v := indexes[k]
		addrStr, err := sugarindex.EncodeIndexAddress(k.addrType, []byte(k.hash),
			s.cfg.ChainParams)
		if err != nil {
			return nil, sugarIndexError(rpcInvalidAddressOrKey, "Unknown address type")
		}

		entry := btcjson.GetAddressMempoolResult{
			Address:   addrStr,
			Txid:      k.txid.String(),
			Index:     k.index,
			Satoshis:  v.amount,
			Timestamp: v.time,
		}
		if v.prevtxid != nil {
			txidStr := v.prevtxid.String()
			prevout := *v.prevout
			entry.Prevtxid = &txidStr
			entry.Prevout = &prevout
		}
		result = append(result, entry)
	}

	return result, nil
}

// prevOutScript resolves the pkScript and value of a spent output, checking the
// chain UTXO set first and the mempool second.
func (s *rpcServer) prevOutScript(op wire.OutPoint) ([]byte, int64, bool) {
	if entry, err := s.cfg.Chain.FetchUtxoEntry(op); err == nil && entry != nil {
		return entry.PkScript(), entry.Amount(), true
	}

	if tx, err := s.cfg.TxMemPool.FetchTransaction(&op.Hash); err == nil {
		msgTx := tx.MsgTx()
		if int(op.Index) < len(msgTx.TxOut) {
			out := msgTx.TxOut[op.Index]
			return out.PkScript, out.Value, true
		}
	}

	return nil, 0, false
}

// handleGetBlockHashes implements the getblockhashes command.
func handleGetBlockHashes(s *rpcServer, cmd interface{},
	closeChan <-chan struct{}) (interface{}, error) {

	c := cmd.(*btcjson.GetBlockHashesCmd)
	high := uint32(c.High)
	low := uint32(c.Low)

	fActiveOnly := c.Options != nil && c.Options.NoOrphans
	fLogicalTS := c.Options != nil && c.Options.LogicalTimes

	entries, err := s.cfg.SugarIndex.ReadTimestampIndex(low, high)
	if err != nil {
		return nil, sugarIndexError(rpcInvalidAddressOrKey,
			"No information available for block hashes")
	}

	if fActiveOnly {
		active := make([]sugarindex.TimestampEntry, 0, len(entries))
		for _, e := range entries {
			if _, err := s.cfg.Chain.BlockHeightByHash(&e.BlockHash); err == nil {
				active = append(active, e)
			}
		}
		entries = active
	}

	if fLogicalTS {
		result := make([]btcjson.GetBlockHashesResult, 0, len(entries))
		for _, e := range entries {
			result = append(result, btcjson.GetBlockHashesResult{
				Blockhash: e.BlockHash.String(),
				Logicalts: e.Timestamp,
			})
		}
		return result, nil
	}

	result := make([]string, 0, len(entries))
	for _, e := range entries {
		result = append(result, e.BlockHash.String())
	}
	return result, nil
}

// handleGetSpentInfo implements the getspentinfo command.
func handleGetSpentInfo(s *rpcServer, cmd interface{},
	closeChan <-chan struct{}) (interface{}, error) {

	c := cmd.(*btcjson.GetSpentInfoCmd)
	if c.Inputs == nil {
		return nil, sugarIndexError(rpcInvalidAddressOrKey, "Invalid txid or index")
	}

	txid, err := chainhash.NewHashFromStr(c.Inputs.Txid)
	if err != nil {
		return nil, sugarIndexError(rpcInvalidAddressOrKey, "Invalid txid or index")
	}

	key := &sugarindex.SpentIndexKey{
		TxID:        *txid,
		OutputIndex: c.Inputs.Index,
	}
	val, err := s.cfg.SugarIndex.ReadSpentIndex(key)
	if err != nil {
		return nil, sugarIndexError(rpcInvalidAddressOrKey, "Unable to get spent info")
	}
	if val == nil {
		return nil, sugarIndexError(rpcInvalidAddressOrKey, "Unable to get spent info")
	}

	return &btcjson.GetSpentInfoResult{
		Txid:   val.TxID.String(),
		Index:  val.InputIndex,
		Height: val.BlockHeight,
	}, nil
}
