// SPDX-License-Identifier: MIT

package skiplist

import (
	"errors"
	"math/bits"
	"sync"
	"sync/atomic"
)

const (
	// chunkShift is the log of the number of words in a block of nodes.
	chunkShift = 18

	// chunkWords is the number of 32-bit words in a block of nodes, 1 MiB.
	chunkWords = 1 << chunkShift

	// chunkMask selects the position of a word in its block.
	chunkMask = chunkWords - 1

	// dataBlockSize is the size in bytes of a block that holds keys and values.
	dataBlockSize = 1 << 20

	// largeEntry is the size from which a key and a value have a block of their
	// own, so that they do not waste the end of a shared block.
	largeEntry = dataBlockSize / 4

	// maxBlocks is the largest number of blocks of nodes, and of shared blocks of
	// data, in a list. The list holds up to 4 GiB of each.
	maxBlocks = 4096

	// maxLarge is the largest number of entries that have a block of their own.
	maxLarge = 8192

	// largeFlag marks the block number of an entry that has a block of its own.
	largeFlag = 1 << 31

	// maxEntry is the largest size of a key and a value together.
	maxEntry = 1 << 30

	// headerWords is the number of words of a node before its links: the data
	// block, the offset, the length of the key, the length of the value and the
	// height.
	headerWords = 5

	// head is the index of the first node of the list. It has no key, it is as tall
	// as the tallest node can be, and every list starts at it. Index 0 is nil.
	head = 1
)

// ErrFull is the error of Insert when the list has no more room for an entry: the
// list holds 4 GiB of nodes or 4 GiB of keys and values, or an entry is larger than
// 1 GiB.
var ErrFull = errors.New("skiplist: the list is full")

// wordBlock is a block of nodes.
type wordBlock [chunkWords]uint32

// dataBlock is a block of keys and values.
type dataBlock [dataBlockSize]byte

// List is a skip list for keys that are never deleted. It is safe for concurrent
// use, and nobody waits for a lock. Many goroutines can insert keys and read at the
// same time. A reader sees each entry whole or not at all, and it sees an entry as
// soon as Insert links it at the lowest level.
//
// The nodes, the keys and the values are in blocks of memory that have no pointers,
// so the garbage collector does not read them. A node is a few words in a block of
// uint32, and it names the next nodes by their index in the blocks. A key and a value
// are copied to a block of bytes. A list takes new blocks as it grows and gives them
// all back to the collector when nobody uses the list.
type List struct {
	comp   Comparator
	rng    atomic.Uint64
	height atomic.Int32
	size   atomic.Int64

	wordCursor atomic.Uint64
	words      [maxBlocks]atomic.Pointer[wordBlock]

	dataMu     sync.Mutex
	dataCursor atomic.Uint64
	small      [maxBlocks]atomic.Pointer[dataBlock]
	largeCount atomic.Uint32
	large      [maxLarge]atomic.Pointer[[]byte]

	blockLimit uint32
}

// NewList returns an empty list. comp orders the keys. seed starts the generator
// that selects the height of each new node, and the same seed gives the same
// sequence of heights.
func NewList(comp Comparator, seed uint64) *List {
	l := &List{comp: comp, blockLimit: maxBlocks}
	l.rng.Store(seed)
	l.height.Store(1)
	l.wordCursor.Store(head)
	if idx, ok := l.allocWords(headerWords + maxHeight); !ok || idx != head {
		panic("skiplist: cannot make the first node")
	}
	l.words[0].Load()[head+4] = maxHeight
	l.small[0].Store(new(dataBlock))
	return l
}

// word returns the address of the word at index i. The block of i must exist.
func (l *List) word(i uint32) *uint32 {
	return &l.words[i>>chunkShift].Load()[i&chunkMask]
}

// next returns the index of the node that follows n at level, or 0.
func (l *List) next(n uint32, level int) uint32 {
	return atomic.LoadUint32(l.word(n + headerWords + uint32(level)))
}

