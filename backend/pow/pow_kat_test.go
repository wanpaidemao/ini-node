package pow

import (
	"testing"
	"time"

	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/wire/v2"
)

func TestTestnetGenesisPoW(t *testing.T) {
	merkle, err := chainhash.NewHashFromStr("7677ce2a579cb0411d1c9e6b1e9072b8f537f1e59cb387dacac2daac56e150b0")
	if err != nil {
		t.Fatal(err)
	}
	header := &wire.BlockHeader{
		Version:    1,
		PrevBlock:  chainhash.Hash{},
		MerkleRoot: *merkle,
		Timestamp:  time.Unix(1565913601, 0),
		Bits:       0x1f3fffff,
		Nonce:      490,
	}
	got := BlockPoWHash(header)
	want, err := chainhash.NewHashFromStr("0032f49a73e00fc182e08d5ede75c1418c7833092d663e43a5463c1dbd096f28")
	if err != nil {
		t.Fatal(err)
	}
	if got != *want {
		t.Fatalf("PoW mismatch: got %s want %s", got, want)
	}
}
