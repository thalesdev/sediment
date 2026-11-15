// SPDX-License-Identifier: MIT

package sstable

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	"github.com/thalesdev/sediment/internal/ikey"
)

func blockOf(n int, fill byte) []byte { return bytes.Repeat([]byte{fill}, n) }

func TestBlockCache(t *testing.T) {
	t.Run("Should keep nothing when it is nil or has no capacity", func(t *testing.T) {
		for _, c := range []*BlockCache{nil, NewBlockCache(0), NewBlockCache(-5)} {
			c.put(1, 0, blockOf(100, 1))
			if _, ok := c.get(1, 0); ok {
				t.Fatal("a nil cache kept a block")
			}
			c.DropTable(1)
			if st := c.Stats(); st != (CacheStats{}) {
				t.Fatalf("got %+v", st)
			}
		}
	})

	t.Run("Should give back the block that was put and count hits and misses", func(t *testing.T) {
		c := NewBlockCache(1 << 20)
		if _, ok := c.get(1, 0); ok {
			t.Fatal("found a block in an empty cache")
		}
		c.put(1, 0, blockOf(4096, 7))
		got, ok := c.get(1, 0)
		if !ok || len(got) != 4096 || got[0] != 7 {
			t.Fatal("did not give back the block")
		}
		if _, ok := c.get(1, 4096); ok {
			t.Fatal("found a block at another offset")
		}
		if _, ok := c.get(2, 0); ok {
			t.Fatal("found a block of another table")
		}
		if st := c.Stats(); st.Hits != 1 || st.Misses != 3 || st.Bytes != 4096+entryOverhead {
			t.Fatalf("got %+v", st)
		}
	})

	t.Run("Should drop the block that was used least recently when it is full", func(t *testing.T) {
		const blocks = 10
		c := NewBlockCache(cacheShards * (4096 + entryOverhead) * 3)
		var keys []uint64
		for i := range uint64(1000) {
			if (blockKey{1, i}).shard() == 0 {
				keys = append(keys, i)
				if len(keys) == blocks {
					break
				}
			}
		}
		for _, k := range keys[:3] {
			c.put(1, k, blockOf(4096, byte(k)))
		}
		if _, ok := c.get(1, keys[0]); !ok {
			t.Fatal("the first block is missing")
		}
		c.put(1, keys[3], blockOf(4096, 9))
		if _, ok := c.get(1, keys[1]); ok {
			t.Fatal("the block that was used least recently is still there")
		}
		for _, k := range []uint64{keys[0], keys[2], keys[3]} {
			if _, ok := c.get(1, k); !ok {
				t.Fatalf("the block at %d was dropped", k)
			}
		}
	})

	t.Run("Should stay under its capacity", func(t *testing.T) {
		const capacity = 1 << 20
		c := NewBlockCache(capacity)
		for i := range uint64(5000) {
			c.put(1, i*4096, blockOf(4096, byte(i)))
		}
		if st := c.Stats(); st.Bytes > capacity || st.Bytes < capacity/2 {
			t.Fatalf("the cache holds %d bytes, the capacity is %d", st.Bytes, capacity)
		}
	})

	t.Run("Should not keep a block that is larger than a part of the cache", func(t *testing.T) {
		c := NewBlockCache(cacheShards * 1000)
		c.put(1, 0, blockOf(5000, 1))
		if _, ok := c.get(1, 0); ok {
			t.Fatal("kept a block that does not fit")
		}
		if st := c.Stats(); st.Bytes != 0 {
			t.Fatalf("got %+v", st)
		}
	})

	t.Run("Should keep the first block when the same one is put again", func(t *testing.T) {
		c := NewBlockCache(1 << 20)
		c.put(1, 0, blockOf(100, 1))
		c.put(1, 0, blockOf(100, 2))
		got, _ := c.get(1, 0)
		if got[0] != 1 {
			t.Fatal("replaced the block")
		}
		if st := c.Stats(); st.Bytes != 100+entryOverhead {
			t.Fatalf("got %+v", st)
		}
	})

	t.Run("Should add every block while it has room and some when it is full", func(t *testing.T) {
		c := NewBlockCache(cacheShards * 40 * (4096 + entryOverhead))
		for i := range uint64(100000) {
			if _, hit, _ := c.lookup(1, i*4096, 4096); hit {
				t.Fatal("hit on a block that was never put")
			}
		}
		admitted := 0
		for i := range uint64(100000) {
			_, _, admit := c.lookup(1, i*4096, 4096)
			if admit {
				c.put(1, i*4096, blockOf(4096, 1))
				admitted++
			}
		}
		if admitted < 40*cacheShards {
			t.Fatalf("only %d blocks were admitted while the cache had room", admitted)
		}
		full := 0
		for i := range uint64(100000) {
			if _, _, admit := c.lookup(2, i*4096, 4096); admit {
				full++
			}
		}
		if want := 100000 / (admitMask + 1); full < want/2 || full > want*2 {
			t.Fatalf("a full cache admitted %d of 100000 misses, want about %d", full, want)
		}
		if _, _, admit := c.lookup(3, 0, 1<<20); admit {
			t.Fatal("admitted a block that is larger than a part of the cache")
		}
	})

	t.Run("Should drop every block of a table and only those", func(t *testing.T) {
		c := NewBlockCache(1 << 22)
		for i := range uint64(200) {
			c.put(1, i*4096, blockOf(4096, 1))
			c.put(2, i*4096, blockOf(4096, 2))
		}
		c.DropTable(1)
		for i := range uint64(200) {
			if _, ok := c.get(1, i*4096); ok {
				t.Fatal("a block of the dropped table is there")
			}
			if _, ok := c.get(2, i*4096); !ok {
				t.Fatal("a block of the other table was dropped")
			}
		}
		if st := c.Stats(); st.Bytes != 200*(4096+entryOverhead) {
			t.Fatalf("got %+v", st)
		}
	})

	t.Run("Should let a reader keep a block after the cache dropped it", func(t *testing.T) {
		c := NewBlockCache(cacheShards * (100 + entryOverhead))
		c.put(1, 0, blockOf(100, 5))
		held, _ := c.get(1, 0)
		for i := uint64(1); i < 2000; i++ {
			c.put(1, i, blockOf(100, 6))
		}
		if _, ok := c.get(1, 0); ok {
			t.Fatal("the block was not dropped")
		}
		if held[0] != 5 || held[99] != 5 {
			t.Fatal("the block that the reader holds changed")
		}
	})

	t.Run("Should be safe for many goroutines", func(t *testing.T) {
		c := NewBlockCache(1 << 18)
		var wg sync.WaitGroup
		for g := range 8 {
			wg.Go(func() {
				for i := range 4000 {
					k := uint64((i*7 + g) % 300)
					if b, ok := c.get(uint64(g%3), k*512); ok {
						if len(b) != 512 || b[0] != byte(k) {
							t.Errorf("wrong block")
							return
						}
						continue
					}
					c.put(uint64(g%3), k*512, blockOf(512, byte(k)))
					if i%997 == 0 {
						c.DropTable(uint64(g % 3))
					}
				}
			})
		}
		wg.Wait()
		if st := c.Stats(); st.Bytes > 1<<18 {
			t.Fatalf("got %+v", st)
		}
	})
}

