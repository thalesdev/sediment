// SPDX-License-Identifier: MIT

package sstable

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// errCorrupt is the base of all errors that the reader returns for invalid table
// bytes.
var errCorrupt = errors.New("sstable: corrupt table")

// corruptf returns an error that wraps errCorrupt. It formats the message as
// fmt.Sprintf does.
func corruptf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errCorrupt, fmt.Sprintf(format, args...))
}

// blockIter iterates over the entries of one block. It has the same methods and
// rules as shale.Iterator.
type blockIter struct {
	cmp func(a, b []byte) int

	data     []byte
	restarts []byte
	n        int

	pos   int
	ri    int
	key   []byte
	value []byte
	valid bool
	err   error
}

// newBlockIter returns an iterator over block. cmp orders the keys. It returns
// an error that wraps errCorrupt if the restart array of block is not valid.
func newBlockIter(block []byte, cmp func(a, b []byte) int) (*blockIter, error) {
	if len(block) < 4 {
		return nil, corruptf("block of %d bytes has no restart count", len(block))
	}
	count := uint64(binary.LittleEndian.Uint32(block[len(block)-4:]))
	if count == 0 || count > uint64(len(block)-4)/4 {
		return nil, corruptf("invalid restart count %d for block of %d bytes", count, len(block))
	}
	n := int(count)
	start := len(block) - 4 - 4*n
	it := &blockIter{
		cmp:      cmp,
		data:     block[:start],
		restarts: block[start : len(block)-4],
		n:        n,
	}
	previous := -1
	for i := range n {
		off := it.restart(i)
		if off <= previous || off >= max(len(it.data), 1) || i == 0 && off != 0 {
			return nil, corruptf("restart %d points at %d, which is not valid for %d bytes of entries", i, off, len(it.data))
		}
		previous = off
	}
	return it, nil
}

// restart returns the offset of restart point i.
func (it *blockIter) restart(i int) int {
	return int(binary.LittleEndian.Uint32(it.restarts[4*i:]))
}

// SeekToFirst moves the iterator to the first entry of the block.
func (it *blockIter) SeekToFirst() {
	it.seekToRestart(0)
	it.Next()
}

// Seek moves the iterator to the first entry with a key that is greater than or
// equal to key.
func (it *blockIter) Seek(key []byte) {
	lo, hi := 0, it.n
	for lo < hi && len(it.data) > 0 {
		mid := int(uint(lo+hi) >> 1)
		k, err := it.restartKey(mid)
		if err != nil {
			it.fail(err)
			return
		}
		if it.cmp(k, key) < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	it.seekToRestart(max(lo-1, 0))
	for it.Next(); it.valid && it.cmp(it.key, key) < 0; it.Next() {
	}
}

// Next moves the iterator to the next entry. If there is no next entry, Valid
// returns false. If the entry is not valid, Next stores an error that wraps
// errCorrupt and Valid returns false.
func (it *blockIter) Next() {
	if it.err != nil {
		return
	}
	if it.pos >= len(it.data) {
		it.valid = false
		return
	}
	for it.ri+1 < it.n && it.restart(it.ri+1) <= it.pos {
		if it.restart(it.ri+1) < it.pos {
			it.fail(corruptf("restart %d points inside an entry", it.ri+1))
			return
		}
		it.ri++
	}
	shared, unshared, valueLen, n, err := decodeEntryHeader(it.data[it.pos:])
	if err != nil {
		it.fail(err)
		return
	}
	if shared != 0 && it.restart(it.ri) == it.pos {
		it.fail(corruptf("entry at restart point %d shares %d bytes", it.pos, shared))
		return
	}
	if shared > len(it.key) {
		it.fail(corruptf("entry at %d shares %d bytes of a %d byte key", it.pos, shared, len(it.key)))
		return
	}
	body := it.data[it.pos+n:]
	if uint64(unshared)+uint64(valueLen) > uint64(len(body)) {
		it.fail(corruptf("entry at %d overflows the block", it.pos))
		return
	}
	it.key = append(it.key[:shared], body[:unshared]...)
	it.value = body[unshared : unshared+valueLen : unshared+valueLen]
	it.pos += n + unshared + valueLen
	it.valid = true
}

// Valid returns true if the iterator is at an entry.
func (it *blockIter) Valid() bool { return it.valid }

// Key returns the key of the current entry. The slice is valid only until the
// next move of the iterator. The caller must not change it.
func (it *blockIter) Key() []byte { return it.key }

// Value returns the value of the current entry. The slice is valid only until
// the next move of the iterator. The caller must not change it.
func (it *blockIter) Value() []byte { return it.value }

// Err returns the error that stopped the iteration, or nil.
func (it *blockIter) Err() error { return it.err }

// Close releases the block. It is safe to call Close more than one time.
func (it *blockIter) Close() error {
	it.valid = false
	it.data, it.restarts, it.key, it.value = nil, nil, nil, nil
	it.n = 0
	return nil
}

// seekToRestart sets the position to restart point i and clears the current
// entry. The next call to Next reads the entry at that point.
func (it *blockIter) seekToRestart(i int) {
	it.key = it.key[:0]
	it.value = nil
	it.valid = false
	it.ri = i
	it.pos = it.restart(i)
}

// restartKey returns the key at restart point i. It does not move the iterator.
func (it *blockIter) restartKey(i int) ([]byte, error) {
	off := it.restart(i)
	shared, unshared, _, n, err := decodeEntryHeader(it.data[off:])
	if err != nil {
		return nil, err
	}
	if shared != 0 {
		return nil, corruptf("restart %d shares %d bytes", i, shared)
	}
	body := it.data[off+n:]
	if unshared > len(body) {
		return nil, corruptf("restart %d key overflows the block", i)
	}
	return body[:unshared], nil
}

// fail stores err and makes the iterator not valid.
func (it *blockIter) fail(err error) {
	it.err = err
	it.valid = false
}

// decodeEntryHeader reads the three varints at the start of an entry. It returns
// each value as a non-negative int. The values for unshared and valueLen are not
// larger than len(src).
func decodeEntryHeader(src []byte) (shared, unshared, valueLen, n int, err error) {
	var v [3]uint64
	for i := range v {
		x, k := binary.Uvarint(src[n:])
		limit := uint64(len(src))
		if i == 0 {
			limit = math.MaxInt32
		}
		if k <= 0 || x > limit {
			return 0, 0, 0, 0, corruptf("bad entry header")
		}
		v[i] = x
		n += k
	}
	return int(v[0]), int(v[1]), int(v[2]), n, nil
}
