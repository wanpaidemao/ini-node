package pow

import (
	"bytes"

	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/wire/v2"
	"github.com/btcsuite/btcd/yespower"
)

func BlockPoWHash(header *wire.BlockHeader) chainhash.Hash {
	// Serialize the 80-byte header into a stack buffer instead of a
	// heap-allocated bytes.Buffer: the miner calls this once per nonce,
	// so a per-call allocation would add constant GC pressure on the
	// hottest path in the codebase.  BtcEncode writes exactly 80 bytes,
	// so the stack array is never grown.
	// 把 80 字节头序列化进栈缓冲而不是堆分配的 bytes.Buffer:矿工每个
	// nonce 调用一次本函数,每次调用都分配会给最热路径带来持续 GC 压力。
	// BtcEncode 恰好写 80 字节,栈数组不会扩容。
	var buf [80]byte
	w := bytes.NewBuffer(buf[:0])
	header.BtcEncode(w, 0, wire.BaseEncoding)

	yespowerHash := yespower.Hash(buf[:])

	var hash chainhash.Hash
	copy(hash[:], yespowerHash[:])
	return hash
}
