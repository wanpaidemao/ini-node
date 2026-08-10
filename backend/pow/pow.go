package pow

import (
	"bytes"

	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/wire/v2"
	"github.com/btcsuite/btcd/yespower"
)

func BlockPoWHash(header *wire.BlockHeader) chainhash.Hash {
	var buf bytes.Buffer
	buf.Grow(80)
	header.BtcEncode(&buf, 0, wire.BaseEncoding)

	yespowerHash := yespower.Hash(buf.Bytes())

	var hash chainhash.Hash
	copy(hash[:], yespowerHash[:])
	return hash
}
