// SPDX-License-Identifier: MIT

package sstable

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/thalesdev/sediment/internal/bloom"
)

var errClosed = errors.New("sstable: iterator closed")

// Reader reads a table through an io.ReaderAt. Open loads the index and the
// filter. The Reader reads each data block when it is needed, and checks the
// checksum of the block.
// A Reader is safe for concurrent use. It does not close the io.ReaderAt.
type Reader struct {
	src  io.ReaderAt
	size int64
	opts Options

	index  []byte
	filter []byte

	indexIter blockIter
}

// Open reads the footer, the index and the filter of the table in the first size
// bytes of src.
// Open uses only the Compare and FilterKey fields of opts. They must be the same
// as the values that the writer used.
func Open(src io.ReaderAt, size int64, opts Options) (*Reader, error) {
	if size < footerSize {
		return nil, corruptf("table of %d bytes is smaller than the footer", size)
	}
	r := &Reader{src: src, size: size, opts: opts.withDefaults()}

	var footer [footerSize]byte
	if _, err := src.ReadAt(footer[:], size-footerSize); err != nil {
		return nil, fmt.Errorf("sstable: reading footer: %w", err)
	}
	if binary.LittleEndian.Uint64(footer[footerSize-8:]) != tableMagic {
		return nil, corruptf("bad magic number")
	}
	metaHandle, n := decodeBlockHandle(footer[:])
	if n <= 0 {
		return nil, corruptf("malformed metaindex handle")
	}
	indexHandle, m := decodeBlockHandle(footer[n:])
	if m <= 0 {
		return nil, corruptf("malformed index handle")
	}

	var err error
	if r.index, err = r.readBlock(indexHandle); err != nil {
		return nil, fmt.Errorf("sstable: reading index: %w", err)
	}
	indexIter, err := newBlockIter(r.index, r.opts.Compare)
	if err != nil {
		return nil, fmt.Errorf("sstable: index: %w", err)
	}
	r.indexIter = *indexIter
	if err := r.loadFilter(metaHandle); err != nil {
		return nil, err
	}
	return r, nil
}

// newIndexIter returns a new iterator over the index block. Open checked the
// restart points of the index one time, so this does not check them again. The
// check goes through all the restart points, and an index can have tens of
// thousands of them.
func (r *Reader) newIndexIter() *blockIter {
	it := r.indexIter
	return &it
}

// loadFilter reads the metaindex block at metaHandle, and then the filter block
// that the metaindex names. If the table has no filter, r.filter stays nil.
func (r *Reader) loadFilter(metaHandle blockHandle) error {
	meta, err := r.readBlock(metaHandle)
	if err != nil {
		return fmt.Errorf("sstable: reading metaindex: %w", err)
	}
	it, err := newBlockIter(meta, bytes.Compare)
	if err != nil {
		return fmt.Errorf("sstable: metaindex: %w", err)
	}
	it.Seek([]byte(filterMetaKey))
	if it.Err() != nil {
		return fmt.Errorf("sstable: metaindex: %w", it.Err())
	}
	if !it.Valid() || string(it.Key()) != filterMetaKey {
		return nil
	}
	h, err := decodeExactHandle(it.Value())
	if err != nil {
		return err
	}
	fb, err := r.readBlock(h)
	if err != nil {
		return fmt.Errorf("sstable: reading filter: %w", err)
	}
	usable, err := checkFilter(fb)
	if err != nil {
		return fmt.Errorf("sstable: filter: %w", err)
	}
	if usable {
		r.filter = fb
	}
	return nil
}

// decodeExactHandle decodes a block handle that must use all the bytes of src.
func decodeExactHandle(src []byte) (blockHandle, error) {
	h, n := decodeBlockHandle(src)
	if n <= 0 || n != len(src) {
		return blockHandle{}, corruptf("malformed block handle")
	}
	return h, nil
}

