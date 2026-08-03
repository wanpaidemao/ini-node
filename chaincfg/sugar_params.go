// Copyright (c) 2024 The Sugarchain developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package chaincfg

import (
	"math/big"
	"time"

	"github.com/btcsuite/btcd/wire/v2"
)

// Sugarchain mainnet powLimit: 0x003fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff = 2^246 - 1
var sugarMainPowLimit = new(big.Int).Sub(new(big.Int).Lsh(bigOne, 246), bigOne)

// Sugarchain testnet powLimit: 0x003fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff (same)
var sugarTestNetPowLimit = new(big.Int).Sub(new(big.Int).Lsh(bigOne, 246), bigOne)

// Sugarchain regtest powLimit: 0x0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f
var sugarRegTestPowLimit = new(big.Int).SetBytes([]byte{
	0x0f, 0x0f, 0x0f, 0x0f, 0x0f, 0x0f, 0x0f, 0x0f,
	0x0f, 0x0f, 0x0f, 0x0f, 0x0f, 0x0f, 0x0f, 0x0f,
	0x0f, 0x0f, 0x0f, 0x0f, 0x0f, 0x0f, 0x0f, 0x0f,
	0x0f, 0x0f, 0x0f, 0x0f, 0x0f, 0x0f, 0x0f, 0x0f,
})

// SugarMainNetParams defines the network parameters for the Sugarchain main
// network.
var SugarMainNetParams = Params{
	Name:        "sugarmainnet",
	Net:         wire.BitcoinNet(0x9d4beb9f), // Magic on wire: 9f eb 4b 9d (LE-encoded)
	DefaultPort: "34230",
	DNSSeeds:    []DNSSeed{}, // No DNS seeds yet

	// Chain parameters
	GenesisBlock:             &sugarGenesisBlock,
	GenesisHash:              &sugarGenesisHash,
	PowLimit:                 sugarMainPowLimit,
	PowLimitBits:             0x1f3fffff, // powLimit in compact form
	BIP0034Height:            17,         // BIP34 active at height 17
	BIP0034Hash:              nil,        // Will be computed after genesis
	BIP0065Height:            0,          // BIP65 active at genesis
	BIP0066Height:            0,          // BIP66 active at genesis
	CoinbaseMaturity:         100,        // Same as Bitcoin
	SubsidyReductionInterval: 12500000,   // 12.5 million blocks
	TargetTimespan:           0,          // Not used (SugarShield uses 510-block window)
	TargetTimePerBlock:       5 * time.Second,
	RetargetAdjustmentFactor: 4,          // SugarShield has its own limits
	ReduceMinDifficulty:      false,
	MinDiffReductionTime:     0,
	GenerateSupported:        true,       // CPU mining supported

	// Checkpoints (empty for now)
	Checkpoints: nil,

	// Consensus rule change deployments
	// BIP34/65/66 active at genesis, CSV/SegWit active at genesis
	RuleChangeActivationThreshold: 9180, // 75% of MinerConfirmationWindow
	MinerConfirmationWindow:       12240,
	Deployments: [DefinedDeployments]ConsensusDeployment{
		DeploymentTestDummy: {
			BitNumber: 28,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Unix(1565881200, 0), // Sugarchain genesis: 2019-08-14
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Unix(1597417200, 0), // 1 year later
			),
		},
		DeploymentTestDummyMinActivation: {
			BitNumber:                 22,
			CustomActivationThreshold: 9180,
			MinActivationHeight:       0,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Time{}, // Always active
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{}, // Never expires
			),
		},
		DeploymentTestDummyAlwaysActive: {
			BitNumber: 30,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Time{}, // Always active
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{}, // Never expires
			),
			AlwaysActiveHeight: 0,
		},
		DeploymentCSV: {
			BitNumber: 0,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Unix(1565881200, 0), // Active at genesis
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{}, // Never expires
			),
			AlwaysActiveHeight: 0,
		},
		DeploymentSegwit: {
			BitNumber: 1,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Unix(1565881200, 0), // Active at genesis
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{}, // Never expires
			),
			AlwaysActiveHeight: 0,
		},
		DeploymentTaproot: {
			BitNumber: 2,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Unix(1565881200, 0), // Active at genesis
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{}, // Never expires
			),
			AlwaysActiveHeight: 0,
		},
	},

	// Mempool parameters
	RelayNonStdTxs: false,

	// Human-readable part for Bech32 encoded segwit addresses
	Bech32HRPSegwit: "sugar",

	// Address encoding magics
	PubKeyHashAddrID:        0x3f, // First byte of P2PKH address (63 = 'S')
	ScriptHashAddrID:        0x7d, // First byte of P2SH address (125 = 's')
	PrivateKeyID:            0x80, // Same as Bitcoin
	WitnessPubKeyHashAddrID: 0x06, // Same as Bitcoin
	WitnessScriptHashAddrID: 0x0A, // Same as Bitcoin

	// BIP32 hierarchical deterministic extended key magics
	HDPrivateKeyID: [4]byte{0x04, 0x88, 0xad, 0xe4}, // xprv
	HDPublicKeyID:  [4]byte{0x04, 0x88, 0xb2, 0x1e}, // xpub

	// BIP44 coin type
	HDCoinType: 0,
}

