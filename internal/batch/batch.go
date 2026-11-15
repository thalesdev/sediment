// SPDX-License-Identifier: MIT

package batch

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/thalesdev/sediment/internal/ikey"
)

// HeaderSize is the size in bytes of the header of a batch.
const HeaderSize = 12

// Kind is the kind of an operation of a batch. A delete and a put have the same
// numbers as the kinds of an internal key, so a batch that has only these two is
// the LevelDB write batch. A range delete has a number that LevelDB does not
// know, and LevelDB refuses a batch that has one.
type Kind uint8

const (
	// KindDelete deletes a key.
	KindDelete Kind = Kind(ikey.KindDelete)

	// KindPut sets the value of a key.
	KindPut Kind = Kind(ikey.KindPut)

	// KindRangeDelete deletes every key in a range of user keys. The key of the
	// operation is the first key of the range, and the value is the end of the
	// range, which is not in it.
	KindRangeDelete Kind = 2
)

// ErrCorrupt is the base of all errors that Parse returns.
var ErrCorrupt = errors.New("batch: corrupt batch")

// Batch builds a batch. The zero value is ready for use and holds a batch with
// the sequence number 0 and no operation. A Batch keeps its buffer between
// calls to Reset. It is not safe for concurrent use.
type Batch struct {
	buf   []byte
	count uint32
}

// Reset clears the batch and sets the sequence number of its first operation.
func (b *Batch) Reset(seq uint64) {
	b.buf = binary.LittleEndian.AppendUint64(b.buf[:0], seq)
	b.buf = binary.LittleEndian.AppendUint32(b.buf, 0)
	b.count = 0
}

// Put adds an operation that sets the value of key.
func (b *Batch) Put(key, value []byte) {
	b.begin(KindPut, key)
	b.buf = binary.AppendUvarint(b.buf, uint64(len(value)))
	b.buf = append(b.buf, value...)
	b.end()
}

// DeleteRange adds an operation that deletes every key from start up to, and
// not including, end. A range that is empty or inverted is a valid batch for no
// reader, so the caller must not add one: Parse refuses it.
func (b *Batch) DeleteRange(start, end []byte) {
	b.begin(KindRangeDelete, start)
	b.buf = binary.AppendUvarint(b.buf, uint64(len(end)))
	b.buf = append(b.buf, end...)
	b.end()
}

// Delete adds an operation that deletes key.
func (b *Batch) Delete(key []byte) {
	b.begin(KindDelete, key)
	b.end()
}

// Count returns the number of operations in the batch.
func (b *Batch) Count() int {
	return int(b.count)
}

// Seq returns the sequence number of the first operation.
func (b *Batch) Seq() uint64 {
	if len(b.buf) < HeaderSize {
		return 0
	}
	return binary.LittleEndian.Uint64(b.buf)
}

// Bytes returns the encoded batch. The slice is valid until the next call to
// Reset, Put, Delete or DeleteRange, and the caller must not change it.
func (b *Batch) Bytes() []byte {
	if len(b.buf) < HeaderSize {
		b.Reset(0)
	}
	return b.buf
}

// begin starts an operation. It sets up the header if the batch has none.
func (b *Batch) begin(kind Kind, key []byte) {
	if len(b.buf) < HeaderSize {
		b.Reset(0)
	}
	b.buf = append(b.buf, byte(kind))
	b.buf = binary.AppendUvarint(b.buf, uint64(len(key)))
	b.buf = append(b.buf, key...)
}

// end counts the operation that begin started and updates the header.
func (b *Batch) end() {
	b.count++
	binary.LittleEndian.PutUint32(b.buf[8:], b.count)
}

// View is a batch that Parse checked. It borrows the bytes that Parse got.
type View struct {
	data  []byte
	seq   uint64
	count uint32
}

