package sstable

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"testing"
)

func buildBlock(entries []kv, interval int) []byte {
	bw := newBlockWriter(interval)
	for _, e := range entries {
		bw.add(e.key, e.value)
	}
	return bytes.Clone(bw.finish())
}

func collect(t *testing.T, it *blockIter) []kv {
	t.Helper()
	var out []kv
	for ; it.Valid(); it.Next() {
		out = append(out, kv{bytes.Clone(it.Key()), bytes.Clone(it.Value())})
	}
	if it.Err() != nil {
		t.Fatalf("iterator error: %v", it.Err())
	}
	return out
}

func TestBlockIter(t *testing.T) {
	for _, interval := range []int{1, 2, 16} {
		t.Run(fmt.Sprintf("Should iterate and seek every key with restart interval %d", interval), func(t *testing.T) {
			entries := numbered(200, 3)
			it, err := newBlockIter(buildBlock(entries, interval), bytes.Compare)
			if err != nil {
				t.Fatal(err)
			}
			it.SeekToFirst()
			assertEntries(t, collect(t, it), entries)

			for i, e := range entries {
				it.Seek(e.key)
				if !it.Valid() || !bytes.Equal(it.Key(), e.key) {
					t.Fatalf("Seek(%q) landed on %q", e.key, it.Key())
				}
				it.Seek(append(bytes.Clone(e.key), 0))
				if i+1 == len(entries) {
					if it.Valid() {
						t.Fatalf("Seek past the last key is valid at %q", it.Key())
					}
				} else if !it.Valid() || !bytes.Equal(it.Key(), entries[i+1].key) {
					t.Fatalf("Seek between keys landed on %q, want %q", it.Key(), entries[i+1].key)
				}
			}
			it.Seek(nil)
			if !it.Valid() || !bytes.Equal(it.Key(), entries[0].key) {
				t.Fatal("Seek before the first key must land on the first key")
			}
		})
	}

	t.Run("Should agree with a linear scan on random keys", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(1, 2))
		seen := map[string]bool{}
		var entries []kv
		for len(entries) < 500 {
			k := make([]byte, 1+rng.IntN(6))
			for i := range k {
				k[i] = byte('a' + rng.IntN(4))
			}
			if !seen[string(k)] {
				seen[string(k)] = true
				entries = append(entries, kv{k, []byte{byte(len(entries))}})
			}
		}
		sort.Slice(entries, func(i, j int) bool { return bytes.Compare(entries[i].key, entries[j].key) < 0 })
		it, err := newBlockIter(buildBlock(entries, 4), bytes.Compare)
		if err != nil {
			t.Fatal(err)
		}
		for range 2000 {
			target := make([]byte, rng.IntN(7))
			for i := range target {
				target[i] = byte('a' + rng.IntN(5))
			}
			want := sort.Search(len(entries), func(i int) bool { return bytes.Compare(entries[i].key, target) >= 0 })
			it.Seek(target)
			if want == len(entries) {
				if it.Valid() {
					t.Fatalf("Seek(%q) valid at %q, want end", target, it.Key())
				}
			} else if !it.Valid() || !bytes.Equal(it.Key(), entries[want].key) {
				t.Fatalf("Seek(%q) got %q, want %q", target, it.Key(), entries[want].key)
			}
		}
	})

	t.Run("Should treat an empty block as an empty iterator", func(t *testing.T) {
		it, err := newBlockIter(buildBlock(nil, 1), bytes.Compare)
		if err != nil {
			t.Fatal(err)
		}
		it.SeekToFirst()
		if it.Valid() || it.Err() != nil {
			t.Fatalf("SeekToFirst: valid=%v err=%v", it.Valid(), it.Err())
		}
		it.Seek([]byte("a"))
		if it.Valid() || it.Err() != nil {
			t.Fatalf("Seek: valid=%v err=%v", it.Valid(), it.Err())
		}
	})

	t.Run("Should reject malformed blocks without panicking", func(t *testing.T) {
		good := buildBlock(numbered(20, 2), 4)
		cases := map[string][]byte{
			"too short":             {1, 2},
			"zero restarts":         {0, 0, 0, 0},
			"restart count too big": {0xff, 0xff, 0, 0},
			"restart out of range":  append(bytes.Clone(good[:len(good)-8]), 0xff, 0xff, 0, 0, 4, 0, 0, 0),
		}
		for name, b := range cases {
			it, err := newBlockIter(b, bytes.Compare)
			if err == nil {
				it.SeekToFirst()
				err = it.Err()
			}
			if !errors.Is(err, errCorrupt) {
				t.Fatalf("%s: got %v, want errCorrupt", name, err)
			}
		}

		t.Run("shared prefix longer than the previous key", func(t *testing.T) {
			b := []byte{5, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0}
			it, err := newBlockIter(b, bytes.Compare)
			if err != nil {
				t.Fatal(err)
			}
			it.SeekToFirst()
			if it.Valid() || !errors.Is(it.Err(), errCorrupt) {
				t.Fatalf("valid=%v err=%v", it.Valid(), it.Err())
			}
		})
	})
}

func FuzzBlockIter(f *testing.F) {
	f.Add(buildBlock(numbered(30, 2), 4), []byte("key-00000010"))
	f.Add(buildBlock(nil, 1), []byte("x"))
	f.Fuzz(func(t *testing.T, block, target []byte) {
		it, err := newBlockIter(block, bytes.Compare)
		if err != nil {
			return
		}
		it.Seek(target)
		for steps := 0; it.Valid() && steps < len(block)+1; steps++ {
			_ = it.Key()
			_ = it.Value()
			it.Next()
		}
		it.SeekToFirst()
		for steps := 0; it.Valid() && steps < len(block)+1; steps++ {
			it.Next()
		}
	})
}
