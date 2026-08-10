package pow

import (
	"testing"

	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/yespower"
)

func BenchmarkYespower(b *testing.B) {
	input := make([]byte, 80)
	for i := 0; i < b.N; i++ {
		_ = yespower.Hash(input)
	}
}

func BenchmarkYespowerKnown(b *testing.B) {
	merkle, _ := chainhash.NewHashFromStr("7677ce2a579cb0411d1c9e6b1e9072b8f537f1e59cb387dacac2daac56e150b0")
	h := &headerBytes{merkle}
	_ = h
}

type headerBytes struct{ m *chainhash.Hash }