// Parse checks data and returns a View of it. It returns an error that wraps
// ErrCorrupt if the header is short, if the batch has no operation, if the
// sequence numbers go past ikey.MaxSeq, if an operation is not valid or is cut,
// if the number of operations is not the number in the header, if a range
// delete does not have its start below its end, or if bytes follow the last
// operation. Parse does not allocate memory from the count in
// the header.
func Parse(data []byte) (View, error) {
	if len(data) < HeaderSize {
		return View{}, fmt.Errorf("%w: %d bytes are shorter than the header", ErrCorrupt, len(data))
	}
	seq := binary.LittleEndian.Uint64(data)
	count := binary.LittleEndian.Uint32(data[8:])
	if count == 0 {
		return View{}, fmt.Errorf("%w: batch has no operation", ErrCorrupt)
	}
	if seq > ikey.MaxSeq || uint64(count)-1 > ikey.MaxSeq-seq {
		return View{}, fmt.Errorf("%w: sequence numbers from %d with %d operations go past the largest one", ErrCorrupt, seq, count)
	}
	pos := HeaderSize
	for i := range count {
		_, _, _, next, err := decodeOp(data, pos)
		if err != nil {
			return View{}, fmt.Errorf("operation %d: %w", i, err)
		}
		pos = next
	}
	if pos != len(data) {
		return View{}, fmt.Errorf("%w: %d bytes follow the last operation", ErrCorrupt, len(data)-pos)
	}
	return View{data: data, seq: seq, count: count}, nil
}

// Seq returns the sequence number of the first operation.
func (v View) Seq() uint64 {
	return v.seq
}

// Count returns the number of operations.
func (v View) Count() int {
	return int(v.count)
}

// ForEach calls fn for each operation in order. The operation number i has the
// sequence number Seq() + i. For a delete, value is nil. For a range delete, key
// is the start and value is the end of the range. The slices key and value
// borrow the bytes of the batch, and fn must not keep them.
func (v View) ForEach(fn func(seq uint64, kind Kind, key, value []byte)) {
	pos := HeaderSize
	for i := range v.count {
		kind, key, value, next, err := decodeOp(v.data, pos)
		if err != nil {
			return
		}
		fn(v.seq+uint64(i), kind, key, value)
		pos = next
	}
}

// decodeOp reads the operation at pos in data. It returns the position after
// the operation, or an error that wraps ErrCorrupt.
func decodeOp(data []byte, pos int) (kind Kind, key, value []byte, next int, err error) {
	if pos >= len(data) {
		return 0, nil, nil, 0, fmt.Errorf("%w: batch ends before the operation", ErrCorrupt)
	}
	kind = Kind(data[pos])
	if kind > KindRangeDelete {
		return 0, nil, nil, 0, fmt.Errorf("%w: kind %d is not valid", ErrCorrupt, kind)
	}
	key, pos, err = decodeBytes(data, pos+1)
	if err != nil {
		return 0, nil, nil, 0, err
	}
	if kind == KindPut || kind == KindRangeDelete {
		if value, pos, err = decodeBytes(data, pos); err != nil {
			return 0, nil, nil, 0, err
		}
	}
	if kind == KindRangeDelete && bytes.Compare(key, value) >= 0 {
		return 0, nil, nil, 0, fmt.Errorf("%w: a range delete does not have its start below its end", ErrCorrupt)
	}
	return kind, key, value, pos, nil
}

// decodeBytes reads a length as a varint and then that number of bytes.
func decodeBytes(data []byte, pos int) (b []byte, next int, err error) {
	length, n := binary.Uvarint(data[pos:])
	if n <= 0 {
		return nil, 0, fmt.Errorf("%w: length is not valid", ErrCorrupt)
	}
	pos += n
	if length > uint64(len(data)-pos) {
		return nil, 0, fmt.Errorf("%w: %d bytes do not fit in the %d bytes that are left", ErrCorrupt, length, len(data)-pos)
	}
	end := pos + int(length)
	return data[pos:end:end], end, nil
}
