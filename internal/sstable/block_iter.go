package sstable

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// errCorrupt wraps every error caused by malformed table bytes.
var errCorrupt = errors.New("sstable: corrupt table")

func corruptf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errCorrupt, fmt.Sprintf(format, args...))
}

// blockIter iterates over the entries of one block. It satisfies the same
// contract as sediment.Iterator.
type blockIter struct {
	cmp func(a, b []byte) int

	data     []byte
	restarts []byte
	n        int

	pos   int
	key   []byte
	value []byte
	valid bool
	err   error
}

func newBlockIter(block []byte, cmp func(a, b []byte) int) (*blockIter, error) {
	if len(block) < 4 {
		return nil, corruptf("block of %d bytes has no restart count", len(block))
	}
	n := int(binary.LittleEndian.Uint32(block[len(block)-4:]))
	if n == 0 || n > (len(block)-4)/4 {
		return nil, corruptf("invalid restart count %d for block of %d bytes", n, len(block))
	}
	start := len(block) - 4 - 4*n
	it := &blockIter{
		cmp:      cmp,
		data:     block[:start],
		restarts: block[start : len(block)-4],
		n:        n,
	}
	for i := range n {
		off := it.restart(i)
		if len(it.data) > 0 && off >= len(it.data) || len(it.data) == 0 && off != 0 {
			return nil, corruptf("restart %d points at %d, outside of %d bytes of entries", i, off, len(it.data))
		}
	}
	return it, nil
}

func (it *blockIter) restart(i int) int {
	return int(binary.LittleEndian.Uint32(it.restarts[4*i:]))
}

func (it *blockIter) SeekToFirst() {
	it.seekToRestart(0)
	it.Next()
}

// Seek positions the iterator at the first entry whose key is >= key.
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

func (it *blockIter) Next() {
	if it.err != nil {
		return
	}
	if it.pos >= len(it.data) {
		it.valid = false
		return
	}
	shared, unshared, valueLen, n, err := decodeEntryHeader(it.data[it.pos:])
	if err != nil {
		it.fail(err)
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

func (it *blockIter) Valid() bool   { return it.valid }
func (it *blockIter) Key() []byte   { return it.key }
func (it *blockIter) Value() []byte { return it.value }
func (it *blockIter) Err() error    { return it.err }

func (it *blockIter) Close() error {
	it.valid = false
	it.data, it.restarts, it.key, it.value = nil, nil, nil, nil
	it.n = 0
	return nil
}

func (it *blockIter) seekToRestart(i int) {
	it.key = it.key[:0]
	it.value = nil
	it.valid = false
	if len(it.data) > 0 {
		it.pos = it.restart(i)
	} else {
		it.pos = 0
	}
}

// restartKey returns the key stored at restart i, without moving the iterator.
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

func (it *blockIter) fail(err error) {
	it.err = err
	it.valid = false
}

// decodeEntryHeader reads the three varints that start an entry. The lengths
// are returned as ints that are non-negative and fit in len(src).
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
