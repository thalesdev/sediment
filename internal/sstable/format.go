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

func maskChecksum(c uint32) uint32 {
	return bits.RotateLeft32(c, -15) + maskDelta
}

// blockChecksum is the masked CRC-32C of the block contents followed by the block type.
func blockChecksum(block []byte, blockType byte) uint32 {
	c := crc32.Update(0, crcTable, block)
	c = crc32.Update(c, crcTable, []byte{blockType})
	return maskChecksum(c)
}

type blockHandle struct {
	offset uint64
	size   uint64
}

func (h blockHandle) append(dst []byte) []byte {
	dst = binary.AppendUvarint(dst, h.offset)
	return binary.AppendUvarint(dst, h.size)
}

// decodeBlockHandle returns the handle and the number of bytes read, or n <= 0 on malformed input.
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

func appendFooter(dst []byte, metaindex, index blockHandle) []byte {
	start := len(dst)
	dst = metaindex.append(dst)
	dst = index.append(dst)
	for len(dst)-start < footerSize-8 {
		dst = append(dst, 0)
	}
	return binary.LittleEndian.AppendUint64(dst, tableMagic)
}
