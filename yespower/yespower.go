// Copyright (c) 2024 The Sugarchain developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
//
// Pure Go implementation of Yespower 1.0 for Sugarchain.
// Based on github.com/mraksoll4/yespower_go

package yespower

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"math/bits"

	"golang.org/x/crypto/pbkdf2"
)

const (
	// Yespower 1.0 parameters
	piTer     = 1
	pwxSimple = 2
	pwxGather = 4

	// Yespower 1.0 specific
	salsa20Rounds = 2
	pwxRounds     = 3
	sWidth        = 11

	// Derived values
	pwxBytes = pwxGather * pwxSimple * 8
	pwxWords = pwxBytes / 4

	// HashSize is the size of a Yespower hash in bytes (256 bits)
	HashSize = 32
)

// Sugarchain personalization string (matches umami block.cpp perslen 74)
const sugarPers = "Satoshi Nakamoto 31/Oct/2008 Proof-of-work is essentially one-CPU-one-vote"

type pwxformCtx struct {
	salsa20Rounds int
	pwxRounds     int
	w             int
	sWidth        int
	sBytes        int
	sMask         int
	S             []uint32
	s0, s1, s2    int
}

func newPwxformCtx() *pwxformCtx {
	ctx := &pwxformCtx{
		salsa20Rounds: salsa20Rounds,
		pwxRounds:     pwxRounds,
		sWidth:        sWidth,
	}
	ctx.sBytes = 3 * (1 << ctx.sWidth) * pwxSimple * 8
	ctx.sMask = ((1 << ctx.sWidth) - 1) * pwxSimple * 8
	ctx.S = make([]uint32, ctx.sBytes/4)
	ctx.s0 = 0
	ctx.s1 = ctx.s0 + (1<<ctx.sWidth)*pwxSimple*2
	ctx.s2 = ctx.s1 + (1<<ctx.sWidth)*pwxSimple*2
	ctx.w = 0
	return ctx
}

// Hash computes the Yespower 1.0 hash for Sugarchain.
// Parameters: N=2048, r=32, personalization="Satoshi Nakamoto 31/Oct/2008..."
func Hash(input []byte) [HashSize]byte {
	ctx := newPwxformCtx()

	// SHA-256 of input
	shaHash := sha256.Sum256(input)

	// PBKDF2 with personalization as source
	pBufSize := 128 * 32 // r=32
	buf := pbkdf2.Key(shaHash[:], []byte(sugarPers), piTer, pBufSize, sha256.New)

	// Convert to uint32 array
	BSize := len(buf) / 4
	B := make([]uint32, BSize)
	data := make([]byte, 128)
	for i := 0; i < BSize; i++ {
		B[i] = binary.LittleEndian.Uint32(buf[i*4:])
		if i < 128 {
			data[i] = buf[i]
		}
	}

	// Temporary storage
	vSize := 128 * 32 * 2048 / 4 // N=2048, r=32
	V := make([]uint32, vSize)
	xSize := 128 * 32 / 4
	X := make([]uint32, xSize)

	// Run SMix
	smix(B, 32, 2048, V, X, ctx)

	// Convert B back to bytes
	b := make([]byte, len(B)*4)
	for idx, val := range B {
		binary.LittleEndian.PutUint32(b[idx*4:], val)
	}

	// Final HMAC-SHA256
	h := hmac.New(sha256.New, b[len(b)-64:])
	h.Write(data[:32])

	var result [HashSize]byte
	copy(result[:], h.Sum(nil))
	return result
}

func smix(B []uint32, r, N int, V, X []uint32, ctx *pwxformCtx) {
	var nloopAll uint32 = uint32((N + 2) / 3)
	var nloopRw uint32 = nloopAll

	// Round up to even
	nloopAll++
	nloopAll &= 0xfffffffe

	// Round up to even
	nloopRw++
	nloopRw &= 0xfffffffe

	// Start mixing
	smix1(B, 1, ctx.sBytes/128, ctx.S, X, ctx, true)
	smix1(B, r, N, V, X, ctx, false)

	smix2(B, r, N, int(nloopRw), V, X, ctx)
	smix2(B, r, N, int(nloopAll-nloopRw), V, X, ctx)
}

