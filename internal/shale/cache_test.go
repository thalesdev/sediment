// SPDX-License-Identifier: MIT

package shale

import (
	"fmt"
	"sync"
	"testing"
)

func fillTables(t *testing.T, db *DB, n int) {
	t.Helper()
	for i := range n {
		mustPut(t, db, string(keyN(i)), fmt.Sprintf("value-%08d", i))
	}
	waitQuiet(t, db)
	if db.Metrics().Tables == 0 {
		t.Fatal("the test does not reach a table")
	}
}

func TestBlockCache(t *testing.T) {
	t.Run("Should count a hit when a block is read again", func(t *testing.T) {
		db := openDB(t, t.TempDir(), Options{MemtableSize: 4096, BlockCacheSize: 1 << 20})
		defer closeDB(t, db)
		fillTables(t, db, 2000)
		before := db.Metrics()
		for range 3 {
			mustGet(t, db, string(keyN(7)), "value-00000007")
		}
		after := db.Metrics()
		if after.BlockCacheHits-before.BlockCacheHits < 2 {
			t.Fatalf("hits went from %d to %d", before.BlockCacheHits, after.BlockCacheHits)
		}
		if after.BlockCacheBytes == 0 || after.BlockCacheBytes > 1<<20 {
			t.Fatalf("the cache holds %d bytes", after.BlockCacheBytes)
		}
	})

	t.Run("Should give the same values with the cache off, small and large", func(t *testing.T) {
		for _, size := range []int{-1, 1 << 12, 1 << 24} {
			db := openDB(t, t.TempDir(), Options{MemtableSize: 4096, BlockCacheSize: size})
			fillTables(t, db, 1500)
			for round := range 2 {
				for i := 0; i < 1500; i += 7 {
					mustGet(t, db, string(keyN(i)), fmt.Sprintf("value-%08d", i))
				}
				_ = round
			}
			mustNotFind(t, db, "no-such-key")
			m := db.Metrics()
			if size < 0 && (m.BlockCacheHits != 0 || m.BlockCacheBytes != 0) {
				t.Fatalf("the cache is off and has %+v", m)
			}
			if size > 0 && m.BlockCacheHits+m.BlockCacheMisses == 0 {
				t.Fatalf("the cache of %d was not used", size)
			}
			closeDB(t, db)
		}
	})

	t.Run("Should see the newest version after an overwrite and a delete", func(t *testing.T) {
		db := openDB(t, t.TempDir(), Options{MemtableSize: 4096, BlockCacheSize: 1 << 20})
		defer closeDB(t, db)
		fillTables(t, db, 1000)
		mustGet(t, db, string(keyN(5)), "value-00000005")
		mustPut(t, db, string(keyN(5)), "new")
		mustDelete(t, db, string(keyN(6)))
		waitIdle(t, db)
		mustGet(t, db, string(keyN(5)), "new")
		mustNotFind(t, db, string(keyN(6)))
		mustGet(t, db, string(keyN(7)), "value-00000007")
	})

	t.Run("Should survive many readers with a small cache while tables are written and compacted", func(t *testing.T) {
		db := openDB(t, t.TempDir(), Options{MemtableSize: 4096, BlockCacheSize: 1 << 14})
		defer closeDB(t, db)
		fillTables(t, db, 1500)
		stop := make(chan struct{})
		var readers sync.WaitGroup
		for r := range 6 {
			readers.Go(func() {
				for i := r; ; i += 11 {
					select {
					case <-stop:
						return
					default:
					}
					n := i % 1500
					got, err := db.Get(keyN(n))
					if err != nil {
						t.Errorf("Get(%d): %v", n, err)
						return
					}
					if string(got) != fmt.Sprintf("value-%08d", n) && string(got) != fmt.Sprintf("again-%08d", n) {
						t.Errorf("Get(%d) = %q", n, got)
						return
					}
				}
			})
		}
		for round := range 3 {
			for i := range 1500 {
				mustPut(t, db, string(keyN(i)), fmt.Sprintf("again-%08d", i))
			}
			_ = round
		}
		waitIdle(t, db)
		close(stop)
		readers.Wait()
		if m := db.Metrics(); m.Compactions == 0 {
			t.Fatalf("the test does not cover a compaction: %+v", m)
		}
	})
}
