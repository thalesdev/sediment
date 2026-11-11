// SPDX-License-Identifier: MIT

package sstable

import (
	"encoding/binary"
	"math/bits"
)

// blockWriter builds one block. It stores each key without the prefix that it
// shares with the previous key. After every interval keys, it stores the full
// key and adds a restart point.
type blockWriter struct {
	buf      []byte
	lastKey  []byte
	counter  int
	restarts []uint32
	interval int
}

// newBlockWriter returns a blockWriter that adds a restart point after every
// interval keys.
func newBlockWriter(interval int) *blockWriter {
	return &blockWriter{
		interval: interval,
		restarts: []uint32{0},
	}
}

// add appends a key-value pair. The caller must add keys in increasing order.
func (bw *blockWriter) add(key, value []byte) {
	var shared int
	if bw.counter == bw.interval {
		bw.counter = 0
		bw.restarts = append(bw.restarts, uint32(len(bw.buf)))
	} else {
		shared = bw.commonPrefixLength(key)
	}
	bw.counter++
	bw.lastKey = append(bw.lastKey[:shared], key[shared:]...)
	bw.buf = binary.AppendUvarint(bw.buf, uint64(shared))
	bw.buf = binary.AppendUvarint(bw.buf, uint64(len(key)-shared))
	bw.buf = binary.AppendUvarint(bw.buf, uint64(len(value)))
	bw.buf = append(bw.buf, key[shared:]...)
	bw.buf = append(bw.buf, value...)
}

// finish appends the restart array to the block and returns the block.
// The returned slice is valid only until the next call to reset.
func (bw *blockWriter) finish() []byte {
	for _, offset := range bw.restarts {
		bw.buf = binary.LittleEndian.AppendUint32(bw.buf, offset)
	}
	bw.buf = binary.LittleEndian.AppendUint32(bw.buf, uint32(len(bw.restarts)))
	return bw.buf
}

// estimatedSize returns the size in bytes that the block has after finish.
func (bw *blockWriter) estimatedSize() int {
	return len(bw.buf) + 4*len(bw.restarts) + 4
}

// empty returns true if no entry was added since the last reset.
func (bw *blockWriter) empty() bool {
	return len(bw.buf) == 0
}

// reset clears the blockWriter so that it can build a new block.
func (bw *blockWriter) reset() {
	bw.buf = bw.buf[:0]
	bw.lastKey = bw.lastKey[:0]
	bw.restarts = append(bw.restarts[:0], 0)
	bw.counter = 0
}

// commonPrefixLength returns the length of the prefix that key shares with the
// last key that was added.
func (bw *blockWriter) commonPrefixLength(key []byte) int {
	maxSize := min(len(bw.lastKey), len(key))
	var counter int

	for counter+8 <= maxSize {
		b1 := binary.LittleEndian.Uint64(key[counter:])
		b2 := binary.LittleEndian.Uint64(bw.lastKey[counter:])
		if b1 != b2 {
			return counter + (bits.TrailingZeros64(b1^b2) >> 3)
		}
		counter += 8
	}

	for counter < maxSize && key[counter] == bw.lastKey[counter] {
		counter++
	}
	return counter
}