func TestFindWithCache(t *testing.T) {
	entries := versionedEntries(300)
	file := writeVersioned(t, entries)

	open := func(t *testing.T, cache *BlockCache, id uint64) *Reader {
		t.Helper()
		src := &countingReaderAt{b: file}
		o := internalOptions()
		o.Cache, o.CacheID = cache, id
		r, err := Open(src, int64(len(src.b)), o)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		return r
	}

	t.Run("Should find the same entries with and without a cache", func(t *testing.T) {
		plain, cached := open(t, nil, 0), open(t, NewBlockCache(1<<22), 1)
		for round := range 2 {
			for i := range 300 {
				lookup := ikey.AppendLookup(nil, fmt.Appendf(nil, "user-%04d", i), uint64(3+round*4))
				k1, v1, f1, e1 := plain.Find(lookup)
				k2, v2, f2, e2 := cached.Find(lookup)
				if e1 != nil || e2 != nil || f1 != f2 || !bytes.Equal(k1, k2) || !bytes.Equal(v1, v2) {
					t.Fatalf("lookup %d differs: %q %q %v %v / %q %q %v %v", i, k1, v1, f1, e1, k2, v2, f2, e2)
				}
			}
		}
	})

	t.Run("Should read a block from the file one time and then from the cache", func(t *testing.T) {
		cache := NewBlockCache(1 << 22)
		r := open(t, cache, 9)
		src := r.src.(*countingReaderAt)
		lookup := ikey.AppendLookup(nil, []byte("user-0123"), 100)
		if _, _, found, err := r.Find(lookup); err != nil || !found {
			t.Fatalf("Find: %v, %v", found, err)
		}
		reads := len(src.reads)
		for range 50 {
			if _, _, found, err := r.Find(lookup); err != nil || !found {
				t.Fatalf("Find: %v, %v", found, err)
			}
		}
		if len(src.reads) != reads {
			t.Fatalf("the file was read %d more times", len(src.reads)-reads)
		}
		if st := cache.Stats(); st.Hits != 50 || st.Misses != 1 {
			t.Fatalf("got %+v", st)
		}
	})

	t.Run("Should keep the tables that share a cache apart", func(t *testing.T) {
		cache := NewBlockCache(1 << 22)
		a, b := open(t, cache, 1), open(t, cache, 2)
		lookup := ikey.AppendLookup(nil, []byte("user-0007"), 100)
		a.Find(lookup)
		b.Find(lookup)
		if st := cache.Stats(); st.Misses != 2 || st.Hits != 0 {
			t.Fatalf("got %+v", st)
		}
		cache.DropTable(1)
		a.Find(lookup)
		b.Find(lookup)
		if st := cache.Stats(); st.Misses != 3 || st.Hits != 1 {
			t.Fatalf("got %+v", st)
		}
	})

	t.Run("Should not keep a block that fails its checksum", func(t *testing.T) {
		bad := bytes.Clone(file)
		bad[10] ^= 0xff
		cache := NewBlockCache(1 << 22)
		o := internalOptions()
		o.Cache, o.CacheID = cache, 1
		r, err := Open(&countingReaderAt{b: bad}, int64(len(bad)), o)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		lookup := ikey.AppendLookup(nil, []byte("user-0000"), 100)
		if _, _, _, err := r.Find(lookup); err == nil {
			t.Fatal("Find did not report the damaged block")
		}
		if st := cache.Stats(); st.Bytes != 0 {
			t.Fatalf("the damaged block is in the cache: %+v", st)
		}
	})

	t.Run("Should be safe for many readers of one table", func(t *testing.T) {
		cache := NewBlockCache(1 << 16)
		o := internalOptions()
		o.Cache, o.CacheID = cache, 1
		r, err := Open(bytes.NewReader(file), int64(len(file)), o)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		var wg sync.WaitGroup
		for g := range 8 {
			wg.Go(func() {
				for i := range 600 {
					n := (i*13 + g*7) % 300
					lookup := ikey.AppendLookup(nil, fmt.Appendf(nil, "user-%04d", n), 100)
					k, _, found, err := r.Find(lookup)
					if err != nil || !found {
						t.Errorf("Find: %v, %v", found, err)
						return
					}
					user, _, _, _ := ikey.Parse(k)
					if want := fmt.Sprintf("user-%04d", n); string(user) != want {
						t.Errorf("found %q, want %q", user, want)
						return
					}
				}
			})
		}
		wg.Wait()
	})
}