// allocWords reserves n words for a node and returns the index of the first one. A
// node is always inside one block.
func (l *List) allocWords(n int) (uint32, bool) {
	for {
		cur := l.wordCursor.Load()
		start := cur
		if cur&chunkMask+uint64(n) > chunkWords {
			start = cur&^chunkMask + chunkWords
		}
		end := start + uint64(n)
		if (end-1)>>chunkShift >= uint64(l.blockLimit) {
			return 0, false
		}
		if !l.wordCursor.CompareAndSwap(cur, end) {
			continue
		}
		b := start >> chunkShift
		if l.words[b].Load() == nil {
			l.words[b].CompareAndSwap(nil, new(wordBlock))
		}
		return uint32(start), true
	}
}

// allocData reserves n bytes for a key and a value, and returns the block, which has
// largeFlag set for an entry with a block of its own, and the offset in it.
func (l *List) allocData(n int) (block, offset uint32, ok bool) {
	if n >= largeEntry {
		i := l.largeCount.Add(1) - 1
		if i >= maxLarge {
			return 0, 0, false
		}
		b := make([]byte, n)
		l.large[i].Store(&b)
		return largeFlag | i, 0, true
	}
	for {
		cur := l.dataCursor.Load()
		b, o := uint32(cur>>32), uint32(cur)
		if uint64(o)+uint64(n) <= dataBlockSize {
			if l.dataCursor.CompareAndSwap(cur, cur+uint64(n)) {
				return b, o, true
			}
			continue
		}
		l.dataMu.Lock()
		if again := l.dataCursor.Load(); uint32(again>>32) == b {
			if b+1 >= l.blockLimit {
				l.dataMu.Unlock()
				return 0, 0, false
			}
			l.small[b+1].Store(new(dataBlock))
			l.dataCursor.Store(uint64(b+1) << 32)
		}
		l.dataMu.Unlock()
	}
}

// bytes returns the n bytes at offset in block.
func (l *List) bytes(block, offset, n uint32) []byte {
	if block&largeFlag == 0 {
		return l.small[block].Load()[offset : offset+n : offset+n]
	}
	return (*l.large[block&^largeFlag].Load())[offset : offset+n : offset+n]
}

// key returns the key of node n. It is valid for as long as the list is, and the
// caller must not change it.
func (l *List) key(n uint32) []byte {
	b := l.words[n>>chunkShift].Load()
	i := n & chunkMask
	return l.bytes(b[i], b[i+1], b[i+2])
}

// value returns the value of node n. It is valid for as long as the list is, and
// the caller must not change it.
func (l *List) value(n uint32) []byte {
	b := l.words[n>>chunkShift].Load()
	i := n & chunkMask
	return l.bytes(b[i], b[i+1]+b[i+2], b[i+3])
}

// randomHeight returns the height of a new node. A node is one level taller with a
// probability of one in four.
func (l *List) randomHeight() int {
	z := l.rng.Add(0x9e3779b97f4a7c15)
	z = (z ^ z>>30) * 0xbf58476d1ce4e5b9
	z = (z ^ z>>27) * 0x94d049bb133111eb
	z ^= z >> 31
	return min(1+bits.TrailingZeros64(z)/2, maxHeight)
}

// newNode copies the key and the value to the blocks and makes a node for them. The
// links of the node are 0 until the caller links it.
func (l *List) newNode(key, value []byte, height int) (uint32, error) {
	n := len(key) + len(value)
	if n >= maxEntry {
		return 0, ErrFull
	}
	block, offset, ok := l.allocData(n)
	if !ok {
		return 0, ErrFull
	}
	dst := l.bytes(block, offset, uint32(n))
	copy(dst, key)
	copy(dst[len(key):], value)
	idx, ok := l.allocWords(headerWords + height)
	if !ok {
		return 0, ErrFull
	}
	b := l.words[idx>>chunkShift].Load()
	i := idx & chunkMask
	b[i], b[i+1], b[i+2], b[i+3], b[i+4] = block, offset, uint32(len(key)), uint32(len(value)), uint32(height)
	l.size.Add(int64(n + 4*(headerWords+height)))
	return idx, nil
}

// splice finds, at each level, the last node with a key less than key, in preds, and
// the node after it, in succs. It returns the node with a key equal to key, or 0.
func (l *List) splice(key []byte, preds, succs *[maxHeight]uint32) uint32 {
	x := uint32(head)
	top := int(l.height.Load())
	for level := maxHeight - 1; level >= top; level-- {
		preds[level], succs[level] = head, 0
	}
	var equal uint32
	for level := top - 1; level >= 0; level-- {
		var nx uint32
		for {
			nx = l.next(x, level)
			if nx == 0 {
				break
			}
			c := l.comp(l.key(nx), key)
			if c >= 0 {
				if c == 0 {
					equal = nx
				}
				break
			}
			x = nx
		}
		preds[level], succs[level] = x, nx
	}
	return equal
}

