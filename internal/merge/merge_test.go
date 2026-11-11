// SPDX-License-Identifier: MIT

package merge

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/thalesdev/sediment/internal/skiplist"
	"github.com/thalesdev/sediment/internal/sstable"
)

var (
	errSeek   = errors.New("seek failed")
	errNext   = errors.New("next failed")
	errCloseA = errors.New("close a failed")
	errCloseB = errors.New("close b failed")
)

type kv struct{ key, value []byte }

type sliceSource struct {
	entries []kv
	pos     int
	buf     []byte
	err     error

	seekErr  error
	failNext int
	closeErr error

	nexts  int
	closes int
}

func (s *sliceSource) load() {
	for i := range s.buf {
		s.buf[i] = 0xff
	}
	if s.pos < len(s.entries) {
		s.buf = append(s.buf[:0], s.entries[s.pos].key...)
	}
}

func (s *sliceSource) SeekToFirst() {
	if s.seekErr != nil {
		s.err = s.seekErr
		return
	}
	s.pos = 0
	s.load()
}

func (s *sliceSource) Seek(key []byte) {
	if s.seekErr != nil {
		s.err = s.seekErr
		return
	}
	lo, hi := 0, len(s.entries)
	for lo < hi {
		mid := (lo + hi) / 2
		if bytes.Compare(s.entries[mid].key, key) < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	s.pos = lo
	s.load()
}

func (s *sliceSource) Next() {
	s.nexts++
	if s.failNext > 0 && s.nexts == s.failNext {
		s.err = errNext
		return
	}
	s.pos++
	s.load()
}

func (s *sliceSource) Valid() bool   { return s.err == nil && s.pos < len(s.entries) }
func (s *sliceSource) Key() []byte   { return s.buf }
func (s *sliceSource) Value() []byte { return s.entries[s.pos].value }
func (s *sliceSource) Err() error    { return s.err }

func (s *sliceSource) Close() error {
	s.closes++
	return s.closeErr
}

func asSources(raw [][]kv) ([]*sliceSource, []Source) {
	spies := make([]*sliceSource, len(raw))
	sources := make([]Source, len(raw))
	for i, entries := range raw {
		spies[i] = &sliceSource{entries: entries}
		sources[i] = spies[i]
	}
	return spies, sources
}

func randomSources(rng *rand.Rand, k, universe int, density float64) [][]kv {
	raw := make([][]kv, k)
	for i := range raw {
		for n := range universe {
			if rng.Float64() < density {
				key := fmt.Sprintf("key-%05d", n)
				raw[i] = append(raw[i], kv{[]byte(key), []byte(fmt.Sprintf("s%d:%s", i, key))})
			}
		}
	}
	return raw
}

func reference(raw [][]kv, from []byte) []kv {
	var out []kv
	for _, entries := range raw {
		for _, e := range entries {
			if bytes.Compare(e.key, from) >= 0 {
				out = append(out, e)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b kv) int { return bytes.Compare(a.key, b.key) })
	return out
}

func drain(t *testing.T, it *Iterator) []kv {
	t.Helper()
	var out []kv
	for ; it.Valid(); it.Next() {
		out = append(out, kv{bytes.Clone(it.Key()), bytes.Clone(it.Value())})
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
	return out
}

func assertKV(t *testing.T, got, want []kv) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i].key, want[i].key) || !bytes.Equal(got[i].value, want[i].value) {
			t.Fatalf("entry %d: got %q=%q, want %q=%q", i, got[i].key, got[i].value, want[i].key, want[i].value)
		}
	}
}

