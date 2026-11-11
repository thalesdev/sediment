// SPDX-License-Identifier: MIT

package ikey

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/thalesdev/sediment/internal/skiplist"
)

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
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

func TestAppendAndParse(t *testing.T) {
	t.Run("Should write the trailer as seq << 8 | kind in little-endian order", func(t *testing.T) {
		got := Append(nil, []byte("abc"), 5, KindPut)
		want := []byte{'a', 'b', 'c', 0x01, 0x05, 0, 0, 0, 0, 0, 0}
		if !bytes.Equal(got, want) {
			t.Fatalf("got % x, want % x", got, want)
		}
		got = Append(nil, []byte("abc"), 0x0102, KindDelete)
		want = []byte{'a', 'b', 'c', 0x00, 0x02, 0x01, 0, 0, 0, 0, 0}
		if !bytes.Equal(got, want) {
			t.Fatalf("got % x, want % x", got, want)
		}
	})

	t.Run("Should append to dst and keep what it holds", func(t *testing.T) {
		got := Append([]byte("xy"), []byte("k"), 1, KindPut)
		if !bytes.HasPrefix(got, []byte("xyk")) || len(got) != 3+TrailerSize {
			t.Fatalf("got % x", got)
		}
	})

	t.Run("Should round trip every kind with the limits of the sequence number", func(t *testing.T) {
		for _, user := range [][]byte{nil, {}, []byte("a"), []byte("user key"), bytes.Repeat([]byte{0xff}, 300)} {
			for _, seq := range []uint64{0, 1, 255, 256, 1 << 32, MaxSeq - 1, MaxSeq} {
				for _, kind := range []Kind{KindDelete, KindPut} {
					key := Append(nil, user, seq, kind)
					gotUser, gotSeq, gotKind, err := Parse(key)
					if err != nil || !bytes.Equal(gotUser, user) || gotSeq != seq || gotKind != kind {
						t.Fatalf("Parse(%q, %d, %d) = %q, %d, %d, %v", user, seq, kind, gotUser, gotSeq, gotKind, err)
					}
				}
			}
		}
	})

	t.Run("Should panic for a sequence number or kind that is not valid", func(t *testing.T) {
		mustPanic(t, func() { Append(nil, []byte("a"), MaxSeq+1, KindPut) })
		mustPanic(t, func() { Append(nil, []byte("a"), 1, Kind(2)) })
		mustPanic(t, func() { AppendLookup(nil, []byte("a"), MaxSeq+1) })
	})

	t.Run("Should return an error for a key that is shorter than the trailer", func(t *testing.T) {
		for n := range TrailerSize {
			_, _, _, err := Parse(make([]byte, n))
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("length %d: got %v, want ErrCorrupt", n, err)
			}
		}
	})

	t.Run("Should return an error for a kind that is not valid", func(t *testing.T) {
		for _, kind := range []byte{2, 3, 0x80, 0xff} {
			key := append([]byte("abc"), kind, 1, 0, 0, 0, 0, 0, 0)
			if _, _, _, err := Parse(key); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("kind %d: got %v, want ErrCorrupt", kind, err)
			}
		}
	})

	t.Run("Should return the user key and keep a short key whole", func(t *testing.T) {
		key := Append(nil, []byte("abc"), 9, KindPut)
		if got := UserKey(key); string(got) != "abc" {
			t.Fatalf("UserKey = %q", got)
		}
		if got := FilterKey(key); string(got) != "abc" {
			t.Fatalf("FilterKey = %q", got)
		}
		if got := UserKey([]byte("abc")); string(got) != "abc" {
			t.Fatalf("UserKey of a short key = %q", got)
		}
	})
}

type version struct {
	user []byte
	seq  uint64
	kind Kind
}

func referenceCompare(a, b version) int {
	if c := bytes.Compare(a.user, b.user); c != 0 {
		return c
	}
	if a.seq != b.seq {
		if a.seq > b.seq {
			return -1
		}
		return 1
	}
	if a.kind != b.kind {
		if a.kind > b.kind {
			return -1
		}
		return 1
	}
	return 0
}

