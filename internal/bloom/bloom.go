// SPDX-License-Identifier: MIT

package bloom

import (
	"encoding/binary"
	"math/bits"
)

const (
	DefaultBitsPerKey = 10

	hashSeed = 0xbc9f1d34
	hashMul  = 0xc6a4a793

	minBits = 64
	maxK    = 30
)

// Filter is a bloom filter in the LevelDB format. The last byte is the number of
// hash functions. The other bytes are the bit array.
type Filter []byte

// k returns the number of hash functions for bitsPerKey. The result is from 1 to
// maxK.
func k(bitsPerKey int) int {
	return min(max(bitsPerKey*69/100, 1), maxK)
}

// New returns a Filter that holds keys. bitsPerKey sets the size of the filter.
// If bitsPerKey is less than 1, New uses DefaultBitsPerKey. The filter has at
// least 64 bits.
func New(keys [][]byte, bitsPerKey int) Filter {
	if bitsPerKey < 1 {
		bitsPerKey = DefaultBitsPerKey
	}
	nBits := max(len(keys)*bitsPerKey, minBits)
	nBytes := (nBits + 7) / 8
	nBits = nBytes * 8

	f := make(Filter, nBytes+1)
	numHashes := k(bitsPerKey)
	f[nBytes] = byte(numHashes)

	for _, key := range keys {
		h := hash(key)
		delta := bits.RotateLeft32(h, -17)
		for range numHashes {
			pos := h % uint32(nBits)
			f[pos/8] |= 1 << (pos % 8)
			h += delta
		}
	}
	return f
}

// MayContain returns false if key is not in the filter. It returns true if key
// can be in the filter. A true result can be wrong. A false result is always
// correct. A filter that is shorter than 2 bytes contains no key.
func (f Filter) MayContain(key []byte) bool {
	if len(f) < 2 {
		return false
	}
	numHashes := int(f[len(f)-1])
	if numHashes > maxK {
		return true
	}
	nBits := uint32(len(f)-1) * 8

	h := hash(key)
	delta := bits.RotateLeft32(h, -17)
	for range numHashes {
		pos := h % nBits
		if f[pos/8]&(1<<(pos%8)) == 0 {
			return false
		}
		h += delta
	}
	return true
}

// hash returns the 32-bit hash of data. It is the hash function of LevelDB.
func hash(data []byte) uint32 {
	h := uint32(hashSeed) ^ uint32(len(data))*hashMul

	for len(data) >= 4 {
		h += binary.LittleEndian.Uint32(data)
		h *= hashMul
		h ^= h >> 16
		data = data[4:]
	}

	switch len(data) {
	case 3:
		h += uint32(data[2]) << 16
		fallthrough
	case 2:
		h += uint32(data[1]) << 8
		fallthrough
	case 1:
		h += uint32(data[0])
		h *= hashMul
		h ^= h >> 24
	}
	return h
}