func TestIterator(t *testing.T) {
	shapes := []struct {
		name    string
		density float64
	}{
		{"sparse", 0.1},
		{"overlapping", 0.5},
		{"identical", 1},
	}
	for _, k := range []int{0, 1, 2, 5, 32} {
		for _, shape := range shapes {
			t.Run(fmt.Sprintf("Should merge %d %s sources", k, shape.name), func(t *testing.T) {
				rng := rand.New(rand.NewPCG(uint64(k), 7))
				raw := randomSources(rng, k, 400, shape.density)
				_, sources := asSources(raw)
				it := New(nil, sources...)

				it.SeekToFirst()
				assertKV(t, drain(t, it), reference(raw, nil))

				it.SeekToFirst()
				assertKV(t, drain(t, it), reference(raw, nil))
			})
		}
	}

	t.Run("Should seek to every key, between keys and outside the range", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(3, 3))
		raw := randomSources(rng, 5, 300, 0.4)
		_, sources := asSources(raw)
		it := New(nil, sources...)
		for n := -1; n <= 301; n++ {
			for _, suffix := range []string{"", "-x"} {
				target := []byte(fmt.Sprintf("key-%05d%s", n, suffix))
				it.Seek(target)
				assertKV(t, drain(t, it), reference(raw, target))
			}
		}
		it.Seek(nil)
		assertKV(t, drain(t, it), reference(raw, nil))
	})

	t.Run("Should return every entry and put the source with the lowest index first", func(t *testing.T) {
		raw := [][]kv{
			{{[]byte("a"), []byte("new")}, {[]byte("c"), []byte("new")}},
			{{[]byte("a"), []byte("mid")}, {[]byte("b"), []byte("mid")}, {[]byte("c"), []byte("mid")}},
			{{[]byte("a"), []byte("old")}, {[]byte("b"), []byte("old")}, {[]byte("d"), []byte("old")}},
		}
		spies, sources := asSources(raw)
		it := New(nil, sources...)
		it.SeekToFirst()
		want := []kv{
			{[]byte("a"), []byte("new")}, {[]byte("a"), []byte("mid")}, {[]byte("a"), []byte("old")},
			{[]byte("b"), []byte("mid")}, {[]byte("b"), []byte("old")},
			{[]byte("c"), []byte("new")}, {[]byte("c"), []byte("mid")},
			{[]byte("d"), []byte("old")},
		}
		assertKV(t, drain(t, it), want)
		for i, s := range spies {
			if s.nexts != len(raw[i]) {
				t.Fatalf("source %d moved %d times, want %d", i, s.nexts, len(raw[i]))
			}
		}
	})

	t.Run("Should use the comparator that the caller gives", func(t *testing.T) {
		reverse := func(a, b []byte) int { return bytes.Compare(b, a) }
		raw := [][]kv{
			{{[]byte("c"), []byte("1")}, {[]byte("a"), []byte("1")}},
			{{[]byte("d"), []byte("2")}, {[]byte("c"), []byte("2")}, {[]byte("b"), []byte("2")}},
		}
		_, sources := asSources(raw)
		it := New(reverse, sources...)
		it.SeekToFirst()
		var keys string
		for ; it.Valid(); it.Next() {
			keys += string(it.Key())
		}
		if keys != "dccba" {
			t.Fatalf("got %q, want %q", keys, "dccba")
		}
	})

	t.Run("Should be empty when it has no source", func(t *testing.T) {
		it := New(nil)
		it.SeekToFirst()
		if it.Valid() || it.Err() != nil {
			t.Fatalf("valid=%v err=%v", it.Valid(), it.Err())
		}
		it.Seek([]byte("a"))
		if it.Valid() || it.Err() != nil {
			t.Fatalf("valid=%v err=%v", it.Valid(), it.Err())
		}
	})
}

func TestIteratorErrors(t *testing.T) {
	raw := func() [][]kv {
		return randomSources(rand.New(rand.NewPCG(9, 9)), 3, 100, 0.5)
	}

	t.Run("Should stop and report an error from a source at seek", func(t *testing.T) {
		spies, sources := asSources(raw())
		spies[1].seekErr = errSeek
		it := New(nil, sources...)
		it.SeekToFirst()
		if it.Valid() || !errors.Is(it.Err(), errSeek) {
			t.Fatalf("valid=%v err=%v", it.Valid(), it.Err())
		}
		it.Seek([]byte("key-00010"))
		if it.Valid() || !errors.Is(it.Err(), errSeek) {
			t.Fatalf("after Seek: valid=%v err=%v", it.Valid(), it.Err())
		}
	})

	t.Run("Should stop when a source fails at a later Seek and not use the other sources", func(t *testing.T) {
		spies, sources := asSources(raw())
		it := New(nil, sources...)
		it.SeekToFirst()
		if !it.Valid() {
			t.Fatal("iterator must be valid before the failure")
		}
		spies[0].seekErr = errSeek
		it.Seek([]byte("key-00010"))
		if it.Valid() || !errors.Is(it.Err(), errSeek) {
			t.Fatalf("valid=%v err=%v", it.Valid(), it.Err())
		}
	})

	t.Run("Should stop and report an error from a source during Next", func(t *testing.T) {
		spies, sources := asSources(raw())
		spies[2].failNext = 10
		it := New(nil, sources...)
		it.SeekToFirst()
		want := reference(raw(), nil)
		var got []kv
		for ; it.Valid(); it.Next() {
			got = append(got, kv{bytes.Clone(it.Key()), bytes.Clone(it.Value())})
		}
		if !errors.Is(it.Err(), errNext) {
			t.Fatalf("err=%v, want %v", it.Err(), errNext)
		}
		if len(got) >= len(want) {
			t.Fatalf("got %d entries, want fewer than %d", len(got), len(want))
		}
		assertKV(t, got, want[:len(got)])
		it.Next()
		if it.Valid() || !errors.Is(it.Err(), errNext) {
			t.Fatalf("after Next: valid=%v err=%v", it.Valid(), it.Err())
		}
		it.SeekToFirst()
		if it.Valid() || !errors.Is(it.Err(), errNext) {
			t.Fatalf("after SeekToFirst: valid=%v err=%v", it.Valid(), it.Err())
		}
	})

	t.Run("Should close every source one time and return the first error", func(t *testing.T) {
		spies, sources := asSources(raw())
		spies[0].closeErr = errCloseA
		spies[2].closeErr = errCloseB
		it := New(nil, sources...)
		it.SeekToFirst()
		err := it.Close()
		if !errors.Is(err, errCloseA) || errors.Is(err, errCloseB) {
			t.Fatalf("Close = %v, want only the first error", err)
		}
		if err := it.Close(); err != nil {
			t.Fatalf("second Close = %v, want nil", err)
		}
		for i, s := range spies {
			if s.closes != 1 {
				t.Fatalf("source %d closed %d times, want 1", i, s.closes)
			}
		}
		if it.Valid() {
			t.Fatal("a closed iterator must not be valid")
		}
		it.SeekToFirst()
		if it.Valid() || it.Err() == nil {
			t.Fatal("using a closed iterator must report an error")
		}
	})
}

