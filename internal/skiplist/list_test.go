// SPDX-License-Identifier: MIT

package skiplist

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"sync"
	"testing"
)

func key(i int) []byte { return fmt.Appendf(nil, "key-%06d", i) }

func val(i int) []byte { return fmt.Appendf(nil, "val-%06d", i) }

func mustPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	fn()
}

func mustInsert(t testing.TB, l *List, k, v []byte) {
	t.Helper()
	if err := l.Insert(k, v); err != nil {
		t.Fatalf("Insert: %v", err)
	}
}

func newFilledList(t *testing.T, n int) *List {
	t.Helper()
	l := NewList(bytes.Compare, 42)
	for _, i := range rand.New(rand.NewPCG(7, 7)).Perm(n) {
		mustInsert(t, l, key(i), val(i))
	}
	return l
}

func countLevel0(l *List) int {
	n := 0
	for x := l.next(head, 0); x != 0; x = l.next(x, 0) {
		n++
	}
	return n
}

func nodeHeight(l *List, n uint32) int {
	return int(l.words[n>>chunkShift].Load()[n&chunkMask+4])
}

func checkInvariants(t *testing.T, l *List) {
	t.Helper()
	top := int(l.height.Load())
	if top < 1 || top > maxHeight {
		t.Fatalf("height %d out of range [1, %d]", top, maxHeight)
	}
	var below map[uint32]bool
	for level := 0; level < maxHeight; level++ {
		var prev []byte
		here := map[uint32]bool{}
		for n := l.next(head, level); n != 0; n = l.next(n, level) {
			if level >= top {
				t.Fatalf("node %q is linked at level %d, above the height %d", l.key(n), level, top)
			}
			if nodeHeight(l, n) <= level {
				t.Fatalf("node %q of height %d is linked at level %d", l.key(n), nodeHeight(l, n), level)
			}
			if prev != nil && l.comp(prev, l.key(n)) >= 0 {
				t.Fatalf("level %d is not ascending: %q then %q", level, prev, l.key(n))
			}
			if level > 0 && !below[n] {
				t.Fatalf("node %q is at level %d and not at level %d", l.key(n), level, level-1)
			}
			prev = l.key(n)
			here[n] = true
		}
		below = here
	}
}

func collect(l *List) [][2][]byte {
	var out [][2][]byte
	it := l.NewIterator()
	for it.SeekToFirst(); it.Valid(); it.Next() {
		out = append(out, [2][]byte{slices.Clone(it.Key()), slices.Clone(it.Value())})
	}
	return out
}