func randomVersion(rng *rand.Rand) version {
	user := make([]byte, rng.IntN(4))
	for i := range user {
		user[i] = byte(rng.IntN(3))
	}
	return version{user, uint64(rng.IntN(4)) * 100, Kind(rng.IntN(2))}
}

func TestCompare(t *testing.T) {
	t.Run("Should agree with the order by user key, then sequence number and kind from high to low", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(1, 1))
		for range 5000 {
			a, b := randomVersion(rng), randomVersion(rng)
			ka := Append(nil, a.user, a.seq, a.kind)
			kb := Append(nil, b.user, b.seq, b.kind)
			if got, want := sign(Compare(ka, kb)), sign(referenceCompare(a, b)); got != want {
				t.Fatalf("Compare(%+v, %+v) = %d, want %d", a, b, got, want)
			}
		}
	})

	t.Run("Should sort the newest version of a user key first", func(t *testing.T) {
		var keys [][]byte
		for _, seq := range []uint64{3, 9, 1, 7} {
			keys = append(keys, Append(nil, []byte("k"), seq, KindPut))
		}
		slices.SortFunc(keys, Compare)
		var got []uint64
		for _, k := range keys {
			_, seq, _, _ := Parse(k)
			got = append(got, seq)
		}
		if !slices.Equal(got, []uint64{9, 7, 3, 1}) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("Should compare the user key before the trailer, unlike the order of the bytes", func(t *testing.T) {
		a := Append(nil, []byte("a"), 0xff01, KindPut)
		b := Append(nil, []byte("a\x01"), 0, KindDelete)
		if Compare(a, b) >= 0 {
			t.Fatalf("Compare = %d, want a negative number", Compare(a, b))
		}
		if bytes.Compare(a, b) <= 0 {
			t.Fatal("the test needs keys for which the order of the bytes differs")
		}
		if Compare(Append(nil, []byte("a"), 1, KindPut), Append(nil, []byte("ab"), 1, KindPut)) >= 0 {
			t.Fatal("a must sort before ab")
		}
	})

	t.Run("Should have a total order for keys of any length", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(2, 2))
		keys := [][]byte{nil, []byte("abc"), append([]byte("abc"), make([]byte, 8)...)}
		for range 60 {
			k := make([]byte, rng.IntN(14))
			for i := range k {
				k[i] = byte(rng.IntN(3))
			}
			keys = append(keys, k)
		}
		for _, a := range keys {
			for _, b := range keys {
				ab, ba := Compare(a, b), Compare(b, a)
				if sign(ab) != -sign(ba) {
					t.Fatalf("Compare(%x, %x) = %d but the reverse is %d", a, b, ab, ba)
				}
				if (ab == 0) != bytes.Equal(a, b) {
					t.Fatalf("Compare(%x, %x) = %d with equal=%v", a, b, ab, bytes.Equal(a, b))
				}
				for _, c := range keys {
					if ab <= 0 && Compare(b, c) <= 0 && Compare(a, c) > 0 {
						t.Fatalf("order is not transitive for %x, %x, %x", a, b, c)
					}
				}
			}
		}
	})

	t.Run("Should not give 0 for a short key and a valid key with the same bytes in front", func(t *testing.T) {
		short := []byte("abc")
		valid := append([]byte("abc"), make([]byte, 8)...)
		if Compare(short, valid) == 0 || Compare(valid, short) == 0 {
			t.Fatal("different keys must not compare as equal")
		}
	})

	t.Run("Should not allocate", func(t *testing.T) {
		a := Append(nil, []byte("user-key-a"), 7, KindPut)
		b := Append(nil, []byte("user-key-a"), 5, KindPut)
		if allocs := testing.AllocsPerRun(100, func() { Compare(a, b) }); allocs != 0 {
			t.Fatalf("got %v allocations, want 0", allocs)
		}
	})
}