type memFile struct{ bytes.Buffer }

func (*memFile) Sync() error { return nil }

func buildTable(t *testing.T, entries []kv) *sstable.Iterator {
	t.Helper()
	f := &memFile{}
	w := sstable.NewWriter(f, sstable.Options{BlockSize: 256})
	for _, e := range entries {
		if err := w.Add(e.key, e.value); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := sstable.Open(bytes.NewReader(f.Bytes()), int64(f.Len()), sstable.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return r.NewIterator()
}

func TestIteratorRealSources(t *testing.T) {
	t.Run("Should merge a skip list and two tables in order of priority", func(t *testing.T) {
		const n = 300
		list := skiplist.NewLockedList(bytes.Compare, 1)
		var mid, old, want []kv
		first := map[int]int{}
		for i := range n {
			key := []byte(fmt.Sprintf("key-%05d", i))
			first[i] = len(want)
			if i%3 == 0 {
				list.Insert(key, []byte("mem"))
				want = append(want, kv{key, []byte("mem")})
			}
			if i%2 == 0 {
				mid = append(mid, kv{key, []byte("mid")})
				want = append(want, kv{key, []byte("mid")})
			}
			old = append(old, kv{key, []byte("old")})
			want = append(want, kv{key, []byte("old")})
		}

		it := New(nil, list.NewIterator(), buildTable(t, mid), buildTable(t, old))
		defer it.Close()
		it.SeekToFirst()
		assertKV(t, drain(t, it), want)

		for _, i := range []int{0, 1, 50, 149, 298, 299} {
			it.Seek([]byte(fmt.Sprintf("key-%05d", i)))
			assertKV(t, drain(t, it), want[first[i]:])
		}
		it.Seek([]byte("zzz"))
		if it.Valid() || it.Err() != nil {
			t.Fatalf("Seek past the end: valid=%v err=%v", it.Valid(), it.Err())
		}
	})
}

func TestIteratorAllocations(t *testing.T) {
	t.Run("Should not allocate while it iterates", func(t *testing.T) {
		raw := randomSources(rand.New(rand.NewPCG(5, 5)), 8, 1000, 0.5)
		_, sources := asSources(raw)
		it := New(nil, sources...)
		scan := func() {
			it.SeekToFirst()
			for it.Valid() {
				_ = it.Key()
				_ = it.Value()
				it.Next()
			}
		}
		scan()
		if allocs := testing.AllocsPerRun(10, scan); allocs != 0 {
			t.Fatalf("got %v allocations per scan, want 0", allocs)
		}
	})
}

func FuzzIterator(f *testing.F) {
	f.Add([]byte{3, 1, 5, 2, 5, 0, 9, 1, 9}, []byte{5})
	f.Add([]byte{}, []byte{})
	f.Fuzz(func(t *testing.T, data, target []byte) {
		k := 1
		if len(data) > 0 {
			k = 1 + int(data[0])%5
			data = data[1:]
		}
		sets := make([]map[byte]bool, k)
		for i := range sets {
			sets[i] = map[byte]bool{}
		}
		for i := 0; i+1 < len(data); i += 2 {
			sets[int(data[i])%k][data[i+1]] = true
		}
		raw := make([][]kv, k)
		for i, set := range sets {
			for b := range set {
				raw[i] = append(raw[i], kv{[]byte{b}, []byte{byte(i)}})
			}
			slices.SortFunc(raw[i], func(a, b kv) int { return bytes.Compare(a.key, b.key) })
		}
		_, sources := asSources(raw)
		it := New(nil, sources...)
		it.SeekToFirst()
		assertKV(t, drain(t, it), reference(raw, nil))
		it.Seek(target)
		assertKV(t, drain(t, it), reference(raw, target))
	})
}

func BenchmarkNext(b *testing.B) {
	for _, k := range []int{2, 8, 32} {
		b.Run(fmt.Sprintf("sources=%d", k), func(b *testing.B) {
			raw := make([][]kv, k)
			for n := range 20000 {
				key := []byte(fmt.Sprintf("key-%08d", n))
				raw[n%k] = append(raw[n%k], kv{key, key})
			}
			_, sources := asSources(raw)
			it := New(nil, sources...)
			it.SeekToFirst()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if !it.Valid() {
					it.SeekToFirst()
				}
				it.Next()
			}
		})
	}
}
