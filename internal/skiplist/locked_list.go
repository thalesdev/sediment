package skiplist

import (
	"bytes"
	"sync"
	"unsafe"
)

type node struct {
	key   []byte
	value []byte
	next  []*node
}

type LockedIterator struct {
	list    *LockedList
	current *node
	err     error
}

func (it *LockedIterator) SeekToFirst() {
	it.list.mu.RLock()
	defer it.list.mu.RUnlock()

	it.current = it.list.head.next[0]
}

func (it *LockedIterator) Seek(key []byte) {
	it.list.mu.RLock()
	defer it.list.mu.RUnlock()

	it.current = it.list.findGreaterOrEqual(key, nil)
}

func (it *LockedIterator) Next() {
	if it.current == nil {
		panic("skiplist: Next called on an iterator that is not valid")
	}

	it.list.mu.RLock()
	defer it.list.mu.RUnlock()
	it.current = it.current.next[0]
}

func (it *LockedIterator) Valid() bool {
	return it.current != nil
}

func (it *LockedIterator) Key() []byte {
	if !it.Valid() {
		panic("skiplist: Key called on an iterator that is not valid")
	}
	return it.current.key
}

func (it *LockedIterator) Value() []byte {
	if !it.Valid() {
		panic("skiplist: Value called on an iterator that is not valid")
	}
	return it.current.value
}

func (it *LockedIterator) Err() error {
	return it.err
}

func (it *LockedIterator) Close() error {
	it.current = nil
	it.err = nil
	return nil
}

// LockedList is a thread-safe skip list implementation that allows concurrent insertions and iterations.
type LockedList struct {
	head          *node
	size          int
	currentHeight int
	comp          Comparator
	hg            *heightGen
	buffer        [maxHeight]*node

	mu sync.RWMutex
}

// NewLockedList creates a new LockedList with the given comparator and seed for the random height generator.
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

// findGreaterOrEqual returns the first node whose key is >= key, or nil.
// If update is non-nil, update[i] is set to the last node visited at level i
// (the predecessor of the result at that level).
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

// Insert inserts a key-value pair into the LockedList.
// If the key already exists, it panics.
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

// NewIterator returns a new interator for the LockedList.
// The iterator is not safe for concurrent use.
func (l *LockedList) NewIterator() *LockedIterator {
	return &LockedIterator{
		list:    l,
		current: nil,
	}
}