// checkFilter checks the structure of the filter block fb. It returns an error
// that wraps errCorrupt if the offset array is not valid. It returns false, with
// no error, if the filter uses a base other than filterBaseLg, because the reader
// cannot map data blocks to the filters of such a block.
func checkFilter(fb []byte) (usable bool, err error) {
	if len(fb) < 5 {
		return false, corruptf("filter block of %d bytes is too small", len(fb))
	}
	arrayOffset := uint64(binary.LittleEndian.Uint32(fb[len(fb)-5:]))
	if arrayOffset > uint64(len(fb)-5) || (uint64(len(fb)-5)-arrayOffset)%4 != 0 {
		return false, corruptf("filter offset array at %d does not fit in %d bytes", arrayOffset, len(fb))
	}
	previous := uint32(0)
	for pos := int(arrayOffset); pos < len(fb)-5; pos += 4 {
		offset := binary.LittleEndian.Uint32(fb[pos:])
		if offset < previous || uint64(offset) > arrayOffset {
			return false, corruptf("filter offset %d is not valid", offset)
		}
		previous = offset
	}
	return fb[len(fb)-1] == filterBaseLg, nil
}

// readBlock reads the block at h and checks its type and checksum. The caller owns
// the returned slice.
func (r *Reader) readBlock(h blockHandle) ([]byte, error) {
	if h.size > uint64(r.size) || h.offset > uint64(r.size)-h.size ||
		h.offset+h.size > uint64(r.size)-blockTrailerSize {
		return nil, corruptf("block %+v is outside of the %d byte table", h, r.size)
	}
	buf := make([]byte, h.size+blockTrailerSize)
	if n, err := r.src.ReadAt(buf, int64(h.offset)); n < len(buf) {
		if err == nil || err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("reading block %+v: %w", h, err)
	}
	block, trailer := buf[:h.size], buf[h.size:]
	if got, want := binary.LittleEndian.Uint32(trailer[1:]), blockChecksum(block, trailer[0]); got != want {
		return nil, corruptf("block %+v has checksum %#08x, want %#08x", h, got, want)
	}
	if trailer[0] != blockTypeNoCompression {
		return nil, corruptf("block %+v uses compression type %d, which is not supported", h, trailer[0])
	}
	return block, nil
}

// mayContain uses the filter of the data block at blockOffset. It returns true if
// the table has no usable filter, or if the filter does not cover the block, so
// that the caller reads the block. Open checks the structure of the filter.
func (r *Reader) mayContain(blockOffset uint64, key []byte) bool {
	fb := r.filter
	if fb == nil {
		return true
	}
	arrayOffset := int(binary.LittleEndian.Uint32(fb[len(fb)-5:]))
	num := (len(fb) - 5 - arrayOffset) / 4
	idx := blockOffset >> filterBaseLg
	if idx >= uint64(num) {
		return true
	}
	start := int(binary.LittleEndian.Uint32(fb[arrayOffset+4*int(idx):]))
	limit := arrayOffset
	if int(idx)+1 < num {
		limit = int(binary.LittleEndian.Uint32(fb[arrayOffset+4*(int(idx)+1):]))
	}
	return bloom.Filter(fb[start:limit]).MayContain(key)
}

// Find returns the first entry with a key that is greater than or equal to key,
// in the block that can hold key. It reads one data block at most. It reads no
// data block if the filter, which uses the FilterKey of key, shows that the
// table has no entry for it. It does not need an entry with a key equal to key,
// so it can find the newest version of a user key from a lookup key. The caller
// must compare the key that it gets with the key that it wants.
//
// If the block has no such entry, found is false. This is correct also when the
// index holds a separator that is greater than the last key of the block,
// because a separator is less than the first key of the next block.
//
// The slices foundKey and value are borrowed. They are valid only until the
// next call of the caller to the Reader, and the caller must not change them. To
// keep them, the caller must copy them.
func (r *Reader) Find(key []byte) (foundKey, value []byte, found bool, err error) {
	index := r.newIndexIter()
	index.Seek(key)
	if !index.Valid() {
		return nil, nil, false, index.Err()
	}
	h, err := decodeExactHandle(index.Value())
	if err != nil {
		return nil, nil, false, err
	}
	if !r.mayContain(h.offset, r.opts.FilterKey(key)) {
		return nil, nil, false, nil
	}
	block, err := r.readBlock(h)
	if err != nil {
		return nil, nil, false, err
	}
	data, err := newBlockIter(block, r.opts.Compare)
	if err != nil {
		return nil, nil, false, err
	}
	data.Seek(key)
	if !data.Valid() {
		return nil, nil, false, data.Err()
	}
	return data.Key(), data.Value(), true, nil
}

