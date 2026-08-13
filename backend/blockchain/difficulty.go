// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package blockchain

import (
	"math/big"
	"time"

	"github.com/btcsuite/btcd/blockchain/internal/workmath"
	"github.com/btcsuite/btcd/chainhash/v2"
)

// SugarShield-N510 constants
const (
	// SugarPowAveragingWindow is the number of blocks used for difficulty averaging
	SugarPowAveragingWindow = 510

	// SugarPowTargetSpacing is the target block time in seconds (5 seconds)
	SugarPowTargetSpacing = 5

	// SugarPowMaxAdjustDown is the maximum percentage difficulty can decrease
	SugarPowMaxAdjustDown = 32

	// SugarPowMaxAdjustUp is the maximum percentage difficulty can increase
	SugarPowMaxAdjustUp = 16
)

// SugarAveragingWindowTimespan returns the total time span of the averaging window
// = 510 * 5 = 2550 seconds
func SugarAveragingWindowTimespan() int64 {
	return int64(SugarPowAveragingWindow * SugarPowTargetSpacing)
}

// SugarMinActualTimespan returns the minimum allowed actual timespan
// = 2550 * (100 - 16) / 100 = 2142 seconds
func SugarMinActualTimespan() int64 {
	return SugarAveragingWindowTimespan() * (100 - SugarPowMaxAdjustUp) / 100
}

// SugarMaxActualTimespan returns the maximum allowed actual timespan
// = 2550 * (100 + 32) / 100 = 3366 seconds
func SugarMaxActualTimespan() int64 {
	return SugarAveragingWindowTimespan() * (100 + SugarPowMaxAdjustDown) / 100
}

// HashToBig converts a chainhash.Hash into a big.Int that can be used to
// perform math comparisons.
func HashToBig(hash *chainhash.Hash) *big.Int {
	return workmath.HashToBig(hash)
}

// CompactToBig converts a compact representation of a whole number N to an
// unsigned 32-bit number.  The representation is similar to IEEE754 floating
// point numbers.
//
// Like IEEE754 floating point, there are three basic components: the sign,
// the exponent, and the mantissa.  They are broken out as follows:
//
// - the most significant 8 bits represent the unsigned base 256 exponent
// - bit 23 (the 24th bit) represents the sign bit
// - the least significant 23 bits represent the mantissa
//
//	-------------------------------------------------
//	|   Exponent     |    Sign    |    Mantissa     |
//	-------------------------------------------------
//	| 8 bits [31-24] | 1 bit [23] | 23 bits [22-00] |
//	-------------------------------------------------
//
// The formula to calculate N is:
//
//	N = (-1^sign) * mantissa * 256^(exponent-3)
//
// This compact form is only used in bitcoin to encode unsigned 256-bit numbers
// which represent difficulty targets, thus there really is not a need for a
// sign bit, but it is implemented here to stay consistent with bitcoind.
func CompactToBig(compact uint32) *big.Int {
	return workmath.CompactToBig(compact)
}

// BigToCompact converts a whole number N to a compact representation using
// an unsigned 32-bit number.  The compact representation only provides 23 bits
// of precision, so values larger than (2^23 - 1) only encode the most
// significant digits of the number.  See CompactToBig for details.
func BigToCompact(n *big.Int) uint32 {
	return workmath.BigToCompact(n)
}

// CalcWork calculates a work value from difficulty bits.  Bitcoin increases
// the difficulty for generating a block by decreasing the value which the
// generated hash must be less than.  This difficulty target is stored in each
// block header using a compact representation as described in the documentation
// for CompactToBig.  The main chain is selected by choosing the chain that has
// the most proof of work (highest difficulty).  Since a lower target difficulty
// value equates to higher actual difficulty, the work value which will be
// accumulated must be the inverse of the difficulty.  Also, in order to avoid
// potential division by zero and really small floating point numbers, the
// result adds 1 to the denominator and multiplies the numerator by 2^256.
func CalcWork(bits uint32) *big.Int {
	return workmath.CalcWork(bits)
}

// CalcWorkInto calculates the work value for the given difficulty bits and
// stores it in the provided big.Int, returning it.  This avoids the allocation
// performed by CalcWork when the caller wants to recycle the work value.
func CalcWorkInto(bits uint32, result *big.Int) *big.Int {
	return workmath.CalcWorkInto(bits, result)
}

// calcEasiestDifficulty calculates the easiest possible difficulty that a block
// can have given starting difficulty bits and a duration.  It is mainly used to
// verify that claimed proof of work by a block is sane as compared to a
// known good checkpoint.
func (b *BlockChain) calcEasiestDifficulty(bits uint32, duration time.Duration) uint32 {
	// Convert types used in the calculations below.
	durationVal := int64(duration / time.Second)
	adjustmentFactor := big.NewInt(b.chainParams.RetargetAdjustmentFactor)

	// The test network rules allow minimum difficulty blocks after more
	// than twice the desired amount of time needed to generate a block has
	// elapsed.
	if b.chainParams.ReduceMinDifficulty {
		reductionTime := int64(b.chainParams.MinDiffReductionTime /
			time.Second)
		if durationVal > reductionTime {
			return b.chainParams.PowLimitBits
		}
	}

	// Since easier difficulty equates to higher numbers, the easiest
	// difficulty for a given duration is the largest value possible given
	// the number of retargets for the duration and starting difficulty
	// multiplied by the max adjustment factor.
	newTarget := CompactToBig(bits)
	for durationVal > 0 && newTarget.Cmp(b.chainParams.PowLimit) < 0 {
		newTarget.Mul(newTarget, adjustmentFactor)
		durationVal -= b.maxRetargetTimespan
	}

	// Limit new value to the proof of work limit.
	if newTarget.Cmp(b.chainParams.PowLimit) > 0 {
		newTarget.Set(b.chainParams.PowLimit)
	}

	return BigToCompact(newTarget)
}

