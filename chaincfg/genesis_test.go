package chaincfg

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestGenesisCoinbaseHash(t *testing.T) {
	txHash := sugarGenesisCoinbaseTx.TxHash()
	fmt.Printf("Coinbase TxHash (display): %s\n", txHash.String())
	fmt.Printf("MerkleRoot     (display): %s\n", sugarGenesisMerkleRoot.String())
	fmt.Printf("Expected MerkleRoot:       7677ce2a579cb0411d1c9e6b1e9072b8f537f1e59cb387dacac2daac56e150b0\n")
	if txHash != sugarGenesisMerkleRoot {
		t.Errorf("Coinbase TxHash != MerkleRoot: got %x, want %x",
			txHash[:], sugarGenesisMerkleRoot[:])
	}
	if txHash.String() != "7677ce2a579cb0411d1c9e6b1e9072b8f537f1e59cb387dacac2daac56e150b0" {
		t.Errorf("Coinbase TxHash display mismatch: got %s, want %s",
			txHash.String(),
			"7677ce2a579cb0411d1c9e6b1e9072b8f537f1e59cb387dacac2daac56e150b0")
	}
}

func TestSerializeAndHash(t *testing.T) {
	var buf bytes.Buffer
	if err := sugarGenesisCoinbaseTx.SerializeNoWitness(&buf); err != nil {
		t.Fatalf("SerializeNoWitness: %v", err)
	}
	data := buf.Bytes()

	fmt.Printf("Tx serialized length: %d bytes\n", len(data))
	fmt.Printf("Tx serialized hex:    %x\n", data)

	// Direct SHA256d of the serialised bytes must equal txHash.
	h1 := sha256.Sum256(data)
	h2 := sha256.Sum256(h1[:])
	txHash := sugarGenesisCoinbaseTx.TxHash()
	if !bytes.Equal(h2[:], txHash[:]) {
		t.Errorf("manual SHA256d != TxHash(): got %x, want %x",
			h2[:], txHash[:])
	}
}

func TestGenesisBlockHash(t *testing.T) {
	blockHash := sugarGenesisBlock.Header.BlockHash()
	fmt.Printf("Block hash (display): %s\n", blockHash.String())
	fmt.Printf("Expected:             7d5eaec2dbb75f99feadfa524c78b7cabc1d8c8204f79d4f3a83381b811b0adc\n")
	if blockHash != sugarGenesisHash {
		t.Errorf("Block hash mismatch: got %x, want %x",
			blockHash[:], sugarGenesisHash[:])
	}
	if blockHash.String() != "7d5eaec2dbb75f99feadfa524c78b7cabc1d8c8204f79d4f3a83381b811b0adc" {
		t.Errorf("Block hash display mismatch: got %s, want %s",
			blockHash.String(),
			"7d5eaec2dbb75f99feadfa524c78b7cabc1d8c8204f79d4f3a83381b811b0adc")
	}
}

func TestTestnetBlockHash(t *testing.T) {
	h := sugarTestNetGenesisHash
	fmt.Printf("Testnet Block hash (display): %s\n", h.String())
	fmt.Printf("Expected:                    e0e0e42e493ba7b15f7b0fe1a7e66f73b7fd8b3e6e6a7b0e821a6b95040d3826\n")
	if h.String() != "e0e0e42e493ba7b15f7b0fe1a7e66f73b7fd8b3e6e6a7b0e821a6b95040d3826" {
		t.Errorf("Testnet hash display mismatch: got %s, want %s",
			h.String(),
			"e0e0e42e493ba7b15f7b0fe1a7e66f73b7fd8b3e6e6a7b0e821a6b95040d3826")
	}
}

func TestRegtestBlockHash(t *testing.T) {
	h := sugarRegTestGenesisHash
	fmt.Printf("Regtest Block hash (display): %s\n", h.String())
	fmt.Printf("Expected:                     223231facc4c2337baedba62921cf0ada7f867a869194ce9b3697eefd9d54c59\n")
	if h.String() != "223231facc4c2337baedba62921cf0ada7f867a869194ce9b3697eefd9d54c59" {
		t.Errorf("Regtest hash display mismatch: got %s, want %s",
			h.String(),
			"223231facc4c2337baedba62921cf0ada7f867a869194ce9b3697eefd9d54c59")
	}
}