func smix1(B []uint32, r, N int, V, X []uint32, ctx *pwxformCtx, init bool) {
	var start, stop int
	s := 32 * r

	for k := 0; k < 2*r; k++ {
		for i := 0; i < 16; i++ {
			X[k*16+i] = B[k*16+(i*5%16)]
		}
	}

	for k := 1; k < r; k++ {
		start = (k - 1) * 32
		stop = start + 32
		copy(X[k*32:], X[start:stop])
		blockmixPwxform(X[k*32:], ctx, 1)
	}

	for i := 0; i < N; i++ {
		copy(V[i*s:], X)

		if i > 1 {
			start = s * wrap(integerify(X, r), i)
			stop = start + s

			for j, val := range V[start:stop] {
				X[j] ^= val
			}
		}

		if init {
			blockmixSalsa(X, ctx.salsa20Rounds)
		} else {
			blockmixPwxform(X, ctx, r)
		}
	}

	for k := 0; k < 2*r; k++ {
		for i := 0; i < 16; i++ {
			B[k*16+(i*5%16)] = X[k*16+i]
		}
	}
}

func smix2(B []uint32, r, N, Nloop int, V, X []uint32, ctx *pwxformCtx) {
	s := 32 * r
	for k := 0; k < 2*r; k++ {
		for i := 0; i < 16; i++ {
			X[k*16+i] = B[k*16+(i*5%16)]
		}
	}

	for i := 0; i < Nloop; i++ {
		j := integerify(X, r) & (uint32(N) - 1)

		for k, x := range V[int(j)*s : (int(j)*s)+s] {
			X[k] ^= x
		}

		if Nloop != 2 {
			copy(V[int(j)*s:], X[:s])
		}

		blockmixPwxform(X, ctx, r)
	}

	for k := 0; k < 2*r; k++ {
		for i := 0; i < 16; i++ {
			B[k*16+(i*5%16)] = X[k*16+i]
		}
	}
}

func blockmixSalsa(B []uint32, rounds int) {
	X := make([]uint32, 16)
	copy(X, B[16:])

	for i := 0; i < 2; i++ {
		for j, val := range B[i*16 : i*16+16] {
			X[j] ^= val
		}

		salsaXOR(X, X, rounds)

		copy(B[i*16:], X)
	}
}

func blockmixPwxform(B []uint32, ctx *pwxformCtx, r int) {
	var start, stop int

	X := make([]uint32, pwxWords)

	r1 := 128 * r / pwxBytes

	start = (r1 - 1) * pwxWords
	stop = start + pwxWords
	copy(X, B[start:stop])

	for i := 0; i < r1; i++ {
		start = i * pwxWords
		stop = start + pwxWords
		if r1 > 1 {
			for j, val := range B[start:stop] {
				X[j] ^= val
			}
		}

		pwxform(X, ctx)

		copy(B[start:], X[:pwxWords])
	}

	i := (r1 - 1) * pwxBytes / 64
	salsaXOR(B[i*16:], B[i*16:], ctx.salsa20Rounds)
}

func pwxform(B []uint32, ctx *pwxformCtx) {
	w := ctx.w
	S0, S1, S2 := ctx.s0, ctx.s1, ctx.s2
	for i := 0; i < ctx.pwxRounds; i++ {
		for j := 0; j < pwxGather; j++ {
			xl := B[j*4]
			xh := B[j*4+1]

			p0 := uint32(S0) + 2*((xl&uint32(ctx.sMask))/8)
			p1 := uint32(S1) + 2*((xh&uint32(ctx.sMask))/8)

			for k := 0; k < pwxSimple; k++ {
				s0 := bits.RotateLeft64(uint64(ctx.S[int(p0)+(2*k)+1]), 32) + uint64(ctx.S[int(p0)+(2*k)])
				s1 := bits.RotateLeft64(uint64(ctx.S[int(p1)+(2*k)+1]), 32) + uint64(ctx.S[int(p1)+(2*k)])

				xl = B[j*4+k*2]
				xh = B[j*4+k*2+1]

				x := uint64(xl) * uint64(xh)
				x += s0
				x ^= s1

				B[j*4+k*2] = uint32(x)
				B[j*4+k*2+1] = uint32(x >> 32)
			}

			if i == 0 || j < (pwxGather/2) {
				if j&1 != 0 {
					for k := 0; k < pwxSimple; k++ {
						ctx.S[S1+w] = B[j*4+k*2]
						ctx.S[S1+w+1] = B[j*4+k*2+1]
						w += 2
					}
				} else {
					for k := 0; k < pwxSimple; k++ {
						ctx.S[S0+w+(2*k)] = B[j*4+k*2]
						ctx.S[S0+w+(2*k)+1] = B[j*4+k*2+1]
					}
				}
			}
		}
	}

	ctx.s0 = S2
	ctx.s1 = S0
	ctx.s2 = S1
	ctx.w = w & ((1<<(ctx.sWidth+1))*pwxSimple - 1)
}

