// SPDX-License-Identifier: MIT

package skiplist

import (
	"bytes"
	"sync"
	"unsafe"
)

// node is one element of the skip list. next[i] is the next node at level i.
type node struct {
	key   []byte
	value []byte
	next  []*node
}

// LockedIterator iterates over a LockedList in key order. It takes the read lock
// of the list for each call, and not between calls. It is not safe for
// concurrent use.
type LockedIterator struct {
	list    *LockedList
	current *node
	err     error
}

// SeekToFirst moves the iterator to the first entry.
func (it *LockedIterator) SeekToFirst() {
	it.list.mu.RLock()
	defer it.list.mu.RUnlock()

	it.current = it.list.head.next[0]
}

// Seek moves the iterator to the first entry with a key that is greater than or
// equal to key.
func (it *LockedIterator) Seek(key []byte) {
	it.list.mu.RLock()
	defer it.list.mu.RUnlock()

	it.current = it.list.findGreaterOrEqual(key, nil)
}

// Next moves the iterator to the next entry. If there is no next entry, Valid
// returns false. Next panics if the iterator is not valid.
func (it *LockedIterator) Next() {
	if it.current == nil {
		panic("skiplist: Next called on an iterator that is not valid")
	}

	it.list.mu.RLock()
	defer it.list.mu.RUnlock()
	it.current = it.current.next[0]
}

// Valid returns true if the iterator is at an entry.
func (it *LockedIterator) Valid() bool {
	return it.current != nil
}

// Key returns the key of the current entry. The slice is valid only until the
// next move of the iterator. The caller must not change it. It panics if the
// iterator is not valid.
func (it *LockedIterator) Key() []byte {
	if !it.Valid() {
		panic("skiplist: Key called on an iterator that is not valid")
	}
	return it.current.key
}

// Value returns the value of the current entry. The slice is valid only until
// the next move of the iterator. The caller must not change it. It panics if the
// iterator is not valid.
func (it *LockedIterator) Value() []byte {
	if !it.Valid() {
		panic("skiplist: Value called on an iterator that is not valid")
	}
	return it.current.value
}

// Err returns the error that stopped the iteration, or nil.
func (it *LockedIterator) Err() error {
	return it.err
}

// Close releases the iterator. It is safe to call Close more than one time.
func (it *LockedIterator) Close() error {
	it.current = nil
	it.err = nil
	return nil
}

// LockedList is a skip list. It is safe for concurrent use. Many goroutines can
// insert keys and iterate at the same time.
type LockedList struct {
	head          *node
	size          int
	currentHeight int
	comp          Comparator
	hg            *heightGen
	buffer        [maxHeight]*node

	mu sync.RWMutex
}

// NewLockedList returns an empty LockedList. comp orders the keys. seed starts the
// generator that selects the height of each new node.
func NewLockedList(comp Comparator, seed uint64) *LockedList {
	return &LockedList{
		head: &node{
			next: make([]*node, maxHeight),
		},
		currentHeight: 1,
		comp:          comp,
		hg:            newHeightGen(seed),
		buffer:        [maxHeight]*node{},
	}
}

// findGreaterOrEqual returns the first node with a key that is greater than or
// equal to key. It returns nil if no such node exists.
// If update is not nil, update[i] holds the last node visited at level i. This
// node is the predecessor of the result at that level.
// The caller must hold l.mu.
func (l *LockedList) findGreaterOrEqual(key []byte, update []*node) *node {
	x := l.head
	for level := l.currentHeight - 1; level >= 0; level-- {
		for x.next[level] != nil && l.comp(x.next[level].key, key) < 0 {
			x = x.next[level]
		}
		if update != nil {
			update[level] = x
		}
	}
	return x.next[0]
}

// Insert adds a key-value pair to the list. It panics if the key is already in the
// list.
func (l *LockedList) Insert(key, value []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()

	found := l.findGreaterOrEqual(key, l.buffer[:])
	if found != nil && l.comp(found.key, key) == 0 {
		panic("duplicate key insertion is not allowed")
	}
	height := l.hg.randomHeight()
	if height > l.currentHeight {
		for i := l.currentHeight; i < height; i++ {
			l.buffer[i] = l.head
		}
		l.currentHeight = height
	}

	newNode := &node{
		key:   bytes.Clone(key),
		value: bytes.Clone(value),
		next:  make([]*node, height),
	}

	for i := range height {
		newNode.next[i] = l.buffer[i].next[i]
		l.buffer[i].next[i] = newNode
	}

	l.size += len(key) + len(value) + int(unsafe.Sizeof(node{})) + height*int(unsafe.Sizeof(uintptr(0)))
}

// ApproximateSize returns an estimate of the number of bytes that the entries of
// the list use. It counts the keys, the values and the nodes.
func (l *LockedList) ApproximateSize() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.size
}

// NewIterator returns an iterator over the list. The iterator is not safe for
// concurrent use.
func (l *LockedList) NewIterator() *LockedIterator {
	return &LockedIterator{
		list:    l,
		current: nil,
	}
}