// findPrevTestNetDifficulty returns the difficulty of the previous block which
// did not have the special testnet minimum difficulty rule applied.
func findPrevTestNetDifficulty(startNode HeaderCtx, c ChainCtx) uint32 {
	// Search backwards through the chain for the last block without
	// the special rule applied.
	iterNode := startNode
	for iterNode != nil && iterNode.Height()%c.BlocksPerRetarget() != 0 &&
		iterNode.Bits() == c.ChainParams().PowLimitBits {

		iterNode = iterNode.Parent()
	}

	// Return the found difficulty or the minimum difficulty if no
	// appropriate block was found.
	lastBits := c.ChainParams().PowLimitBits
	if iterNode != nil {
		lastBits = iterNode.Bits()
	}
	return lastBits
}

// calcNextRequiredDifficulty calculates the required difficulty for the block
// after the passed previous HeaderCtx based on the difficulty retarget rules.
// This function differs from the exported CalcNextRequiredDifficulty in that
// the exported version uses the current best chain as the previous HeaderCtx
// while this function accepts any block node. This function accepts a ChainCtx
// parameter that gives the necessary difficulty context variables.
//
// Sugarchain: Uses SugarShield-N510 algorithm (recalculates at every block)
func calcNextRequiredDifficulty(lastNode HeaderCtx, newBlockTime time.Time,
	c ChainCtx) (uint32, error) {

	// Emulate the same behavior as Bitcoin Core that for regtest there is
	// no difficulty retargeting.
	if c.ChainParams().PoWNoRetargeting {
		return c.ChainParams().PowLimitBits, nil
	}

	// Genesis block.
	if lastNode == nil {
		return c.ChainParams().PowLimitBits, nil
	}

	// Repair any parent links severed by the in-memory header window before
	// walking the ancestor chain below.  evictWindow cuts parent pointers at
	// the window boundary, so without this repair a valid block whose
	// ancestors fell out of the window would stop the walk early and be
	// falsely rejected with the PowLimit as the expected difficulty.  The
	// repair re-links severed ancestors from the authoritative cold index and
	// also keeps the median-time comparisons (CalcPastMedianTime below)
	// correct.  Only the real BlockChain owns the cold-read layer; other
	// ChainCtx implementations are left unchanged.
	if bc, ok := c.(*BlockChain); ok {
		if bn, ok := lastNode.(*blockNode); ok {
			bc.repairDifficultyChain(bn)
		}
	}

	// SugarShield-N510: Recalculate difficulty at every block
	// Collect the last 510 blocks' nBits
	var bnTot big.Int
	pindexFirst := lastNode
	for i := 0; pindexFirst != nil && i < SugarPowAveragingWindow; i++ {
		bnTmp := CompactToBig(pindexFirst.Bits())
		bnTot.Add(&bnTot, bnTmp)
		pindexFirst = pindexFirst.Parent()
	}

	// Check we have enough blocks (if not, use PoW limit)
	if pindexFirst == nil {
		return c.ChainParams().PowLimitBits, nil
	}

	// Calculate average target
	bnAvg := new(big.Int).Div(&bnTot, big.NewInt(int64(SugarPowAveragingWindow)))

	// Get median time past for last and first blocks
	lastMTP := CalcPastMedianTime(lastNode).Unix()
	firstMTP := CalcPastMedianTime(pindexFirst).Unix()

	// Calculate actual timespan
	nActualTimespan := lastMTP - firstMTP

	// Apply damping: only apply 25% of the deviation
	// new = 2550 + (actual - 2550) / 4
	nActualTimespan = SugarAveragingWindowTimespan() + (nActualTimespan-SugarAveragingWindowTimespan())/4

	// Clamp to allowed range
	if nActualTimespan < SugarMinActualTimespan() {
		nActualTimespan = SugarMinActualTimespan()
	}
	if nActualTimespan > SugarMaxActualTimespan() {
		nActualTimespan = SugarMaxActualTimespan()
	}

	// Calculate new target: bnNew = bnAvg / 2550 * nActualTimespan
	// (same operation order as C++ CalculateNextWorkRequired: divide, then multiply)
	newTarget := new(big.Int).Set(bnAvg)
	newTarget.Div(newTarget, big.NewInt(SugarAveragingWindowTimespan()))
	newTarget.Mul(newTarget, big.NewInt(nActualTimespan))

	// Limit to powLimit
	if newTarget.Cmp(c.ChainParams().PowLimit) > 0 {
		newTarget.Set(c.ChainParams().PowLimit)
	}

	newTargetBits := BigToCompact(newTarget)
	return newTargetBits, nil
}

// CalcNextRequiredDifficulty calculates the required difficulty for the block
// after the end of the current best chain based on the difficulty retarget
// rules.
//
// This function is safe for concurrent access.
func (b *BlockChain) CalcNextRequiredDifficulty(timestamp time.Time) (uint32, error) {
	b.chainLock.Lock()
	difficulty, err := calcNextRequiredDifficulty(b.bestChain.Tip(), timestamp, b)
	b.chainLock.Unlock()
	return difficulty, err
}