// Get returns a copy of the value for key, and reports if the key exists. It
// uses Find, so it reads one data block at most, and none if the filter shows
// that the key is not in the table. The key of the entry must be equal to key.
func (r *Reader) Get(key []byte) (value []byte, found bool, err error) {
	foundKey, v, found, err := r.Find(key)
	if err != nil || !found || r.opts.Compare(foundKey, key) != 0 {
		return nil, false, err
	}
	return bytes.Clone(v), true, nil
}

// Iterator iterates over all entries of a table in key order. It has the same
// methods and rules as shale.Iterator. It is not safe for concurrent use.
type Iterator struct {
	r      *Reader
	index  *blockIter
	data   *blockIter
	err    error
	closed bool
}

// NewIterator returns an iterator over the table. The iterator has no position
// until the caller calls SeekToFirst or Seek.
func (r *Reader) NewIterator() *Iterator {
	it := &Iterator{r: r}
	it.index = r.newIndexIter()
	return it
}

// SeekToFirst moves the iterator to the first entry.
func (it *Iterator) SeekToFirst() {
	if !it.usable() {
		return
	}
	it.index.SeekToFirst()
	it.openBlock()
	if it.data != nil {
		it.data.SeekToFirst()
	}
	it.skipEmptyForward()
}

// Seek moves the iterator to the first entry with a key that is greater than or
// equal to key. If there is no such entry, Valid returns false.
func (it *Iterator) Seek(key []byte) {
	if !it.usable() {
		return
	}
	it.index.Seek(key)
	it.openBlock()
	if it.data != nil {
		it.data.Seek(key)
	}
	it.skipEmptyForward()
}

// Next moves the iterator to the next entry. If there is no next entry, Valid
// returns false.
func (it *Iterator) Next() {
	if !it.Valid() {
		return
	}
	it.data.Next()
	it.skipEmptyForward()
}

// Valid returns true if the iterator is at an entry.
func (it *Iterator) Valid() bool { return it.err == nil && it.data != nil && it.data.Valid() }

// Key returns the key of the current entry. The slice is valid only until the
// next move of the iterator. The caller must not change it.
func (it *Iterator) Key() []byte { return it.data.Key() }

// Value returns the value of the current entry. The slice is valid only until
// the next move of the iterator. The caller must not change it.
func (it *Iterator) Value() []byte { return it.data.Value() }

// Err returns the error that stopped the iteration, or nil.
func (it *Iterator) Err() error { return it.err }

// Close releases the iterator. It is safe to call Close more than one time.
// After Close, the iterator is not valid and Err returns an error.
func (it *Iterator) Close() error {
	if it.closed {
		return nil
	}
	it.closed = true
	it.data, it.index = nil, nil
	return nil
}

// usable returns true if the iterator can be used. If the iterator is closed, it
// stores an error.
func (it *Iterator) usable() bool {
	if it.closed {
		it.err = errors.Join(it.err, errClosed)
	}
	return it.err == nil
}

// openBlock reads the data block at the index position. If the index has no more
// entries or has an error, it sets the data iterator to nil.
func (it *Iterator) openBlock() {
	it.data = nil
	if err := it.index.Err(); err != nil {
		it.err = err
		return
	}
	if !it.index.Valid() {
		return
	}
	h, err := decodeExactHandle(it.index.Value())
	if err != nil {
		it.err = err
		return
	}
	block, err := it.r.readBlock(h)
	if err != nil {
		it.err = err
		return
	}
	if it.data, err = newBlockIter(block, it.r.opts.Compare); err != nil {
		it.data, it.err = nil, err
	}
}

// skipEmptyForward moves to the next data block while the current data block has
// no more entries. It stops at the first error.
func (it *Iterator) skipEmptyForward() {
	for it.err == nil && it.data != nil && !it.data.Valid() {
		if err := it.data.Err(); err != nil {
			it.err = err
			return
		}
		it.index.Next()
		it.openBlock()
		if it.data != nil {
			it.data.SeekToFirst()
		}
	}
}