// SugarTestNetParams defines the network parameters for the Sugarchain test
// network.
var SugarTestNetParams = Params{
	Name:        "sugartestnet",
	Net:         wire.BitcoinNet(0x709011b0), // Magic on wire: b0 11 90 70 (LE-encoded)
	DefaultPort: "43230",
	DNSSeeds:    []DNSSeed{},

	// Chain parameters
	GenesisBlock:             &sugarTestNetGenesisBlock,
	GenesisHash:              &sugarTestNetGenesisHash,
	PowLimit:                 sugarTestNetPowLimit,
	PowLimitBits:             0x1f3fffff,
	BIP0034Height:            17,
	BIP0034Hash:              nil,
	BIP0065Height:            0,
	BIP0066Height:            0,
	CoinbaseMaturity:         100,
	SubsidyReductionInterval: 12500000,
	TargetTimespan:           0,
	TargetTimePerBlock:       5 * time.Second,
	RetargetAdjustmentFactor: 4,
	ReduceMinDifficulty:      false,
	MinDiffReductionTime:     0,
	GenerateSupported:        true,

	Checkpoints: nil,

	RuleChangeActivationThreshold: 9180,
	MinerConfirmationWindow:       12240,
	Deployments: [DefinedDeployments]ConsensusDeployment{
		DeploymentTestDummy: {
			BitNumber: 28,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Unix(1565881200, 0),
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{},
			),
		},
		DeploymentTestDummyMinActivation: {
			BitNumber:                 22,
			CustomActivationThreshold: 9180,
			MinActivationHeight:       0,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Time{},
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{},
			),
		},
		DeploymentTestDummyAlwaysActive: {
			BitNumber: 30,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Time{},
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{},
			),
			AlwaysActiveHeight: 0,
		},
		DeploymentCSV: {
			BitNumber: 0,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Unix(1565881200, 0),
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{},
			),
			AlwaysActiveHeight: 0,
		},
		DeploymentSegwit: {
			BitNumber: 1,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Unix(1565881200, 0),
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{},
			),
			AlwaysActiveHeight: 0,
		},
		DeploymentTaproot: {
			BitNumber: 2,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Unix(1565881200, 0),
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{},
			),
			AlwaysActiveHeight: 0,
		},
	},

	RelayNonStdTxs: false,

	Bech32HRPSegwit: "tugar",

	PubKeyHashAddrID:        0x42, // 'T' (66)
	ScriptHashAddrID:        0x80, // 't' (128)
	PrivateKeyID:            0xEF,
	WitnessPubKeyHashAddrID: 0x06,
	WitnessScriptHashAddrID: 0x0A,

	HDPrivateKeyID: [4]byte{0x04, 0x35, 0x87, 0xcf},
	HDPublicKeyID:  [4]byte{0x04, 0x35, 0x87, 0xd0},

	HDCoinType: 1,
}

