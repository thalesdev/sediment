// SPDX-License-Identifier: MIT

package shale

import (
	"math/rand"
	"testing"
)

func tomb(start, end string, seq uint64) rangeTomb {
	return rangeTomb{start: []byte(start), end: []byte(end), seq: seq}
}

func naiveCover(tombs []rangeTomb, user string, snap uint64) uint64 {
	var best uint64
	for _, t := range tombs {
		if user >= string(t.start) && user < string(t.end) && t.seq <= snap && t.seq > best {
			best = t.seq
		}
	}
	return best
}

func TestTombIndex(t *testing.T) {
	t.Run("Should cover nothing when it has no range delete", func(t *testing.T) {
		var x *tombIndex
		if x.maxCover([]byte("a"), 100) != 0 {
			t.Fatal("a nil index covers a key")
		}
		if newTombIndex(nil) != nil {
			t.Fatal("an index of nothing should be nil")
		}
	})

	t.Run("Should cover the start and not the end of a range", func(t *testing.T) {
		x := newTombIndex([]rangeTomb{tomb("b", "d", 5)})
		for key, want := range map[string]uint64{"a": 0, "b": 5, "bz": 5, "c": 5, "d": 0, "e": 0} {
			if got := x.maxCover([]byte(key), 100); got != want {
				t.Fatalf("maxCover(%q) = %d, want %d", key, got, want)
			}
		}
	})

	t.Run("Should only see the range deletes that are not above the snapshot", func(t *testing.T) {
		x := newTombIndex([]rangeTomb{tomb("a", "z", 3), tomb("a", "z", 8), tomb("c", "e", 6)})
		for snap, want := range map[uint64]uint64{2: 0, 3: 3, 5: 3, 6: 3, 7: 3, 8: 8, 100: 8} {
			if got := x.maxCover([]byte("a"), snap); got != want {
				t.Fatalf("snap %d: got %d, want %d", snap, got, want)
			}
		}
		if got := x.maxCover([]byte("d"), 7); got != 6 {
			t.Fatalf("got %d, want 6", got)
		}
	})

	t.Run("Should give a range of one key that the empty key starts", func(t *testing.T) {
		x := newTombIndex([]rangeTomb{{start: nil, end: []byte{0}, seq: 4}})
		if x.maxCover(nil, 10) != 4 || x.maxCover([]byte{0}, 10) != 0 {
			t.Fatal("wrong cover for the empty key")
		}
	})

	t.Run("Should agree with a scan of all the range deletes", func(t *testing.T) {
		rng := rand.New(rand.NewSource(1))
		key := func() string { return string([]byte{byte('a' + rng.Intn(12))}) }
		for range 300 {
			var tombs []rangeTomb
			for i := range 1 + rng.Intn(12) {
				a, b := key(), key()
				if a > b {
					a, b = b, a
				}
				if a == b {
					b += "z"
				}
				tombs = append(tombs, tomb(a, b, uint64(1+i*2+rng.Intn(2))))
			}
			x := newTombIndex(tombs)
			for c := byte('a'); c <= 'n'; c++ {
				for _, suffix := range []string{"", "a", "zz"} {
					user := string([]byte{c}) + suffix
					for snap := uint64(0); snap < 30; snap += 3 {
						if got, want := x.maxCover([]byte(user), snap), naiveCover(tombs, user, snap); got != want {
							t.Fatalf("maxCover(%q, %d) = %d, want %d with %+v", user, snap, got, want, tombs)
						}
					}
				}
			}
		}
	})

	t.Run("Should give the bounds of a table that has only range deletes", func(t *testing.T) {
		smallest, largest := tombBounds([]rangeTomb{tomb("m", "q", 3), tomb("b", "c", 4), tomb("x", "y", 5)})
		if string(smallest[:len(smallest)-8]) != "b" || string(largest[:len(largest)-8]) != "y" {
			t.Fatalf("got %q and %q", smallest, largest)
		}
	})
}
