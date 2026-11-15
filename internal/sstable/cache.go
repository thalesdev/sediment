// SPDX-License-Identifier: MIT

package sstable

import "sync"

const (
	// cacheShards is the number of parts of a BlockCache. Each part has its own lock
	// and its own list, so readers of different blocks rarely wait for each other. It
	// must be a power of 2.
	cacheShards = 16

	// entryOverhead is the number of bytes that an entry costs besides its block: the
	// entry, its place in the map and its links in the list.
	entryOverhead = 96
)

// blockKey names a block: the table that it is in and its offset in the file.
type blockKey struct {
	table  uint64
	offset uint64
}

// shard returns the number of the part of the cache that holds the block.
func (k blockKey) shard() int {
	h := k.table*0x9e3779b97f4a7c15 ^ k.offset*0xbf58476d1ce4e5b9
	h ^= h >> 32
	return int(h & (cacheShards - 1))
}

// cacheEntry is a block in a part of the cache, and a node of its list.
type cacheEntry struct {
	key        blockKey
	block      []byte
	prev, next *cacheEntry
}

// cacheShard is a part of a BlockCache: a map, and a list that keeps the entries from
// the one that was used last to the one that was used first.
type cacheShard struct {
	mu       sync.Mutex
	items    map[blockKey]*cacheEntry
	first    *cacheEntry
	last     *cacheEntry
	bytes    int
	capacity int
	hits     uint64
	misses   uint64
}

// remove takes e out of the list and the map, and gives its bytes back. The caller
// must hold s.mu.
func (s *cacheShard) remove(e *cacheEntry) {
	s.unlink(e)
	delete(s.items, e.key)
	s.bytes -= len(e.block) + entryOverhead
}

// unlink takes e out of the list. The caller must hold s.mu.
func (s *cacheShard) unlink(e *cacheEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		s.first = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		s.last = e.prev
	}
	e.prev, e.next = nil, nil
}

// pushFront puts e at the start of the list. The caller must hold s.mu.
func (s *cacheShard) pushFront(e *cacheEntry) {
	e.next = s.first
	if s.first != nil {
		s.first.prev = e
	}
	s.first = e
	if s.last == nil {
		s.last = e
	}
}

// BlockCache keeps data blocks that were read from tables, so that a read of the same
// block does not read the file, check the checksum and allocate the block again. It
// has a limit in bytes, and when it is full it drops the block that was used least
// recently. A cache can be shared by any number of tables, which name their blocks by
// the Options.CacheID. It is safe for concurrent use. A nil *BlockCache is a cache that
// holds nothing.
//
// The blocks are not copied, and a block that the cache drops is not reused, so a
// reader can keep using a block after the cache dropped it. The caller must not change
// a block.
type BlockCache struct {
	shards [cacheShards]cacheShard
}

// NewBlockCache returns a cache that holds up to capacity bytes of blocks, counting an
// overhead for each one. It returns nil, which holds nothing, if capacity is not
// positive.
func NewBlockCache(capacity int) *BlockCache {
	if capacity <= 0 {
		return nil
	}
	c := &BlockCache{}
	for i := range c.shards {
		c.shards[i].items = make(map[blockKey]*cacheEntry)
		c.shards[i].capacity = max(capacity/cacheShards, 1)
	}
	return c
}

// get returns the block at offset of the table, and moves it to the front of its list.
func (c *BlockCache) get(table, offset uint64) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	k := blockKey{table, offset}
	s := &c.shards[k.shard()]
	s.mu.Lock()
	e := s.items[k]
	if e == nil {
		s.misses++
		s.mu.Unlock()
		return nil, false
	}
	s.hits++
	if s.first != e {
		s.unlink(e)
		s.pushFront(e)
	}
	block := e.block
	s.mu.Unlock()
	return block, true
}

// put adds the block at offset of the table, and drops the blocks that were used least
// recently until the part of the cache fits its limit. A block that is larger than the
// part is not kept. If the block is already there, put only moves it to the front.
func (c *BlockCache) put(table, offset uint64, block []byte) {
	if c == nil {
		return
	}
	k := blockKey{table, offset}
	s := &c.shards[k.shard()]
	s.mu.Lock()
	defer s.mu.Unlock()
	cost := len(block) + entryOverhead
	if cost > s.capacity {
		return
	}
	if e := s.items[k]; e != nil {
		if s.first != e {
			s.unlink(e)
			s.pushFront(e)
		}
		return
	}
	e := &cacheEntry{key: k, block: block}
	s.items[k] = e
	s.pushFront(e)
	s.bytes += cost
	for s.bytes > s.capacity && s.last != nil {
		s.remove(s.last)
	}
}

// DropTable removes every block of the table. The engine calls it when it removes the
// file of a table, so that the blocks of a file that does not exist do not take the
// room of the blocks that are in use.
func (c *BlockCache) DropTable(table uint64) {
	if c == nil {
		return
	}
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		for k, e := range s.items {
			if k.table == table {
				s.remove(e)
			}
		}
		s.mu.Unlock()
	}
}

// CacheStats is the use of a BlockCache.
type CacheStats struct {
	// Hits is the number of reads that found their block in the cache.
	Hits uint64

	// Misses is the number of reads that did not.
	Misses uint64

	// Bytes is the size of the blocks in the cache, with their overhead.
	Bytes int
}

// Stats returns the use of the cache. It is the zero value for a nil cache.
func (c *BlockCache) Stats() CacheStats {
	var st CacheStats
	if c == nil {
		return st
	}
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		st.Hits += s.hits
		st.Misses += s.misses
		st.Bytes += s.bytes
		s.mu.Unlock()
	}
	return st
}
