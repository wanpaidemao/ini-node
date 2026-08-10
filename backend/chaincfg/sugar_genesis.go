// Copyright (c) 2024 The Sugarchain developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package chaincfg

import (
	"time"

	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/wire/v2"
)

// sugarGenesisCoinbaseTx is the coinbase transaction of every Sugarchain
// genesis block (mainnet, testnet and regtest all share the same coinbase tx).
//
// Built to match the C++ createGenesisBlock in umami/src/kernel/chainparams.cpp
// exactly. The C++ scriptSig is:
//
//   CScript() << 486604799 << CScriptNum(4) <<
//       std::vector<unsigned char>(pszTimestamp, pszTimestamp + strlen(pszTimestamp));
//
// where pszTimestamp is the 87-byte string:
//   "The Times 17/July/2019 Bitcoin falls after senators call Facebook
//    delusional over libra"
//
// The serialised scriptSig is therefore:
//   - 0x04 <FF FF 00 1D>            // push32 of CScriptNum::serialize(486604799)
//   - 0x01 <04>                     // push1  of CScriptNum::serialize(4)
//   - 0x4c 0x57 <87-byte timestamp> // OP_PUSHDATA1 + len(87) + bytes  (since 87 >= 76)
// Totalling 96 bytes.
//
// The output script is the same as Bitcoin's original genesis block:
//   CScript() << ParseHex("04678afd...5f") << OP_CHECKSIG
// i.e. 0x41 (<65-byte pubkey>) 0xac, totalling 67 bytes.
//
// The output value is 0x100000000 satoshis = 42.94967296 SUGAR.
var sugarGenesisCoinbaseTx = wire.MsgTx{
	Version: 1,
	TxIn: []*wire.TxIn{
		{
			PreviousOutPoint: wire.OutPoint{
				Hash:  chainhash.Hash{},
				Index: 0xffffffff,
			},
			SignatureScript: []byte{
				// push4 CScriptNum::serialize(486604799) = {0xff, 0xff, 0x00, 0x1d}
				0x04, 0xff, 0xff, 0x00, 0x1d,
				// push1 CScriptNum::serialize(4) = {0x04}
				0x01, 0x04,
				// OP_PUSHDATA1 (0x4c) + length 87 (0x57) followed by the 87-byte
				// "The Times 17/July/2019 Bitcoin falls after senators call
				//  Facebook delusional over libra"
				0x4c, 0x57,
				0x54, 0x68, 0x65, 0x20, 0x54, 0x69, 0x6d, 0x65, /* The Time */
				0x73, 0x20, 0x31, 0x37, 0x2f, 0x4a, 0x75, 0x6c, /* s 17/Jul */
				0x79, 0x2f, 0x32, 0x30, 0x31, 0x39, 0x20, 0x42, /* y/2019 B */
				0x69, 0x74, 0x63, 0x6f, 0x69, 0x6e, 0x20, 0x66, /* itcoin f */
				0x61, 0x6c, 0x6c, 0x73, 0x20, 0x61, 0x66, 0x74, /* alls aft */
				0x65, 0x72, 0x20, 0x73, 0x65, 0x6e, 0x61, 0x74, /* er senat */
				0x6f, 0x72, 0x73, 0x20, 0x63, 0x61, 0x6c, 0x6c, /* ors call */
				0x20, 0x46, 0x61, 0x63, 0x65, 0x62, 0x6f, 0x6f, /*  Facebook */
				0x6b, 0x20, 0x64, 0x65, 0x6c, 0x75, 0x73, 0x69, /* k delusi */
				0x6f, 0x6e, 0x61, 0x6c, 0x20, 0x6f, 0x76, 0x65, /* onal ove */
				0x72, 0x20, 0x6c, 0x69, 0x62, 0x72, 0x61, /* r libra */
			},
			Sequence: 0xffffffff,
		},
	},
	TxOut: []*wire.TxOut{
		{
			// 4294967296 satoshis = 42.94967296 SUGAR
			Value: 0x100000000,
			// P2PK script (uncompressed pubkey) shared with Bitcoin's genesis,
			// 0x41 <65-byte pubkey> 0xac.
			PkScript: []byte{
				0x41,
				0x04, 0x67, 0x8a, 0xfd, 0xb0, 0xfe, 0x55, 0x48,
				0x27, 0x19, 0x67, 0xf1, 0xa6, 0x71, 0x30, 0xb7,
				0x10, 0x5c, 0xd6, 0xa8, 0x28, 0xe0, 0x39, 0x09,
				0xa6, 0x79, 0x62, 0xe0, 0xea, 0x1f, 0x61, 0xde,
				0xb6, 0x49, 0xf6, 0xbc, 0x3f, 0x4c, 0xef, 0x38,
				0xc4, 0xf3, 0x55, 0x04, 0xe5, 0x1e, 0xc1, 0x12,
				0xde, 0x5c, 0x38, 0x4d, 0xf7, 0xba, 0x0b, 0x8d,
				0x57, 0x8a, 0x4c, 0x70, 0x2b, 0x6b, 0xf1, 0x1d,
				0x5f,
				0xac,
			},
		},
	},
	LockTime: 0,
}

