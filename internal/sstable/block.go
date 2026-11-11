package sstable

import (
	"encoding/binary"
	"math/bits"
)

type blockWriter struct {
	buf      []byte
	lastKey  []byte
	counter  int
	restarts []uint32
	interval int
}

func newBlockWriter(interval int) *blockWriter {
	return &blockWriter{
		interval: interval,
		restarts: []uint32{0},
	}
}

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

// finish appends the restart array and returns the block.
// The returned slice is only valid until the next reset.
func (bw *blockWriter) finish() []byte {
	for _, offset := range bw.restarts {
		bw.buf = binary.LittleEndian.AppendUint32(bw.buf, offset)
	}
	bw.buf = binary.LittleEndian.AppendUint32(bw.buf, uint32(len(bw.restarts)))
	return bw.buf
}

func (bw *blockWriter) estimatedSize() int {
	return len(bw.buf) + 4*len(bw.restarts) + 4
}

func (bw *blockWriter) empty() bool {
	return len(bw.buf) == 0
}

func (bw *blockWriter) reset() {
	bw.buf = bw.buf[:0]
	bw.lastKey = bw.lastKey[:0]
	bw.restarts = append(bw.restarts[:0], 0)
	bw.counter = 0
}

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