// SugarRegTestParams defines the network parameters for the Sugarchain regression
// test network.
var SugarRegTestParams = Params{
	Name:        "sugarregtest",
	Net:         wire.BitcoinNet(0xad5bfbaf), // Magic on wire: af fb 5b ad (LE-encoded)
	DefaultPort: "18444",
	DNSSeeds:    []DNSSeed{},

	// Chain parameters
	GenesisBlock:             &sugarRegTestGenesisBlock,
	GenesisHash:              &sugarRegTestGenesisHash,
	PowLimit:                 sugarRegTestPowLimit,
	PowLimitBits:             0x200f0f0f, // compact of 0f0f0f0f... (C++ regtest genesis nBits)
	PoWNoRetargeting:         true,       // No retargeting in regtest
	CoinbaseMaturity:         100,
	BIP0034Height:            0,          // Active at genesis
	BIP0065Height:            0,
	BIP0066Height:            0,
	SubsidyReductionInterval: 150,        // Same as Bitcoin regtest
	TargetTimespan:           0,
	TargetTimePerBlock:       5 * time.Second,
	RetargetAdjustmentFactor: 4,
	ReduceMinDifficulty:      true,       // Min difficulty allowed
	MinDiffReductionTime:     20 * time.Second,
	GenerateSupported:        true,

	Checkpoints: nil,

	RuleChangeActivationThreshold: 9180,
	MinerConfirmationWindow:       12240,
	Deployments: [DefinedDeployments]ConsensusDeployment{
		DeploymentTestDummy: {
			BitNumber: 28,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Time{},
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{},
			),
		},
		DeploymentTestDummyMinActivation: {
			BitNumber:                 22,
			CustomActivationThreshold: 9180,
			MinActivationHeight:       0,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Time{},
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{},
			),
		},
		DeploymentTestDummyAlwaysActive: {
			BitNumber: 30,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Time{},
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{},
			),
			AlwaysActiveHeight: 0,
		},
		DeploymentCSV: {
			BitNumber: 0,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Time{},
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{},
			),
			AlwaysActiveHeight: 0,
		},
		DeploymentSegwit: {
			BitNumber: 1,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Time{},
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{},
			),
			AlwaysActiveHeight: 0,
		},
		DeploymentTaproot: {
			BitNumber: 2,
			DeploymentStarter: NewMedianTimeDeploymentStarter(
				time.Time{},
			),
			DeploymentEnder: NewMedianTimeDeploymentEnder(
				time.Time{},
			),
			AlwaysActiveHeight: 0,
		},
	},

	RelayNonStdTxs: false,

	Bech32HRPSegwit: "rugar",

	PubKeyHashAddrID:        0x3d, // 'R' (61)
	ScriptHashAddrID:        0x7b, // 'r' (123)
	PrivateKeyID:            0x80,
	WitnessPubKeyHashAddrID: 0x06,
	WitnessScriptHashAddrID: 0x0A,

	HDPrivateKeyID: [4]byte{0x04, 0x35, 0x87, 0xcf},
	HDPublicKeyID:  [4]byte{0x04, 0x35, 0x87, 0xd0},

	HDCoinType: 1,
}

// SugarNetMagicBytes returns the magic bytes for the Sugarchain network.
func SugarNetMagicBytes() wire.BitcoinNet {
	return wire.BitcoinNet(0x9d4beb9f)
}

// SugarTestNetMagicBytes returns the magic bytes for the Sugarchain test network.
func SugarTestNetMagicBytes() wire.BitcoinNet {
	return wire.BitcoinNet(0x709011b0)
}

// SugarRegTestMagicBytes returns the magic bytes for the Sugarchain regression test network.
func SugarRegTestMagicBytes() wire.BitcoinNet {
	return wire.BitcoinNet(0xad5bfbaf)
}

// SugarSubsidyCalculation returns the block subsidy for a given height.
// Initial subsidy = 42.94967296 SUGAR = 4294967296 satoshis = 2^32 satoshis
// Halving interval = 12,500,000 blocks
func SugarSubsidyCalculation(height int32) int64 {
	// Initial subsidy in satoshis: 4294967296 (2^32)
	initialSubsidy := int64(4294967296)

	// Number of halvings
	halvings := height / 12500000

	// No reward after 31 halvings (would be zero)
	if halvings >= 31 {
		return 0
	}

	// Calculate subsidy: initial / 2^halvings
	subsidy := initialSubsidy >> uint(halvings)

	return subsidy
}

// SugarMaxMoney returns the maximum supply of SUGAR.
// MAX_MONEY = 2^30 COIN = 1073741824 SUGAR = 107374182400000000 satoshis
func SugarMaxMoney() int64 {
	return int64(1) << 30 // 2^30 = 1073741824 SUGAR in satoshis
}

// SugarHalvingInterval returns the block subsidy halving interval.
func SugarHalvingInterval() int32 {
	return 12500000
}

// SugarDifficultyWindow returns the difficulty averaging window size (510 blocks).
func SugarDifficultyWindow() int32 {
	return 510
}

// SugarBlockTarget returns the target block time in seconds (5 seconds).
func SugarBlockTarget() int64 {
	return 5
}

// SugarMaxAdjustmentDown returns the maximum difficulty adjustment down (32%).
// In terms of the algorithm: 0x4000 / 0x8000 = 32768/65536 = 0.5 → but actual is 32%
func SugarMaxAdjustmentDown() int64 {
	return 32
}

// SugarMaxAdjustmentUp returns the maximum difficulty adjustment up (16%).
func SugarMaxAdjustmentUp() int64 {
	return 16
}
