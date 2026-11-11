// SPDX-License-Identifier: MIT

package merge

import (
	"bytes"
	"errors"
	"slices"
)

var errClosed = errors.New("merge: iterator closed")

// Source is a sorted sequence of key-value pairs. It has the same methods and
// rules as shale.Iterator.
type Source interface {
	SeekToFirst()
	Seek(key []byte)
	Next()
	Valid() bool
	Key() []byte
	Value() []byte
	Err() error
	Close() error
}

// Iterator iterates in key order over all the entries of its sources. It does
// not remove duplicates. It has the same methods and rules as shale.Iterator.
// It is not safe for concurrent use.
type Iterator struct {
	cmp     func(a, b []byte) int
	sources []Source
	heap    []int
	err     error
	closed  bool
}

// New returns an iterator over sources. cmp orders the keys. If cmp is nil, New
// uses bytes.Compare. The sources must be sorted with cmp. The source at index 0
// is the newest, and the source with the highest index is the oldest. If entries
// from different sources have the same key, the entry of the source with the
// lower index comes first, so the first entry for a key is the newest. The
// engine depends on this order. The iterator has no position until the caller
// calls SeekToFirst or Seek.
func New(cmp func(a, b []byte) int, sources ...Source) *Iterator {
	if cmp == nil {
		cmp = bytes.Compare
	}
	return &Iterator{
		cmp:     cmp,
		sources: slices.Clone(sources),
		heap:    make([]int, 0, len(sources)),
	}
}

// SeekToFirst moves the iterator to the first entry of all the sources.
func (it *Iterator) SeekToFirst() {
	if !it.usable() {
		return
	}
	for _, s := range it.sources {
		s.SeekToFirst()
	}
	it.build()
}

// Seek moves the iterator to the first entry with a key that is greater than or
// equal to key.
func (it *Iterator) Seek(key []byte) {
	if !it.usable() {
		return
	}
	for _, s := range it.sources {
		s.Seek(key)
	}
	it.build()
}

// Next moves the iterator to the next entry. It moves only the source that
// holds the current entry. If there is no next entry, Valid returns false.
func (it *Iterator) Next() {
	if !it.Valid() {
		return
	}
	it.advanceTop()
}

// Valid returns true if the iterator is at a key.
func (it *Iterator) Valid() bool {
	return it.err == nil && len(it.heap) > 0
}

// Key returns the key of the current entry. The slice is valid only until the next move of
// the iterator. The caller must not change it.
func (it *Iterator) Key() []byte {
	return it.sources[it.heap[0]].Key()
}

// Value returns the value of the current entry. The slice is valid only until
// the next move of the iterator. The caller must not change it.
func (it *Iterator) Value() []byte {
	return it.sources[it.heap[0]].Value()
}

// Err returns the first error that a source reported, or nil. After an error,
// Valid returns false.
func (it *Iterator) Err() error {
	return it.err
}

// Close closes all sources, also if one of them fails, and returns the first
// error. It is safe to call Close more than one time. Each call after the first
// returns nil.
func (it *Iterator) Close() error {
	if it.closed {
		return nil
	}
	it.closed = true
	it.heap = it.heap[:0]
	var first error
	for _, s := range it.sources {
		if err := s.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// usable returns true if the iterator can be moved. If the iterator is closed,
// it stores an error.
func (it *Iterator) usable() bool {
	if it.closed && it.err == nil {
		it.err = errClosed
	}
	return it.err == nil
}

// build makes the heap from the sources that have a key. If a source has an
// error, it stores the error and leaves the heap empty.
func (it *Iterator) build() {
	it.heap = it.heap[:0]
	for i, s := range it.sources {
		if err := s.Err(); err != nil {
			it.fail(err)
			return
		}
		if s.Valid() {
			it.heap = append(it.heap, i)
		}
	}
	for i := len(it.heap)/2 - 1; i >= 0; i-- {
		it.siftDown(i)
	}
}

// advanceTop moves the source at the top of the heap and restores the order of
// the heap. It stores the error and returns false if the source has an error.
func (it *Iterator) advanceTop() bool {
	s := it.sources[it.heap[0]]
	s.Next()
	if s.Valid() {
		it.siftDown(0)
		return true
	}
	if err := s.Err(); err != nil {
		it.fail(err)
		return false
	}
	last := len(it.heap) - 1
	it.heap[0] = it.heap[last]
	it.heap = it.heap[:last]
	if last > 0 {
		it.siftDown(0)
	}
	return true
}

// fail stores err and empties the heap.
func (it *Iterator) fail(err error) {
	it.err = err
	it.heap = it.heap[:0]
}

// less returns true if source a goes before source b. It compares the keys, and
// then the indexes of the sources.
func (it *Iterator) less(a, b int) bool {
	c := it.cmp(it.sources[a].Key(), it.sources[b].Key())
	return c < 0 || c == 0 && a < b
}

// siftDown moves the element at position i down the heap until the heap order
// is correct.
func (it *Iterator) siftDown(i int) {
	n := len(it.heap)
	for {
		child := 2*i + 1
		if child >= n {
			return
		}
		if right := child + 1; right < n && it.less(it.heap[right], it.heap[child]) {
			child = right
		}
		if !it.less(it.heap[child], it.heap[i]) {
			return
		}
		it.heap[i], it.heap[child] = it.heap[child], it.heap[i]
		i = child
	}
}
