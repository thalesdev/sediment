// SPDX-License-Identifier: MIT

package skiplist

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"unsafe"
)

func key(i int) []byte { return fmt.Appendf(nil, "key-%06d", i) }

func val(i int) []byte { return fmt.Appendf(nil, "val-%06d", i) }

func newFilledList(t *testing.T, n int) *LockedList {
	t.Helper()
	l := NewLockedList(bytes.Compare, 42)
	for _, i := range rand.New(rand.NewPCG(7, 7)).Perm(n) {
		l.Insert(key(i), val(i))
	}
	return l
}

func mustPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	fn()
}

func countLevel0(l *LockedList) int {
	n := 0
	for x := l.head.next[0]; x != nil; x = x.next[0] {
		n++
	}
	return n
}

func checkInvariants(t *testing.T, l *LockedList) {
	t.Helper()

	if l.currentHeight < 1 || l.currentHeight > maxHeight {
		t.Fatalf("currentHeight %d out of range [1, %d]", l.currentHeight, maxHeight)
	}

	for level := 0; level < maxHeight; level++ {
		var prev *node
		for n := l.head.next[level]; n != nil; n = n.next[level] {
			if level >= l.currentHeight {
				t.Fatalf("node %q linked at level %d, above currentHeight %d", n.key, level, l.currentHeight)
			}
			if len(n.next) <= level {
				t.Fatalf("node %q (height %d) linked at level %d", n.key, len(n.next), level)
			}
			if prev != nil && l.comp(prev.key, n.key) >= 0 {
				t.Fatalf("level %d not strictly ascending: %q then %q", level, prev.key, n.key)
			}
			prev = n
		}
	}

	for level := 1; level < l.currentHeight; level++ {
		b := l.head.next[level-1]
		for a := l.head.next[level]; a != nil; a = a.next[level] {
			for b != nil && b != a {
				b = b.next[level-1]
			}
			if b == nil {
				t.Fatalf("level %d is not a subset of level %d: node %q missing below", level, level-1, a.key)
			}
		}
	}

	tallest, size := 1, 0
	for n := l.head.next[0]; n != nil; n = n.next[0] {
		tallest = max(tallest, len(n.next))
		size += len(n.key) + len(n.value) +
			int(unsafe.Sizeof(node{})) + len(n.next)*int(unsafe.Sizeof(uintptr(0)))
	}
	if tallest != l.currentHeight {
		t.Fatalf("currentHeight is %d, tallest node is %d", l.currentHeight, tallest)
	}
	if size != l.size {
		t.Fatalf("size is %d, recomputed %d", l.size, size)
	}
}

func TestLockedList(t *testing.T) {
	t.Run("empty list is valid", func(t *testing.T) {
		l := NewLockedList(bytes.Compare, 42)
		checkInvariants(t, l)
		it := l.NewIterator()
		it.SeekToFirst()
		if it.Valid() {
			t.Fatal("got valid iterator on empty list")
		}
	})

	t.Run("descending inserts come out ascending", func(t *testing.T) {
		l := NewLockedList(bytes.Compare, 42)
		for _, k := range []string{"e", "d", "c", "b", "a"} {
			l.Insert([]byte(k), []byte("1"))
		}
		it := l.NewIterator()
		it.SeekToFirst()
		for _, want := range []string{"a", "b", "c", "d", "e"} {
			if !it.Valid() {
				t.Fatalf("iterator ended early, want %q", want)
			}
			if got := it.Key(); !bytes.Equal(got, []byte(want)) {
				t.Fatalf("got %q, want %q", got, want)
			}
			it.Next()
		}
		if it.Valid() {
			t.Fatal("got valid iterator after the last key")
		}
		checkInvariants(t, l)
	})

	t.Run("insert at beginning, middle and end", func(t *testing.T) {
		l := NewLockedList(bytes.Compare, 42)
		for _, k := range []string{"b", "d", "a", "c", "e"} {
			l.Insert([]byte(k), []byte("1"))
		}
		checkInvariants(t, l)
		if got := countLevel0(l); got != 5 {
			t.Fatalf("got %d entries, want 5", got)
		}
	})

	t.Run("many random inserts keep invariants", func(t *testing.T) {
		const n = 5000
		l := newFilledList(t, n)
		if l.currentHeight < 2 {
			t.Fatalf("currentHeight %d: not enough levels to exercise the structure", l.currentHeight)
		}
		checkInvariants(t, l)
		if got := countLevel0(l); got != n {
			t.Fatalf("got %d entries, want %d", got, n)
		}
	})

	t.Run("duplicate key panics and list stays usable", func(t *testing.T) {
		l := NewLockedList(bytes.Compare, 42)
		l.Insert([]byte("a"), []byte("1"))
		mustPanic(t, func() { l.Insert([]byte("a"), []byte("2")) })

		l.Insert([]byte("b"), []byte("2"))
		checkInvariants(t, l)
		if got := countLevel0(l); got != 2 {
			t.Fatalf("got %d entries, want 2", got)
		}
	})
}