// sugarGenesisMerkleRoot is the hash of the genesis coinbase transaction.
// Because a block with a single transaction has its merkle root equal to the
// coinbase txid (see C++ ComputeMerkleRoot in consensus/merkle.cpp returning
// leaves[0] when the vector has exactly one element), we compute this directly
// from the coinbase transaction itself rather than hard-coding the constant.
//
// The expected RPC/display value (matching C++'s
//   assert(genesis.hashMerkleRoot ==
//       uint256S("0x7677ce2a579cb0411d1c9e6b1e9072b8f537f1e59cb387dacac2daac56e150b0"))
// in chainparams.cpp:146) is verified in init().
var sugarGenesisMerkleRoot = sugarGenesisCoinbaseTx.TxHash()

// sugarGenesisBlock is the genesis block of the Sugarchain main network.
// Matches umami/src/kernel/chainparams.cpp:141
//   genesis = CreateGenesisBlock(1565881200, 247, 0x1f3fffff, 1, 42.94967296 * COIN);
var sugarGenesisBlock = wire.MsgBlock{
	Header: wire.BlockHeader{
		Version:    1,
		PrevBlock:  chainhash.Hash{},
		MerkleRoot: sugarGenesisMerkleRoot,
		Timestamp:  time.Unix(1565881200, 0), // 2019-08-14 20:00:00 UTC
		Bits:       0x1f3fffff,
		Nonce:      247,
	},
	Transactions: []*wire.MsgTx{&sugarGenesisCoinbaseTx},
}

// sugarGenesisHash is the hash of the Sugarchain main-network genesis block,
// computed directly from the block header (Sha256d in C++, see
// consensus/hash.h:CHashWriter::GetHash) — exactly matching
//   consensus.hashGenesisBlock = genesis.GetHash();
// at chainparams.cpp:142.  The expected value
//   0x7d5eaec2dbb75f99feadfa524c78b7cabc1d8c8204f79d4f3a83381b811b0adc
// from chainparams.cpp:145 is verified in init().
var sugarGenesisHash = sugarGenesisBlock.Header.BlockHash()

// sugarTestNetGenesisBlock is the Sugarchain testnet genesis block, matches
// umami/src/kernel/chainparams.cpp:251
//   genesis = CreateGenesisBlock(1565913601, 490, 0x1f3fffff, 1, 42.94967296 * COIN);
var sugarTestNetGenesisBlock = wire.MsgBlock{
	Header: wire.BlockHeader{
		Version:    1,
		PrevBlock:  chainhash.Hash{},
		MerkleRoot: sugarGenesisMerkleRoot,
		Timestamp:  time.Unix(1565913601, 0), // 2019-08-15 05:00:01 UTC
		Bits:       0x1f3fffff,
		Nonce:      490,
	},
	Transactions: []*wire.MsgTx{&sugarGenesisCoinbaseTx},
}

