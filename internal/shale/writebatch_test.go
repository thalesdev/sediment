// SPDX-License-Identifier: MIT

package shale

import (
	"errors"
	"strconv"
	"sync"
	"testing"
)

func TestWriteBatch(t *testing.T) {
	t.Run("Should apply the operations in order", func(t *testing.T) {
		db, err := Open(t.TempDir(), Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var b Batch
		b.Put([]byte("a"), []byte("1"))
		b.Put([]byte("b"), []byte("2"))
		b.Put([]byte("a"), []byte("3"))
		b.Delete([]byte("b"))
		if b.Len() != 4 {
			t.Fatalf("got %d", b.Len())
		}
		if err := db.Write(&b); err != nil {
			t.Fatal(err)
		}
		if v, err := db.Get([]byte("a")); err != nil || string(v) != "3" {
			t.Fatalf("a: %q, %v", v, err)
		}
		if _, err := db.Get([]byte("b")); !errors.Is(err, ErrNotFound) {
			t.Fatalf("b: %v", err)
		}
	})

	t.Run("Should do nothing for an empty batch", func(t *testing.T) {
		db, err := Open(t.TempDir(), Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := db.Write(&Batch{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Should survive a reopen", func(t *testing.T) {
		dir := t.TempDir()
		db, err := Open(dir, Options{})
		if err != nil {
			t.Fatal(err)
		}
		var b Batch
		for i := range 100 {
			b.Put([]byte("k"+strconv.Itoa(i)), []byte("v"+strconv.Itoa(i)))
		}
		if err := db.Write(&b); err != nil {
			t.Fatal(err)
		}
		if err := db.Put([]byte("after"), []byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db, err = Open(dir, Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		for i := range 100 {
			v, err := db.Get([]byte("k" + strconv.Itoa(i)))
			if err != nil || string(v) != "v"+strconv.Itoa(i) {
				t.Fatalf("k%d: %q, %v", i, v, err)
			}
		}
		if v, err := db.Get([]byte("after")); err != nil || string(v) != "x" {
			t.Fatalf("after: %q, %v", v, err)
		}
	})

	t.Run("Should never show a part of a batch", func(t *testing.T) {
		db, err := Open(t.TempDir(), Options{MemtableSize: 64 << 10})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		const rounds = 3000
		var wg sync.WaitGroup
		stop := make(chan struct{})
		bad := make(chan string, 1)
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					second, err2 := db.Get([]byte("b"))
					first, err1 := db.Get([]byte("a"))
					if errors.Is(err2, ErrNotFound) {
						continue
					}
					if err1 != nil || err2 != nil {
						continue
					}
					x, _ := strconv.Atoi(string(first))
					y, _ := strconv.Atoi(string(second))
					if x < y {
						select {
						case bad <- "saw b=" + string(second) + " before a=" + string(first):
						default:
						}
						return
					}
				}
			}()
		}
		for i := range rounds {
			var b Batch
			v := []byte(strconv.Itoa(i))
			b.Put([]byte("a"), v)
			b.Put([]byte("b"), v)
			if err := db.Write(&b); err != nil {
				t.Fatal(err)
			}
		}
		close(stop)
		wg.Wait()
		select {
		case msg := <-bad:
			t.Fatal(msg)
		default:
		}
	})

	t.Run("Should share a group with other writes", func(t *testing.T) {
		db, err := Open(t.TempDir(), Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var wg sync.WaitGroup
		for i := range 32 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var b Batch
				b.Put([]byte("x"+strconv.Itoa(i)), []byte("1"))
				b.Put([]byte("y"+strconv.Itoa(i)), []byte("2"))
				if err := db.Write(&b); err != nil {
					t.Error(err)
				}
				if err := db.Put([]byte("z"+strconv.Itoa(i)), []byte("3")); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		for i := range 32 {
			for _, p := range []string{"x", "y", "z"} {
				if _, err := db.Get([]byte(p + strconv.Itoa(i))); err != nil {
					t.Fatalf("%s%d: %v", p, i, err)
				}
			}
		}
	})
}
