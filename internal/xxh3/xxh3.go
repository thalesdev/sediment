// SPDX-License-Identifier: MIT

// Package xxh3 computes the 64-bit XXH3 hash of a byte string, with seed 0 and the
// default secret, which is the hash that the Redis DIGEST command returns. It is a
// port of the scalar code of xxHash 0.8, and the tests check it against the
// reference implementation.
package xxh3

import (
	"encoding/binary"
	"math/bits"
)

const (
	prime32x1 = 0x9E3779B1
	prime32x2 = 0x85EBCA77
	prime32x3 = 0xC2B2AE3D
	prime64x1 = 0x9E3779B185EBCA87
	prime64x2 = 0xC2B2AE3D27D4EB4F
	prime64x3 = 0x165667B19E3779F9
	prime64x4 = 0x85EBCA77C2B2AE63
	prime64x5 = 0x27D4EB2F165667C5
	primeMx1  = 0x165667919E3779F9
	primeMx2  = 0x9FB21C651E98DF25

	stripeLen       = 64
	secretConsume   = 8
	secretSizeMin   = 136
	midsizeStart    = 3
	midsizeLast     = 17
	secretMergeAccs = 11
	secretLastAcc   = 7
)

// secret is the default secret of XXH3.
var secret = [192]byte{
	0xb8, 0xfe, 0x6c, 0x39, 0x23, 0xa4, 0x4b, 0xbe, 0x7c, 0x01, 0x81, 0x2c, 0xf7, 0x21, 0xad, 0x1c,
	0xde, 0xd4, 0x6d, 0xe9, 0x83, 0x90, 0x97, 0xdb, 0x72, 0x40, 0xa4, 0xa4, 0xb7, 0xb3, 0x67, 0x1f,
	0xcb, 0x79, 0xe6, 0x4e, 0xcc, 0xc0, 0xe5, 0x78, 0x82, 0x5a, 0xd0, 0x7d, 0xcc, 0xff, 0x72, 0x21,
	0xb8, 0x08, 0x46, 0x74, 0xf7, 0x43, 0x24, 0x8e, 0xe0, 0x35, 0x90, 0xe6, 0x81, 0x3a, 0x26, 0x4c,
	0x3c, 0x28, 0x52, 0xbb, 0x91, 0xc3, 0x00, 0xcb, 0x88, 0xd0, 0x65, 0x8b, 0x1b, 0x53, 0x2e, 0xa3,
	0x71, 0x64, 0x48, 0x97, 0xa2, 0x0d, 0xf9, 0x4e, 0x38, 0x19, 0xef, 0x46, 0xa9, 0xde, 0xac, 0xd8,
	0xa8, 0xfa, 0x76, 0x3f, 0xe3, 0x9c, 0x34, 0x3f, 0xf9, 0xdc, 0xbb, 0xc7, 0xc7, 0x0b, 0x4f, 0x1d,
	0x8a, 0x51, 0xe0, 0x4b, 0xcd, 0xb4, 0x59, 0x31, 0xc8, 0x9f, 0x7e, 0xc9, 0xd9, 0x78, 0x73, 0x64,
	0xea, 0xc5, 0xac, 0x83, 0x34, 0xd3, 0xeb, 0xc3, 0xc5, 0x81, 0xa0, 0xff, 0xfa, 0x13, 0x63, 0xeb,
	0x17, 0x0d, 0xdd, 0x51, 0xb7, 0xf0, 0xda, 0x49, 0xd3, 0x16, 0x55, 0x26, 0x29, 0xd4, 0x68, 0x9e,
	0x2b, 0x16, 0xbe, 0x58, 0x7d, 0x47, 0xa1, 0xfc, 0x8f, 0xf8, 0xb8, 0xd1, 0x7a, 0xd0, 0x31, 0xce,
	0x45, 0xcb, 0x3a, 0x8f, 0x95, 0x16, 0x04, 0x28, 0xaf, 0xd7, 0xfb, 0xca, 0xbb, 0x4b, 0x40, 0x7e,
}

// le64 and le32 read a little endian integer from the start of b.
func le64(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }

// le32 reads a little endian 32-bit integer from the start of b.
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }

// mulFold multiplies a and b as 128-bit numbers and returns the xor of the two halves.
func mulFold(a, b uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	return hi ^ lo
}

// avalanche64 is the final mix of XXH64.
func avalanche64(h uint64) uint64 {
	h ^= h >> 33
	h *= prime64x2
	h ^= h >> 29
	h *= prime64x3
	h ^= h >> 32
	return h
}

// avalanche3 is the final mix of XXH3.
func avalanche3(h uint64) uint64 {
	h ^= h >> 37
	h *= primeMx1
	h ^= h >> 32
	return h
}