func TestList(t *testing.T) {
	t.Run("Should be valid and empty when it is new", func(t *testing.T) {
		l := NewList(bytes.Compare, 42)
		checkInvariants(t, l)
		it := l.NewIterator()
		it.SeekToFirst()
		if it.Valid() {
			t.Fatal("got a valid iterator on an empty list")
		}
		it.Seek([]byte("a"))
		if it.Valid() {
			t.Fatal("got a valid iterator from Seek on an empty list")
		}
		if got := l.ApproximateSize(); got != 0 {
			t.Fatalf("an empty list has size %d", got)
		}
	})

	t.Run("Should give the keys in ascending order after descending inserts", func(t *testing.T) {
		l := NewList(bytes.Compare, 42)
		for _, k := range []string{"e", "d", "c", "b", "a"} {
			mustInsert(t, l, []byte(k), []byte("1"))
		}
		got := collect(l)
		if len(got) != 5 {
			t.Fatalf("got %d entries", len(got))
		}
		for i, want := range []string{"a", "b", "c", "d", "e"} {
			if string(got[i][0]) != want {
				t.Fatalf("entry %d is %q, want %q", i, got[i][0], want)
			}
		}
		checkInvariants(t, l)
	})

	t.Run("Should keep its invariants after many random inserts", func(t *testing.T) {
		const n = 5000
		l := newFilledList(t, n)
		if l.height.Load() < 2 {
			t.Fatalf("height %d, not enough levels to exercise the structure", l.height.Load())
		}
		checkInvariants(t, l)
		if got := countLevel0(l); got != n {
			t.Fatalf("got %d entries, want %d", got, n)
		}
	})

	t.Run("Should panic on a duplicate key and stay usable", func(t *testing.T) {
		l := NewList(bytes.Compare, 42)
		mustInsert(t, l, []byte("a"), []byte("1"))
		mustPanic(t, func() { _ = l.Insert([]byte("a"), []byte("2")) })
		mustInsert(t, l, []byte("b"), []byte("2"))
		checkInvariants(t, l)
		if got := countLevel0(l); got != 2 {
			t.Fatalf("got %d entries, want 2", got)
		}
		it := l.NewIterator()
		it.Seek([]byte("a"))
		if string(it.Value()) != "1" {
			t.Fatalf("the value of a is %q", it.Value())
		}
	})

	t.Run("Should copy the key and the value", func(t *testing.T) {
		l := NewList(bytes.Compare, 42)
		k, v := []byte("key"), []byte("value")
		mustInsert(t, l, k, v)
		k[0], v[0] = 'X', 'Y'
		got := collect(l)
		if len(got) != 1 || string(got[0][0]) != "key" || string(got[0][1]) != "value" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("Should keep an empty key and an empty value", func(t *testing.T) {
		l := NewList(bytes.Compare, 42)
		mustInsert(t, l, []byte{}, []byte{})
		mustInsert(t, l, []byte("a"), nil)
		got := collect(l)
		if len(got) != 2 || len(got[0][0]) != 0 || len(got[0][1]) != 0 || string(got[1][0]) != "a" || len(got[1][1]) != 0 {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("Should keep entries that fill many blocks of data", func(t *testing.T) {
		l := NewList(bytes.Compare, 42)
		const n = 60
		for i := range n {
			v := bytes.Repeat([]byte{byte(i)}, 200<<10)
			mustInsert(t, l, key(i), v)
		}
		got := collect(l)
		if len(got) != n {
			t.Fatalf("got %d entries", len(got))
		}
		for i, e := range got {
			if !bytes.Equal(e[0], key(i)) || len(e[1]) != 200<<10 || e[1][0] != byte(i) || e[1][len(e[1])-1] != byte(i) {
				t.Fatalf("entry %d is wrong", i)
			}
		}
	})

	t.Run("Should keep entries that have a block of their own", func(t *testing.T) {
		l := NewList(bytes.Compare, 42)
		sizes := []int{largeEntry - 1, largeEntry, largeEntry + 1, 3 << 20, 17 << 20}
		for i, n := range sizes {
			mustInsert(t, l, key(i), bytes.Repeat([]byte{byte(i + 1)}, n))
			mustInsert(t, l, key(100+i), val(i))
		}
		for i, n := range sizes {
			it := l.NewIterator()
			it.Seek(key(i))
			v := it.Value()
			if len(v) != n || v[0] != byte(i+1) || v[n-1] != byte(i+1) {
				t.Fatalf("entry %d has %d bytes", i, len(v))
			}
			it.Seek(key(100 + i))
			if !bytes.Equal(it.Value(), val(i)) {
				t.Fatalf("small entry %d is %q", i, it.Value())
			}
		}
		checkInvariants(t, l)
	})

	t.Run("Should give ErrFull and keep what it has when the blocks of nodes end", func(t *testing.T) {
		l := NewList(bytes.Compare, 42)
		l.blockLimit = 1
		inserted := 0
		for i := 0; ; i++ {
			if err := l.Insert(key(i), nil); err != nil {
				if err != ErrFull {
					t.Fatalf("got %v", err)
				}
				break
			}
			inserted++
			if i > chunkWords {
				t.Fatal("the list never filled")
			}
		}
		if inserted < 1000 {
			t.Fatalf("only %d entries fit", inserted)
		}
		if got := countLevel0(l); got != inserted {
			t.Fatalf("the list has %d entries, %d were inserted", got, inserted)
		}
		checkInvariants(t, l)
		if err := l.Insert(key(inserted+1), nil); err != ErrFull && err != nil {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("Should give ErrFull and keep what it has when the blocks of data end", func(t *testing.T) {
		l := NewList(bytes.Compare, 42)
		l.blockLimit = 2
		v := bytes.Repeat([]byte{1}, 200<<10)
		inserted := 0
		for i := 0; i < 100; i++ {
			err := l.Insert(key(i), v)
			if err == ErrFull {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			inserted++
		}
		if inserted < 5 || inserted >= 100 {
			t.Fatalf("%d entries fit in two blocks", inserted)
		}
		if got := countLevel0(l); got != inserted {
			t.Fatalf("the list has %d entries, %d were inserted", got, inserted)
		}
		checkInvariants(t, l)
	})
}

func TestIterator(t *testing.T) {
	list := NewList(bytes.Compare, 42)
	for _, kv := range [][2]string{{"a", "1"}, {"b", "2"}, {"c", "3"}} {
		mustInsert(t, list, []byte(kv[0]), []byte(kv[1]))
	}

	t.Run("Should be invalid before any seek", func(t *testing.T) {
		if list.NewIterator().Valid() {
			t.Fatal("got a valid iterator before any Seek")
		}
	})

	t.Run("Should go to the first entry with SeekToFirst", func(t *testing.T) {
		it := list.NewIterator()
		it.SeekToFirst()
		if !it.Valid() || string(it.Key()) != "a" || string(it.Value()) != "1" {
			t.Fatal("SeekToFirst is wrong")
		}
	})

	t.Run("Should walk to the end with Next", func(t *testing.T) {
		it := list.NewIterator()
		it.SeekToFirst()
		for _, want := range []string{"a", "b", "c"} {
			if !it.Valid() || string(it.Key()) != want {
				t.Fatalf("want %q", want)
			}
			it.Next()
		}
		if it.Valid() {
			t.Fatal("got a valid iterator after the last key")
		}
	})

	t.Run("Should panic on Next, Key and Value of an invalid iterator", func(t *testing.T) {
		it := list.NewIterator()
		mustPanic(t, func() { it.Next() })
		mustPanic(t, func() { it.Key() })
		mustPanic(t, func() { it.Value() })
	})

	t.Run("Should find the first key that is greater than or equal with Seek", func(t *testing.T) {
		for _, tc := range []struct{ seek, want string }{{"b", "b"}, {"bb", "c"}, {"0", "a"}, {"z", ""}} {
			it := list.NewIterator()
			it.Seek([]byte(tc.seek))
			if tc.want == "" {
				if it.Valid() {
					t.Fatalf("Seek(%q) found %q", tc.seek, it.Key())
				}
				continue
			}
			if !it.Valid() || string(it.Key()) != tc.want {
				t.Fatalf("Seek(%q) is wrong, want %q", tc.seek, tc.want)
			}
		}
	})

	t.Run("Should have no error and close more than once", func(t *testing.T) {
		it := list.NewIterator()
		it.SeekToFirst()
		if it.Err() != nil {
			t.Fatal("Err is not nil")
		}
		if it.Close() != nil || it.Valid() || it.Close() != nil {
			t.Fatal("Close is wrong")
		}
	})

	t.Run("Should keep the key valid while the iterator moves on", func(t *testing.T) {
		it := list.NewIterator()
		it.SeekToFirst()
		first := it.Key()
		it.Next()
		it.Next()
		if string(first) != "a" {
			t.Fatalf("the first key changed to %q", first)
		}
	})
}

func TestListConcurrent(t *testing.T) {
	t.Run("Should let readers see an ordered list while one writer inserts", func(t *testing.T) {
		const writes, readers = 3000, 4
		l := NewList(bytes.Compare, 42)
		done := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			defer close(done)
			for _, i := range rand.New(rand.NewPCG(7, 7)).Perm(writes) {
				mustInsert(t, l, key(i), val(i))
			}
		})
		for range readers {
			wg.Go(func() {
				for {
					select {
					case <-done:
						return
					default:
					}
					it := l.NewIterator()
					var prev []byte
					for it.SeekToFirst(); it.Valid(); it.Next() {
						k := it.Key()
						if prev != nil && bytes.Compare(prev, k) >= 0 {
							t.Errorf("a reader saw %q after %q", k, prev)
							return
						}
						if !bytes.Equal(it.Value(), val(atoi(k))) {
							t.Errorf("the value of %q is %q", k, it.Value())
							return
						}
						prev = k
					}
				}
			})
		}
		wg.Wait()
		checkInvariants(t, l)
		if got := countLevel0(l); got != writes {
			t.Fatalf("got %d entries, want %d", got, writes)
		}
	})

	t.Run("Should take the inserts of many writers at the same time", func(t *testing.T) {
		const writers, each = 8, 4000
		l := NewList(bytes.Compare, 42)
		var wg sync.WaitGroup
		for w := range writers {
			wg.Go(func() {
				for _, i := range rand.New(rand.NewPCG(uint64(w), 9)).Perm(each) {
					mustInsert(t, l, key(i*writers+w), val(i*writers+w))
				}
			})
		}
		wg.Wait()
		checkInvariants(t, l)
		got := collect(l)
		if len(got) != writers*each {
			t.Fatalf("got %d entries, want %d", len(got), writers*each)
		}
		for i, e := range got {
			if !bytes.Equal(e[0], key(i)) || !bytes.Equal(e[1], val(i)) {
				t.Fatalf("entry %d is %q %q", i, e[0], e[1])
			}
		}
	})

	t.Run("Should let readers seek while writers insert", func(t *testing.T) {
		const writers, each = 4, 3000
		l := NewList(bytes.Compare, 42)
		var wg sync.WaitGroup
		stop := make(chan struct{})
		for w := range writers {
			wg.Go(func() {
				for _, i := range rand.New(rand.NewPCG(uint64(w), 5)).Perm(each) {
					mustInsert(t, l, key(i*writers+w), val(i*writers+w))
				}
			})
		}
		var readers sync.WaitGroup
		for r := range 4 {
			readers.Go(func() {
				rng := rand.New(rand.NewPCG(uint64(r), 3))
				it := l.NewIterator()
				for {
					select {
					case <-stop:
						return
					default:
					}
					target := key(rng.IntN(writers * each))
					it.Seek(target)
					if it.Valid() && bytes.Compare(it.Key(), target) < 0 {
						t.Errorf("Seek(%q) found the smaller key %q", target, it.Key())
						return
					}
				}
			})
		}
		wg.Wait()
		close(stop)
		readers.Wait()
		checkInvariants(t, l)
	})
}

func atoi(k []byte) int {
	var n int
	fmt.Sscanf(string(k), "key-%d", &n)
	return n
}

func TestApproximateSize(t *testing.T) {
	t.Run("Should grow with each insert by at least the bytes of the entry", func(t *testing.T) {
		l := NewList(bytes.Compare, 1)
		previous := 0
		for i := range 100 {
			mustInsert(t, l, key(i), val(i))
			got := l.ApproximateSize()
			if got < previous+len(key(i))+len(val(i)) {
				t.Fatalf("size went from %d to %d after an insert of %d bytes", previous, got, len(key(i))+len(val(i)))
			}
			previous = got
		}
	})

	t.Run("Should be safe to read while other goroutines insert", func(t *testing.T) {
		l := NewList(bytes.Compare, 1)
		var wg sync.WaitGroup
		wg.Go(func() {
			for i := range 500 {
				mustInsert(t, l, key(i), val(i))
			}
		})
		for range 500 {
			_ = l.ApproximateSize()
		}
		wg.Wait()
	})
}

func TestRandomHeight(t *testing.T) {
	t.Run("Should give heights that fall by a factor of about four", func(t *testing.T) {
		l := NewList(bytes.Compare, 99)
		counts := make([]int, maxHeight+1)
		const n = 200000
		for range n {
			counts[l.randomHeight()]++
		}
		if counts[0] != 0 {
			t.Fatal("got a height of 0")
		}
		if got := float64(counts[1]) / n; got < 0.74 || got > 0.76 {
			t.Fatalf("%.3f of the heights are 1, want about 0.75", got)
		}
		if got := float64(counts[2]) / n; got < 0.18 || got > 0.20 {
			t.Fatalf("%.3f of the heights are 2, want about 0.1875", got)
		}
		for h := 1; h <= maxHeight; h++ {
			if counts[h] == 0 && h <= 6 {
				t.Fatalf("no node of height %d in %d", h, n)
			}
		}
	})

	t.Run("Should give the same heights for the same seed", func(t *testing.T) {
		a, b := NewList(bytes.Compare, 5), NewList(bytes.Compare, 5)
		for range 1000 {
			if a.randomHeight() != b.randomHeight() {
				t.Fatal("two lists with one seed gave different heights")
			}
		}
	})
}

func randomKey(rng *rand.Rand) []byte {
	k := make([]byte, rng.IntN(12))
	for i := range k {
		k[i] = byte('a' + rng.IntN(4))
	}
	return k
}

func TestListAgainstAModel(t *testing.T) {
	t.Run("Should behave like a sorted slice of keys", func(t *testing.T) {
		for seed := range 20 {
			rng := rand.New(rand.NewPCG(uint64(seed), 11))
			l := NewList(bytes.Compare, uint64(seed))
			var model [][]byte
			seen := map[string]bool{}
			for range 600 {
				k := randomKey(rng)
				if seen[string(k)] {
					continue
				}
				seen[string(k)] = true
				i := sort.Search(len(model), func(i int) bool { return bytes.Compare(model[i], k) >= 0 })
				model = slices.Insert(model, i, k)
				mustInsert(t, l, k, append([]byte("v-"), k...))
			}
			checkInvariants(t, l)
			got := collect(l)
			if len(got) != len(model) {
				t.Fatalf("seed %d: got %d entries, want %d", seed, len(got), len(model))
			}
			for i, e := range got {
				if !bytes.Equal(e[0], model[i]) || !bytes.Equal(e[1], append([]byte("v-"), model[i]...)) {
					t.Fatalf("seed %d: entry %d is %q %q, want %q", seed, i, e[0], e[1], model[i])
				}
			}
			for range 300 {
				target := randomKey(rng)
				i := sort.Search(len(model), func(i int) bool { return bytes.Compare(model[i], target) >= 0 })
				it := l.NewIterator()
				it.Seek(target)
				if i == len(model) {
					if it.Valid() {
						t.Fatalf("seed %d: Seek(%q) found %q, want nothing", seed, target, it.Key())
					}
					continue
				}
				if !it.Valid() || !bytes.Equal(it.Key(), model[i]) {
					t.Fatalf("seed %d: Seek(%q) is wrong, want %q", seed, target, model[i])
				}
			}
		}
	})
}

func FuzzList(f *testing.F) {
	f.Add([]byte("abc\x00abd\x00a\x00"))
	f.Add([]byte("\x00\x00\x00"))
	f.Add(bytes.Repeat([]byte("zyx\x00"), 40))
	f.Fuzz(func(t *testing.T, data []byte) {
		l := NewList(bytes.Compare, uint64(len(data)))
		var model [][]byte
		seen := map[string]bool{}
		for _, k := range bytes.Split(data, []byte{0}) {
			if seen[string(k)] {
				continue
			}
			seen[string(k)] = true
			i := sort.Search(len(model), func(i int) bool { return bytes.Compare(model[i], k) >= 0 })
			model = slices.Insert(model, i, slices.Clone(k))
			if err := l.Insert(k, k); err != nil {
				t.Fatal(err)
			}
		}
		got := collect(l)
		if len(got) != len(model) {
			t.Fatalf("got %d entries, want %d", len(got), len(model))
		}
		for i, e := range got {
			if !bytes.Equal(e[0], model[i]) || !bytes.Equal(e[1], model[i]) {
				t.Fatalf("entry %d is %q %q, want %q", i, e[0], e[1], model[i])
			}
		}
		for _, target := range model {
			it := l.NewIterator()
			it.Seek(target)
			if !it.Valid() || !bytes.Equal(it.Key(), target) {
				t.Fatalf("Seek(%q) is wrong", target)
			}
		}
	})
}