var sugarTestNetGenesisHash = sugarTestNetGenesisBlock.Header.BlockHash()

// sugarRegTestGenesisBlock is the Sugarchain regtest genesis block, matches
// umami/src/kernel/chainparams.cpp:398
//   genesis = CreateGenesisBlock(1602302400, 862, 0x1f7fffff, 1, 42.94967296 * COIN);
var sugarRegTestGenesisBlock = wire.MsgBlock{
	Header: wire.BlockHeader{
		Version:    1,
		PrevBlock:  chainhash.Hash{},
		MerkleRoot: sugarGenesisMerkleRoot,
		Timestamp:  time.Unix(1602302400, 0), // 2020-10-10 00:00:00 UTC
		Bits:       0x1f7fffff,
		Nonce:      862,
	},
	Transactions: []*wire.MsgTx{&sugarGenesisCoinbaseTx},
}

var sugarRegTestGenesisHash = sugarRegTestGenesisBlock.Header.BlockHash()

// sugarTestNetGenesisMerkleRoot and sugarRegTestGenesisMerkleRoot are
// kept for backwards compatibility with external readers; all three
// networks share the same coinbase transaction so the value is identical
// to sugarGenesisMerkleRoot.
var (
	sugarTestNetGenesisMerkleRoot = sugarGenesisMerkleRoot
	sugarRegTestGenesisMerkleRoot = sugarGenesisMerkleRoot
)

// mustParseHash converts a hex string (display/byte-reversed form, matching the
// C++ uint256S and RPC display conventions) into a chainhash.Hash.  Panics on
// parse error — only malicious programmer error can trigger this.
func mustParseHash(s string) chainhash.Hash {
	h, err := chainhash.NewHashFromStr(s)
	if err != nil {
		panic(err)
	}
	return *h
}

// init performs the same role as the C++ assert() calls in umami's
// chainparams.cpp:144-146, 254-256 and 399-401.  If anything about the
// construction of the genesis blocks diverges from the published Sugarchain
// network parameters, the node refuses to start rather than risk serving an
// incorrect identity to peers.
func init() {
	// ---- Mainnet (chainparams.cpp:144-146) ----
	mustEqualHash("mainnet merkle root",
		sugarGenesisMerkleRoot,
		"7677ce2a579cb0411d1c9e6b1e9072b8f537f1e59cb387dacac2daac56e150b0")
	mustEqualHash("mainnet block hash",
		sugarGenesisHash,
		"7d5eaec2dbb75f99feadfa524c78b7cabc1d8c8204f79d4f3a83381b811b0adc")

	// ---- Testnet (chainparams.cpp:254-256) ----
	// C++ asserts the PoW-sha (yespower) hash too, but the Go yespower
	// package isn't imported by chaincfg, so we only verify the block
	// identity hash and the shared merkle root here.
	mustEqualHash("testnet block hash",
		sugarTestNetGenesisHash,
		"e0e0e42e493ba7b15f7b0fe1a7e66f73b7fd8b3e6e6a7b0e821a6b95040d3826")

	// ---- Regtest (chainparams.cpp:401-403) ----
	mustEqualHash("regtest block hash",
		sugarRegTestGenesisHash,
		"223231facc4c2337baedba62921cf0ada7f867a869194ce9b3697eefd9d54c59")
}

// mustEqualHash panics with a readable diagnostic when the computed
// chainhash.Hash does not equal the expected display-form hex string.  This
// mirrors C++'s assert(genesis.hashMerkleRoot == uint256S("0x...")) behaviour.
func mustEqualHash(label string, got chainhash.Hash, wantHex string) {
	want := mustParseHash(wantHex)
	if got != want {
		panic("chaincfg: " + label + " mismatch\n" +
			"  computed: " + got.String() + "\n" +
			"  expected: " + wantHex)
	}
}
