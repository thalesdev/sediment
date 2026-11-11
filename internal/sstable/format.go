// SPDX-License-Identifier: MIT

package sstable

import (
	"encoding/binary"
	"hash/crc32"
	"math/bits"
)

const (
	blockTrailerSize = 5
	footerSize       = 48
	maxHandleSize    = 2 * binary.MaxVarintLen64

	tableMagic uint64 = 0xdb4775248b80fb57

	blockTypeNoCompression byte = 0

	filterBaseLg = 11
	filterBase   = 1 << filterBaseLg

	filterMetaKey = "filter.leveldb.BuiltinBloomFilter"

	maskDelta = 0xa282ead8
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// maskChecksum rotates c and adds maskDelta, as LevelDB does. A stored checksum
// is always masked.
func maskChecksum(c uint32) uint32 {
	return bits.RotateLeft32(c, -15) + maskDelta
}

// blockChecksum returns the masked CRC-32C of the block contents followed by the
// block type.
func blockChecksum(block []byte, blockType byte) uint32 {
	c := crc32.Update(0, crcTable, block)
	c = crc32.Update(c, crcTable, []byte{blockType})
	return maskChecksum(c)
}

// blockHandle gives the position of a block in the file: the offset of its first
// byte and its size. The size does not include the trailer.
type blockHandle struct {
	offset uint64
	size   uint64
}

// append appends the varint encoding of h to dst and returns the result.
func (h blockHandle) append(dst []byte) []byte {
	dst = binary.AppendUvarint(dst, h.offset)
	return binary.AppendUvarint(dst, h.size)
}

// decodeBlockHandle returns the handle and the number of bytes that it read. If
// src is not valid, n is 0 or less.
func decodeBlockHandle(src []byte) (blockHandle, int) {
	offset, n := binary.Uvarint(src)
	if n <= 0 {
		return blockHandle{}, 0
	}
	size, m := binary.Uvarint(src[n:])
	if m <= 0 {
		return blockHandle{}, 0
	}
	return blockHandle{offset: offset, size: size}, n + m
}

// appendFooter appends the footer to dst and returns the result. The footer
// holds the handles of the metaindex block and the index block, zero padding,
// and the magic number.
func appendFooter(dst []byte, metaindex, index blockHandle) []byte {
	start := len(dst)
	dst = metaindex.append(dst)
	dst = index.append(dst)
	for len(dst)-start < footerSize-8 {
		dst = append(dst, 0)
	}
	return binary.LittleEndian.AppendUint64(dst, tableMagic)
}