func integerify(X []uint32, r int) uint32 {
	return X[(2*r-1)*16]
}

func wrap(x uint32, i int) int {
	n := i
	for y := n; y != 0; y = n & (n - 1) {
		n = y
	}
	return int(x&uint32(n-1)) + (i - n)
}

func salsaXOR(in, out []uint32, rounds int) {
	copy(out, in)

	x := make([]uint32, 16)

	/* SIMD unshuffle */
	for i := 0; i < 16; i++ {
		x[i*5%16] = in[i]
	}

	x0 := x[0]
	x1 := x[1]
	x2 := x[2]
	x3 := x[3]
	x4 := x[4]
	x5 := x[5]
	x6 := x[6]
	x7 := x[7]
	x8 := x[8]
	x9 := x[9]
	x10 := x[10]
	x11 := x[11]
	x12 := x[12]
	x13 := x[13]
	x14 := x[14]
	x15 := x[15]

	for i := 0; i < rounds; i += 2 {
		x4 ^= bits.RotateLeft32(x0+x12, 7)
		x8 ^= bits.RotateLeft32(x4+x0, 9)
		x12 ^= bits.RotateLeft32(x8+x4, 13)
		x0 ^= bits.RotateLeft32(x12+x8, 18)

		x9 ^= bits.RotateLeft32(x5+x1, 7)
		x13 ^= bits.RotateLeft32(x9+x5, 9)
		x1 ^= bits.RotateLeft32(x13+x9, 13)
		x5 ^= bits.RotateLeft32(x1+x13, 18)

		x14 ^= bits.RotateLeft32(x10+x6, 7)
		x2 ^= bits.RotateLeft32(x14+x10, 9)
		x6 ^= bits.RotateLeft32(x2+x14, 13)
		x10 ^= bits.RotateLeft32(x6+x2, 18)

		x3 ^= bits.RotateLeft32(x15+x11, 7)
		x7 ^= bits.RotateLeft32(x3+x15, 9)
		x11 ^= bits.RotateLeft32(x7+x3, 13)
		x15 ^= bits.RotateLeft32(x11+x7, 18)

		x1 ^= bits.RotateLeft32(x0+x3, 7)
		x2 ^= bits.RotateLeft32(x1+x0, 9)
		x3 ^= bits.RotateLeft32(x2+x1, 13)
		x0 ^= bits.RotateLeft32(x3+x2, 18)

		x6 ^= bits.RotateLeft32(x5+x4, 7)
		x7 ^= bits.RotateLeft32(x6+x5, 9)
		x4 ^= bits.RotateLeft32(x7+x6, 13)
		x5 ^= bits.RotateLeft32(x4+x7, 18)

		x11 ^= bits.RotateLeft32(x10+x9, 7)
		x8 ^= bits.RotateLeft32(x11+x10, 9)
		x9 ^= bits.RotateLeft32(x8+x11, 13)
		x10 ^= bits.RotateLeft32(x9+x8, 18)

		x12 ^= bits.RotateLeft32(x15+x14, 7)
		x13 ^= bits.RotateLeft32(x12+x15, 9)
		x14 ^= bits.RotateLeft32(x13+x12, 13)
		x15 ^= bits.RotateLeft32(x14+x13, 18)
	}

	x[0] = x0
	x[1] = x1
	x[2] = x2
	x[3] = x3
	x[4] = x4
	x[5] = x5
	x[6] = x6
	x[7] = x7
	x[8] = x8
	x[9] = x9
	x[10] = x10
	x[11] = x11
	x[12] = x12
	x[13] = x13
	x[14] = x14
	x[15] = x15

	/* SIMD shuffle */
	for i := 0; i < 16; i++ {
		out[i] += x[i*5%16]
	}
}
