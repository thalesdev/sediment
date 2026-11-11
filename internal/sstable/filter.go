package sstable

import (
	"encoding/binary"

	"github.com/thalesdev/sediment/internal/bloom"
)

type filterWriter struct {
	bitsPerKey int

	keyBuf    []byte
	keyStarts []int
	keys      [][]byte

	buf     []byte
	offsets []uint32
}

func newFilterWriter(bitsPerKey int) *filterWriter {
	return &filterWriter{bitsPerKey: bitsPerKey}
}

// startBlock is called with the file offset of every data block before its keys are added.
func (fw *filterWriter) startBlock(blockOffset int64) {
	index := int(uint64(blockOffset) / filterBase)
	for index > len(fw.offsets) {
		fw.generate()
	}
}

func (fw *filterWriter) addKey(key []byte) {
	fw.keyStarts = append(fw.keyStarts, len(fw.keyBuf))
	fw.keyBuf = append(fw.keyBuf, key...)
}

// finish returns the filter block. The returned slice is owned by the filterWriter.
func (fw *filterWriter) finish() []byte {
	if len(fw.keyStarts) > 0 {
		fw.generate()
	}
	arrayOffset := uint32(len(fw.buf))
	for _, offset := range fw.offsets {
		fw.buf = binary.LittleEndian.AppendUint32(fw.buf, offset)
	}
	fw.buf = binary.LittleEndian.AppendUint32(fw.buf, arrayOffset)
	return append(fw.buf, filterBaseLg)
}

func (fw *filterWriter) generate() {
	fw.offsets = append(fw.offsets, uint32(len(fw.buf)))
	if len(fw.keyStarts) == 0 {
		return
	}

	fw.keys = fw.keys[:0]
	for i, start := range fw.keyStarts {
		end := len(fw.keyBuf)
		if i+1 < len(fw.keyStarts) {
			end = fw.keyStarts[i+1]
		}
		fw.keys = append(fw.keys, fw.keyBuf[start:end])
	}
	fw.buf = append(fw.buf, bloom.New(fw.keys, fw.bitsPerKey)...)

	fw.keyBuf = fw.keyBuf[:0]
	fw.keyStarts = fw.keyStarts[:0]
}
