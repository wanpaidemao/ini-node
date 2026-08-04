package btcjson

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestSugarIndexCmdWire ensures the sugar index commands unmarshal from the
// exact wire format umami accepts.
func TestSugarIndexCmdWire(t *testing.T) {
	tests := []struct {
		name    string
		request string
		want    interface{}
	}{
		{
			name:    "getaddressbalance",
			request: `{"jsonrpc":"1.0","id":1,"method":"getaddressbalance","params":["Pb7FLL3DyaAVP2eGfRiEkj4U8ZJ3RHLY9g"]}`,
			want: &GetAddressBalanceCmd{
				Address: "Pb7FLL3DyaAVP2eGfRiEkj4U8ZJ3RHLY9g",
			},
		},
		{
			name: "getaddressesbalance",
			request: `{"jsonrpc":"1.0","id":1,"method":"getaddressesbalance","params":[{"addresses":["Pb7FLL3DyaAVP2eGfRiEkj4U8ZJ3RHLY9g"]}]}`,
			want: &GetAddressesBalanceCmd{
				Addresses: getAddressesBalanceParams{
					Addresses: []string{"Pb7FLL3DyaAVP2eGfRiEkj4U8ZJ3RHLY9g"},
				},
			},
		},
		{
			name:    "getaddressutxos with all params",
			request: `{"jsonrpc":"1.0","id":1,"method":"getaddressutxos","params":[{"addresses":["Pb7FLL3DyaAVP2eGfRiEkj4U8ZJ3RHLY9g"]},0.1,true]}`,
			want: &GetAddressUtxosCmd{
				Addresses: getAddressUtxosParams{
					Addresses: []string{"Pb7FLL3DyaAVP2eGfRiEkj4U8ZJ3RHLY9g"},
				},
				Amount:    ptrF(0.1),
				ChainInfo: ptrB(true),
			},
		},
		{
			name:    "getaddressutxos default amount and chainInfo",
			request: `{"jsonrpc":"1.0","id":1,"method":"getaddressutxos","params":[{"addresses":["Pb7FLL3DyaAVP2eGfRiEkj4U8ZJ3RHLY9g"]}]}`,
			want: &GetAddressUtxosCmd{
				Addresses: getAddressUtxosParams{
					Addresses: []string{"Pb7FLL3DyaAVP2eGfRiEkj4U8ZJ3RHLY9g"},
				},
				Amount:    ptrF(0),
				ChainInfo: ptrB(false),
			},
		},
		{
			name:    "getaddressdeltas",
			request: `{"jsonrpc":"1.0","id":1,"method":"getaddressdeltas","params":[{"addresses":["Pb7FLL3DyaAVP2eGfRiEkj4U8ZJ3RHLY9g"],"start":1000,"end":2000,"chainInfo":true}]}`,
			want: &GetAddressDeltasCmd{
				Deltas: &getAddressDeltasParams{
					Addresses: []string{"Pb7FLL3DyaAVP2eGfRiEkj4U8ZJ3RHLY9g"},
					Start:     1000,
					End:       2000,
					ChainInfo: ptrB(true),
				},
			},
		},
		{
			name:    "getaddresstxids",
			request: `{"jsonrpc":"1.0","id":1,"method":"getaddresstxids","params":[{"addresses":["Pb7FLL3DyaAVP2eGfRiEkj4U8ZJ3RHLY9g"]}]}`,
			want: &GetAddressTxidsCmd{
				Txids: &getAddressTxidsParams{
					Addresses: []string{"Pb7FLL3DyaAVP2eGfRiEkj4U8ZJ3RHLY9g"},
				},
			},
		},
		{
			name:    "getaddressmempool",
			request: `{"jsonrpc":"1.0","id":1,"method":"getaddressmempool","params":[{"addresses":["Pb7FLL3DyaAVP2eGfRiEkj4U8ZJ3RHLY9g"]}]}`,
			want: &GetAddressMempoolCmd{
				Addresses: &getAddressMempoolParams{
					Addresses: []string{"Pb7FLL3DyaAVP2eGfRiEkj4U8ZJ3RHLY9g"},
				},
			},
		},
		{
			name:    "getblockhashes",
			request: `{"jsonrpc":"1.0","id":1,"method":"getblockhashes","params":[1231614698,1231024505,{"noOrphans":true,"logicalTimes":true}]}`,
			want: &GetBlockHashesCmd{
				High: 1231614698,
				Low:  1231024505,
				Options: &GetBlockHashesOptionsCmd{
					NoOrphans:    true,
					LogicalTimes: true,
				},
			},
		},
		{
			name:    "getspentinfo",
			request: `{"jsonrpc":"1.0","id":1,"method":"getspentinfo","params":[{"txid":"0437cd7f8525ceed2324359c2d0ba26006d92d856a9c20fa0241106ee5a597c9","index":0}]}`,
			want: &GetSpentInfoCmd{
				Inputs: &getSpentInfoParams{
					Txid:  "0437cd7f8525ceed2324359c2d0ba26006d92d856a9c20fa0241106ee5a597c9",
					Index: 0,
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var request Request
			if err := json.Unmarshal([]byte(test.request), &request); err != nil {
				t.Fatalf("failed to unmarshal request: %v", err)
			}
			cmd, err := UnmarshalCmd(&request)
			if err != nil {
				t.Fatalf("failed to unmarshal cmd: %v", err)
			}
			if !reflect.DeepEqual(cmd, test.want) {
				t.Errorf("unexpected cmd: got %#v, want %#v", cmd, test.want)
			}
		})
	}
}

func ptrF(v float64) *float64 { return &v }
func ptrB(v bool) *bool       { return &v }
