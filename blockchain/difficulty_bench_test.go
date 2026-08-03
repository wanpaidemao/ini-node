package blockchain

import (
	"math/big"
	"testing"
)

// BenchmarkCompactToBig measures the cost of the CompactToBig used in the
// SugarShield DAA window walk.
func BenchmarkCompactToBig(b *testing.B) {
	bits := uint32(0x1f3fffff)
	for i := 0; i < b.N; i++ {
		_ = CompactToBig(bits)
	}
}

// BenchmarkDAASum simulates the 510-block window sum.
func BenchmarkDAASum(b *testing.B) {
	bits := uint32(0x1f3fffff)
	var tot big.Int
	for i := 0; i < b.N; i++ {
		tot.SetInt64(0)
		for j := 0; j < 510; j++ {
			tot.Add(&tot, CompactToBig(bits))
		}
	}
}
