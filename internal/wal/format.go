// SPDX-License-Identifier: MIT

package wal

import (
	"encoding/binary"
	"hash/crc32"
	"math/bits"
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

const (
	blockSize      = 32 * 1024
	headerSize     = 7
	maxPayloadSize = blockSize - headerSize
	maskDelta      = 0xa282ead8
)

// recordType tells how a fragment relates to its record. A record that fits in
// one block has one fragment of type full. A larger record has a first fragment,
// zero or more middle fragments, and a last fragment.
type recordType uint8

// Valid returns true if rt is full, first, middle or last.
func (rt recordType) Valid() bool {
	return rt >= recordTypeFull && rt <= recordTypeLast
}

const (
	recordTypeZero   recordType = 0
	recordTypeFull   recordType = 1
	recordTypeFirst  recordType = 2
	recordTypeMiddle recordType = 3
	recordTypeLast   recordType = 4
)

// header is the header of a fragment. It holds the masked checksum, the length
// of the payload and the record type.
type header struct {
	checksum   uint32
	length     uint16
	recordType recordType
}

// newHeader returns the header for a fragment that has payload data and type
// recordType. It panics if data is larger than maxPayloadSize or if recordType
// is not valid.
func newHeader(data []byte, recordType recordType) header {
	if len(data) > maxPayloadSize {
		panic("wal: data length exceeds max record size")
	}
	if !recordType.Valid() {
		panic("wal: invalid record type")
	}
	h := header{
		recordType: recordType,
	}
	h.checksum = h.computeChecksum(data)
	h.length = uint16(len(data))
	return h
}

// marshal writes the header to the first headerSize bytes of dst. It panics if
// dst is too short.
func (h *header) marshal(dst []byte) {
	if len(dst) < headerSize {
		panic("wal: destination buffer too small for header")
	}
	binary.LittleEndian.PutUint32(dst[0:4], h.checksum)
	binary.LittleEndian.PutUint16(dst[4:6], h.length)
	dst[6] = byte(h.recordType)
}

// unmarshal reads the header from the first headerSize bytes of data. It panics
// if data is too short.
func (h *header) unmarshal(data []byte) {
	if len(data) < headerSize {
		panic("wal: source buffer too small for header")
	}
	h.checksum = binary.LittleEndian.Uint32(data[0:4])
	h.length = binary.LittleEndian.Uint16(data[4:6])
	h.recordType = recordType(data[6])
}

// typeBytes holds one single-byte slice for each value of a record type. It lets
// computeChecksum read the type as a slice without an allocation.
var typeBytes = func() (t [256][1]byte) {
	for i := range t {
		t[i][0] = byte(i)
	}
	return t
}()

// computeChecksum returns the masked checksum of the record type and data.
func (h *header) computeChecksum(data []byte) uint32 {
	checksum := crc32.Update(0, crcTable, typeBytes[h.recordType][:])
	checksum = crc32.Update(checksum, crcTable, data)
	return maskChecksum(checksum)
}

// maskChecksum rotates checksum and adds maskDelta, as LevelDB does.
func maskChecksum(checksum uint32) uint32 {
	masked := bits.RotateLeft32(checksum, -15)
	masked += maskDelta
	return masked
}

// unmaskChecksum reverses maskChecksum.
func unmaskChecksum(masked uint32) uint32 {
	rotated := masked - maskDelta
	return bits.RotateLeft32(rotated, 15)
}