// rrmxmx is the final mix for inputs of 4 to 8 bytes.
func rrmxmx(h, length uint64) uint64 {
	h ^= bits.RotateLeft64(h, 49) ^ bits.RotateLeft64(h, 24)
	h *= primeMx2
	h ^= (h >> 35) + length
	h *= primeMx2
	return h ^ (h >> 28)
}

// mix16 mixes 16 bytes of input with 16 bytes of the secret.
func mix16(in, sec []byte) uint64 {
	return mulFold(le64(in)^le64(sec), le64(in[8:])^le64(sec[8:]))
}

// Sum64 returns the XXH3 64-bit hash of b.
func Sum64(b []byte) uint64 {
	n := len(b)
	switch {
	case n == 0:
		return avalanche64(le64(secret[56:]) ^ le64(secret[64:]))
	case n <= 3:
		c1, c2, c3 := uint32(b[0]), uint32(b[n>>1]), uint32(b[n-1])
		combined := c1<<16 | c2<<24 | c3 | uint32(n)<<8
		flip := uint64(le32(secret[:]) ^ le32(secret[4:]))
		return avalanche64(uint64(combined) ^ flip)
	case n <= 8:
		in1, in2 := le32(b), le32(b[n-4:])
		flip := le64(secret[8:]) ^ le64(secret[16:])
		return rrmxmx((uint64(in2)+uint64(in1)<<32)^flip, uint64(n))
	case n <= 16:
		flip1 := le64(secret[24:]) ^ le64(secret[32:])
		flip2 := le64(secret[40:]) ^ le64(secret[48:])
		lo := le64(b) ^ flip1
		hi := le64(b[n-8:]) ^ flip2
		return avalanche3(uint64(n) + bits.ReverseBytes64(lo) + hi + mulFold(lo, hi))
	case n <= 128:
		acc := uint64(n) * prime64x1
		if n > 32 {
			if n > 64 {
				if n > 96 {
					acc += mix16(b[48:], secret[96:])
					acc += mix16(b[n-64:], secret[112:])
				}
				acc += mix16(b[32:], secret[64:])
				acc += mix16(b[n-48:], secret[80:])
			}
			acc += mix16(b[16:], secret[32:])
			acc += mix16(b[n-32:], secret[48:])
		}
		acc += mix16(b, secret[:])
		acc += mix16(b[n-16:], secret[16:])
		return avalanche3(acc)
	case n <= 240:
		acc := uint64(n) * prime64x1
		rounds := n / 16
		for i := range 8 {
			acc += mix16(b[16*i:], secret[16*i:])
		}
		end := mix16(b[n-16:], secret[secretSizeMin-midsizeLast:])
		acc = avalanche3(acc)
		for i := 8; i < rounds; i++ {
			end += mix16(b[16*i:], secret[16*(i-8)+midsizeStart:])
		}
		return avalanche3(acc + end)
	}
	return long(b)
}

// accumulate mixes one stripe of 64 bytes of input into acc.
func accumulate(acc *[8]uint64, in, sec []byte) {
	for lane := range 8 {
		data := le64(in[lane*8:])
		key := data ^ le64(sec[lane*8:])
		acc[lane^1] += data
		acc[lane] += (key & 0xFFFFFFFF) * (key >> 32)
	}
}

// scramble mixes the accumulators after a block.
func scramble(acc *[8]uint64, sec []byte) {
	for lane := range 8 {
		a := acc[lane]
		a ^= a >> 47
		a ^= le64(sec[lane*8:])
		a *= prime32x1
		acc[lane] = a
	}
}

// long hashes an input of more than 240 bytes.
func long(b []byte) uint64 {
	acc := [8]uint64{prime32x3, prime64x1, prime64x2, prime64x3, prime64x4, prime32x2, prime64x5, prime32x1}
	sec := secret[:]
	stripesPerBlock := (len(sec) - stripeLen) / secretConsume
	blockLen := stripeLen * stripesPerBlock
	blocks := (len(b) - 1) / blockLen
	for n := range blocks {
		block := b[n*blockLen:]
		for s := range stripesPerBlock {
			accumulate(&acc, block[s*stripeLen:], sec[s*secretConsume:])
		}
		scramble(&acc, sec[len(sec)-stripeLen:])
	}
	rest := b[blocks*blockLen:]
	stripes := (len(b) - 1 - blockLen*blocks) / stripeLen
	for s := range stripes {
		accumulate(&acc, rest[s*stripeLen:], sec[s*secretConsume:])
	}
	accumulate(&acc, b[len(b)-stripeLen:], sec[len(sec)-stripeLen-secretLastAcc:])
	result := uint64(len(b)) * prime64x1
	ms := sec[secretMergeAccs:]
	for i := range 4 {
		result += mulFold(acc[2*i]^le64(ms[16*i:]), acc[2*i+1]^le64(ms[16*i+8:]))
	}
	return avalanche3(result)
}
