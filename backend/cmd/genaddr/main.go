package main

import (
	"crypto/sha256"
	"fmt"

	"golang.org/x/crypto/ripemd160"

	"github.com/btcsuite/btcd/address/v2"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/chaincfg/v2"
)

func hash160(data []byte) []byte {
	sha := sha256.Sum256(data)
	h := ripemd160.New()
	h.Write(sha[:])
	return h.Sum(nil)
}

func main() {
	privKey, err := btcec.NewPrivateKey()
	if err != nil {
		panic(err)
	}
	pubKey := privKey.PubKey()
	h160 := hash160(pubKey.SerializeCompressed())
	addr, err := address.NewAddressPubKeyHash(h160, &chaincfg.RegressionNetParams)
	if err != nil {
		panic(err)
	}
	fmt.Println(addr.EncodeAddress())
}