func TestLookup(t *testing.T) {
	list := skiplist.NewLockedList(Compare, 1)
	insert := func(user string, seq uint64, kind Kind) {
		list.Insert(Append(nil, []byte(user), seq, kind), []byte(user))
	}
	insert("a", 2, KindPut)
	insert("b", 3, KindPut)
	insert("b", 7, KindDelete)
	insert("b", 9, KindPut)
	insert("c", 4, KindPut)

	find := func(user string, snapshot uint64) (string, uint64, Kind) {
		it := list.NewIterator()
		it.Seek(AppendLookup(nil, []byte(user), snapshot))
		if !it.Valid() {
			return "", 0, 0
		}
		u, seq, kind, err := Parse(it.Key())
		if err != nil {
			t.Fatal(err)
		}
		return string(u), seq, kind
	}

	for _, tc := range []struct {
		name     string
		snapshot uint64
		user     string
		seq      uint64
		kind     Kind
	}{
		{"before every version of the key", 2, "c", 4, KindPut},
		{"at the oldest version", 3, "b", 3, KindPut},
		{"between two versions", 6, "b", 3, KindPut},
		{"at a delete", 7, "b", 7, KindDelete},
		{"after a delete", 8, "b", 7, KindDelete},
		{"at the newest version", 9, "b", 9, KindPut},
		{"at the largest sequence number", MaxSeq, "b", 9, KindPut},
	} {
		t.Run("Should find the version for a snapshot "+tc.name, func(t *testing.T) {
			user, seq, kind := find("b", tc.snapshot)
			if user != tc.user || seq != tc.seq || kind != tc.kind {
				t.Fatalf("got %s/%d/%d, want %s/%d/%d", user, seq, kind, tc.user, tc.seq, tc.kind)
			}
		})
	}

	t.Run("Should reach the end of the list for a user key that is after all the keys", func(t *testing.T) {
		if user, _, _ := find("d", MaxSeq); user != "" {
			t.Fatalf("got %q, want the end of the list", user)
		}
	})
}

func FuzzCompare(f *testing.F) {
	f.Add([]byte("abc"), append([]byte("abc"), make([]byte, 8)...), []byte{})
	f.Add(Append(nil, []byte("a"), 1, KindPut), Append(nil, []byte("a"), 2, KindDelete), Append(nil, []byte("b"), 0, KindPut))
	f.Fuzz(func(t *testing.T, a, b, c []byte) {
		ab, ba := Compare(a, b), Compare(b, a)
		if sign(ab) != -sign(ba) {
			t.Fatalf("Compare(%x, %x) = %d but the reverse is %d", a, b, ab, ba)
		}
		if (ab == 0) != bytes.Equal(a, b) {
			t.Fatalf("Compare(%x, %x) = %d with equal=%v", a, b, ab, bytes.Equal(a, b))
		}
		if ab <= 0 && Compare(b, c) <= 0 && Compare(a, c) > 0 {
			t.Fatalf("order is not transitive for %x, %x, %x", a, b, c)
		}
	})
}

func FuzzParse(f *testing.F) {
	f.Add([]byte{})
	f.Add(Append(nil, []byte("key"), 42, KindDelete))
	f.Fuzz(func(t *testing.T, key []byte) {
		user, seq, kind, err := Parse(key)
		if err != nil {
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("got %v, want ErrCorrupt", err)
			}
			return
		}
		if got := Append(nil, user, seq, kind); !bytes.Equal(got, key) {
			t.Fatalf("round trip gave % x, want % x", got, key)
		}
	})
}

func BenchmarkCompare(b *testing.B) {
	keys := make([][]byte, 1024)
	rng := rand.New(rand.NewPCG(3, 3))
	for i := range keys {
		user := []byte("user-key-00000000")
		user[len(user)-1] = byte('0' + rng.IntN(10))
		keys[i] = Append(nil, user, uint64(rng.IntN(1000)), KindPut)
	}
	b.ReportAllocs()
	for i := range b.N {
		Compare(keys[i%len(keys)], keys[(i*7+1)%len(keys)])
	}
}
