// SPDX-License-Identifier: MIT

package sstable

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"testing"
)

func TestBlockWriter(t *testing.T) {
	t.Run("Should compress shared prefixes and restart every interval", func(t *testing.T) {
		bw := newBlockWriter(2)
		keys := []string{"apple", "applet", "apply", "apricot", "banana"}
		for _, k := range keys {
			bw.add([]byte(k), []byte("v"))
		}
		entries, restarts := decodeBlock(t, bw.finish())
		wantShared := []int{0, 5, 0, 2, 0}
		for i, e := range entries {
			if string(e.key) != keys[i] || e.shared != wantShared[i] {
				t.Fatalf("entry %d: got key %q shared %d, want %q shared %d", i, e.key, e.shared, keys[i], wantShared[i])
			}
		}
		wantRestarts := []uint32{0, uint32(entries[2].offset), uint32(entries[4].offset)}
		if fmt.Sprint(restarts) != fmt.Sprint(wantRestarts) {
			t.Fatalf("got restarts %v, want %v", restarts, wantRestarts)
		}
	})

	t.Run("Should point every restart at an entry with no shared prefix", func(t *testing.T) {
		bw := newBlockWriter(16)
		for _, e := range numbered(1000, 3) {
			bw.add(e.key, e.value)
		}
		entries, restarts := decodeBlock(t, bw.finish())
		if want := (1000 + 15) / 16; len(restarts) != want {
			t.Fatalf("got %d restarts, want %d", len(restarts), want)
		}
		byOffset := map[uint32]entry{}
		for _, e := range entries {
			byOffset[uint32(e.offset)] = e
		}
		for i, r := range restarts {
			e, ok := byOffset[r]
			if !ok || e.shared != 0 {
				t.Fatalf("restart %d at %d is not a full-key entry", i, r)
			}
			if i > 0 && r <= restarts[i-1] {
				t.Fatalf("restarts not increasing at %d", i)
			}
		}
	})

	t.Run("Should make every entry a restart with interval one", func(t *testing.T) {
		bw := newBlockWriter(1)
		for _, e := range numbered(50, 1) {
			bw.add(e.key, e.value)
		}
		entries, restarts := decodeBlock(t, bw.finish())
		if len(restarts) != len(entries) {
			t.Fatalf("got %d restarts for %d entries", len(restarts), len(entries))
		}
		for _, e := range entries {
			if e.shared != 0 {
				t.Fatalf("entry %q has shared %d, want 0", e.key, e.shared)
			}
		}
	})

	t.Run("Should round trip binary keys and empty values", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(1, 1))
		var want []kv
		seen := map[string]bool{}
		for len(want) < 2000 {
			k := make([]byte, rng.IntN(40))
			for i := range k {
				k[i] = byte(rng.IntN(4))
			}
			if !seen[string(k)] {
				seen[string(k)] = true
				want = append(want, kv{k, nil})
			}
		}
		for i := 1; i < len(want); i++ {
			for j := i; j > 0 && bytes.Compare(want[j-1].key, want[j].key) > 0; j-- {
				want[j-1], want[j] = want[j], want[j-1]
			}
		}
		bw := newBlockWriter(16)
		for _, e := range want {
			bw.add(e.key, e.value)
		}
		entries, _ := decodeBlock(t, bw.finish())
		got := make([]kv, len(entries))
		for i, e := range entries {
			got[i] = kv{e.key, e.value}
		}
		assertEntries(t, got, want)
	})

	t.Run("Should produce the same bytes after reset as a fresh writer", func(t *testing.T) {
		reused := newBlockWriter(4)
		for _, e := range numbered(37, 5) {
			reused.add(e.key, e.value)
		}
		reused.finish()
		reused.reset()

		fresh := newBlockWriter(4)
		second := []kv{{[]byte("zeta"), []byte("1")}, {[]byte("zetta"), []byte("2")}, {[]byte("zz"), []byte("3")}, {[]byte("zzz"), []byte("4")}, {[]byte("zzzz"), []byte("5")}}
		for _, e := range second {
			reused.add(e.key, e.value)
			fresh.add(e.key, e.value)
		}
		if !bytes.Equal(reused.finish(), fresh.finish()) {
			t.Fatal("block written after reset differs from a fresh block")
		}
	})

	t.Run("Should estimate exactly the finished size", func(t *testing.T) {
		bw := newBlockWriter(16)
		for i, e := range numbered(200, 7) {
			bw.add(e.key, e.value)
			if i%37 == 0 {
				want := bw.estimatedSize()
				clone := *bw
				clone.buf = bytes.Clone(bw.buf)
				if got := len(clone.finish()); got != want {
					t.Fatalf("after %d entries: estimated %d, finished %d", i+1, want, got)
				}
			}
		}
	})

	t.Run("Should finish an empty block with a single restart", func(t *testing.T) {
		got := newBlockWriter(16).finish()
		want := []byte{0, 0, 0, 0, 1, 0, 0, 0}
		if !bytes.Equal(got, want) {
			t.Fatalf("got % x, want % x", got, want)
		}
	})
}

func naivePrefix(a, b []byte) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func TestCommonPrefixLength(t *testing.T) {
	t.Run("Should match a byte by byte comparison", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(9, 9))
		for range 20000 {
			a := make([]byte, rng.IntN(40))
			for i := range a {
				a[i] = byte(rng.IntN(3))
			}
			b := bytes.Clone(a[:rng.IntN(len(a)+1)])
			for range rng.IntN(20) {
				b = append(b, byte(rng.IntN(3)))
			}
			if rng.IntN(2) == 0 && len(b) > 0 {
				b[rng.IntN(len(b))] ^= byte(1 << rng.IntN(8))
			}
			bw := &blockWriter{lastKey: a}
			if got, want := bw.commonPrefixLength(b), naivePrefix(a, b); got != want {
				t.Fatalf("commonPrefixLength(%x, %x) = %d, want %d", a, b, got, want)
			}
		}
	})
}

func FuzzCommonPrefixLength(f *testing.F) {
	f.Add([]byte("abcdefghijk"), []byte("abcdefghXjk"))
	f.Add([]byte("12345678"), []byte("12345679"))
	f.Add([]byte{}, []byte("x"))
	f.Fuzz(func(t *testing.T, a, b []byte) {
		bw := &blockWriter{lastKey: a}
		if got, want := bw.commonPrefixLength(b), naivePrefix(a, b); got != want {
			t.Fatalf("commonPrefixLength(%x, %x) = %d, want %d", a, b, got, want)
		}
	})
}