func TestLockedIterator(t *testing.T) {
	list := NewLockedList(bytes.Compare, 42)
	for _, kv := range [][2]string{{"a", "1"}, {"b", "2"}, {"c", "3"}} {
		list.Insert([]byte(kv[0]), []byte(kv[1]))
	}

	t.Run("new iterator is invalid", func(t *testing.T) {
		if list.NewIterator().Valid() {
			t.Fatal("got valid iterator before any Seek")
		}
	})

	t.Run("SeekToFirst", func(t *testing.T) {
		it := list.NewIterator()
		it.SeekToFirst()
		if !it.Valid() {
			t.Fatal("got invalid iterator after SeekToFirst")
		}
		if got := it.Key(); !bytes.Equal(got, []byte("a")) {
			t.Fatalf("key: got %q, want %q", got, "a")
		}
		if got := it.Value(); !bytes.Equal(got, []byte("1")) {
			t.Fatalf("value: got %q, want %q", got, "1")
		}
	})

	t.Run("Next walks to the end", func(t *testing.T) {
		it := list.NewIterator()
		it.SeekToFirst()
		for _, want := range []string{"a", "b", "c"} {
			if !it.Valid() {
				t.Fatalf("iterator ended early, want %q", want)
			}
			if got := it.Key(); !bytes.Equal(got, []byte(want)) {
				t.Fatalf("got %q, want %q", got, want)
			}
			it.Next()
		}
		if it.Valid() {
			t.Fatal("got valid iterator after the last key")
		}
	})

	t.Run("Next, Key and Value panic on invalid iterator", func(t *testing.T) {
		it := list.NewIterator()
		mustPanic(t, func() { it.Next() })
		mustPanic(t, func() { it.Key() })
		mustPanic(t, func() { it.Value() })
	})

	t.Run("Seek", func(t *testing.T) {
		tests := []struct {
			name string
			seek string
			want string
		}{
			{"exact key", "b", "b"},
			{"between keys", "bb", "c"},
			{"before all keys", "0", "a"},
			{"after all keys", "z", ""},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				it := list.NewIterator()
				it.Seek([]byte(tc.seek))
				if tc.want == "" {
					if it.Valid() {
						t.Fatalf("Seek(%q): got key %q, want invalid", tc.seek, it.Key())
					}
					return
				}
				if !it.Valid() {
					t.Fatalf("Seek(%q): got invalid, want %q", tc.seek, tc.want)
				}
				if got := it.Key(); !bytes.Equal(got, []byte(tc.want)) {
					t.Fatalf("Seek(%q): got %q, want %q", tc.seek, got, tc.want)
				}
			})
		}
	})

	t.Run("Seek on empty list", func(t *testing.T) {
		it := NewLockedList(bytes.Compare, 42).NewIterator()
		it.Seek([]byte("a"))
		if it.Valid() {
			t.Fatal("got valid iterator on empty list")
		}
	})

	t.Run("Err is always nil", func(t *testing.T) {
		it := list.NewIterator()
		it.SeekToFirst()
		for it.Valid() {
			it.Next()
		}
		if err := it.Err(); err != nil {
			t.Fatalf("got error %v, want nil", err)
		}
	})

	t.Run("Close invalidates and is idempotent", func(t *testing.T) {
		it := list.NewIterator()
		it.SeekToFirst()
		if err := it.Close(); err != nil {
			t.Fatalf("first Close: got %v, want nil", err)
		}
		if it.Valid() {
			t.Fatal("got valid iterator after Close")
		}
		if err := it.Close(); err != nil {
			t.Fatalf("second Close: got %v, want nil", err)
		}
	})
}

func TestLockedListConcurrent(t *testing.T) {
	const (
		writes  = 2000
		readers = 4
	)
	l := NewLockedList(bytes.Compare, 42)
	done := make(chan struct{})
	var wg sync.WaitGroup

	wg.Go(func() {
		defer close(done)
		for _, i := range rand.New(rand.NewPCG(7, 7)).Perm(writes) {
			l.Insert(key(i), val(i))
		}
	})

	for range readers {
		wg.Go(func() {
			var prev []byte
			for {
				select {
				case <-done:
					return
				default:
				}
				it := l.NewIterator()
				prev = prev[:0]
				for it.SeekToFirst(); it.Valid(); it.Next() {
					k := it.Key()
					if len(prev) > 0 && bytes.Compare(prev, k) >= 0 {
						t.Errorf("reader saw %q after %q", k, prev)
						it.Close()
						return
					}
					prev = append(prev[:0], k...)
				}
				it.Close()
			}
		})
	}

	wg.Wait()
	checkInvariants(t, l)
	if got := countLevel0(l); got != writes {
		t.Fatalf("got %d entries, want %d", got, writes)
	}
}

func TestApproximateSize(t *testing.T) {
	t.Run("Should be zero for an empty list and grow with each insert", func(t *testing.T) {
		l := NewLockedList(bytes.Compare, 1)
		if got := l.ApproximateSize(); got != 0 {
			t.Fatalf("empty list has size %d", got)
		}
		previous := 0
		for i := range 100 {
			l.Insert(key(i), val(i))
			got := l.ApproximateSize()
			if got < previous+len(key(i))+len(val(i)) {
				t.Fatalf("size went from %d to %d after an insert of %d bytes", previous, got, len(key(i))+len(val(i)))
			}
			previous = got
		}
	})

	t.Run("Should be safe to call while other goroutines insert", func(t *testing.T) {
		l := NewLockedList(bytes.Compare, 1)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				l.Insert(key(i), val(i))
			}
		}()
		for range 500 {
			_ = l.ApproximateSize()
		}
		wg.Wait()
	})
}