// seek returns the first node with a key greater than or equal to key, or 0. It
// returns the node that it read at level 0 and does not read the link of the last
// node again, because an insert can put a node with a smaller key there in between.
func (l *List) seek(key []byte) uint32 {
	x := uint32(head)
	var nx uint32
	for level := int(l.height.Load()) - 1; level >= 0; level-- {
		for {
			nx = l.next(x, level)
			if nx == 0 || l.comp(l.key(nx), key) >= 0 {
				break
			}
			x = nx
		}
	}
	return nx
}

// Insert adds a key-value pair to the list. It copies the key and the value, so the
// caller can use them again after Insert returns. It returns ErrFull if the list has
// no room for the entry, and then it does not change the list. It panics if the key
// is already in the list, which is a bug of the caller.
func (l *List) Insert(key, value []byte) error {
	height := l.randomHeight()
	node, err := l.newNode(key, value, height)
	if err != nil {
		return err
	}
	for {
		cur := l.height.Load()
		if int(cur) >= height || l.height.CompareAndSwap(cur, int32(height)) {
			break
		}
	}
	var preds, succs [maxHeight]uint32
	for {
		if l.splice(key, &preds, &succs) != 0 {
			panic("duplicate key insertion is not allowed")
		}
		atomic.StoreUint32(l.word(node+headerWords), succs[0])
		if atomic.CompareAndSwapUint32(l.word(preds[0]+headerWords), succs[0], node) {
			break
		}
	}
	for level := 1; level < height; level++ {
		for {
			atomic.StoreUint32(l.word(node+headerWords+uint32(level)), succs[level])
			if atomic.CompareAndSwapUint32(l.word(preds[level]+headerWords+uint32(level)), succs[level], node) {
				break
			}
			l.splice(key, &preds, &succs)
		}
	}
	return nil
}

// ApproximateSize returns an estimate of the number of bytes that the entries of the
// list use. It counts the keys, the values and the nodes.
func (l *List) ApproximateSize() int {
	return int(l.size.Load())
}

// Iterator iterates over a List in key order. It is not safe for concurrent use,
// but any number of iterators can read one list while it grows. It sees the entries
// that were in the list when it reached them.
type Iterator struct {
	list    *List
	current uint32
}

// NewIterator returns an iterator over the list. It has no position until SeekToFirst
// or Seek.
func (l *List) NewIterator() *Iterator {
	return &Iterator{list: l}
}

// SeekToFirst moves the iterator to the first entry.
func (it *Iterator) SeekToFirst() {
	it.current = it.list.next(head, 0)
}

// Seek moves the iterator to the first entry with a key that is greater than or
// equal to key.
func (it *Iterator) Seek(key []byte) {
	it.current = it.list.seek(key)
}

// Next moves the iterator to the next entry. If there is no next entry, Valid
// returns false. Next panics if the iterator is not valid.
func (it *Iterator) Next() {
	if it.current == 0 {
		panic("skiplist: Next called on an iterator that is not valid")
	}
	it.current = it.list.next(it.current, 0)
}

// Valid returns true if the iterator is at an entry.
func (it *Iterator) Valid() bool {
	return it.current != 0
}

// Key returns the key of the current entry. The slice is valid for as long as the
// list is, and the caller must not change it. It panics if the iterator is not
// valid.
func (it *Iterator) Key() []byte {
	if it.current == 0 {
		panic("skiplist: Key called on an iterator that is not valid")
	}
	return it.list.key(it.current)
}

// Value returns the value of the current entry. The slice is valid for as long as
// the list is, and the caller must not change it. It panics if the iterator is not
// valid.
func (it *Iterator) Value() []byte {
	if it.current == 0 {
		panic("skiplist: Value called on an iterator that is not valid")
	}
	return it.list.value(it.current)
}

// Err returns the error that stopped the iteration. It is always nil.
func (it *Iterator) Err() error {
	return nil
}

// Close releases the iterator. It is safe to call Close more than one time.
func (it *Iterator) Close() error {
	it.current = 0
	return nil
}
