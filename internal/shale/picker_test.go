// SPDX-License-Identifier: MIT

package shale

import (
	"math/rand/v2"
	"testing"
)

func infos(sizes ...uint64) []tableInfo {
	out := make([]tableInfo, len(sizes))
	for i, s := range sizes {
		out[i] = tableInfo{size: s}
	}
	return out
}

func repeat(size uint64, n int) []uint64 {
	out := make([]uint64, n)
	for i := range out {
		out[i] = size
	}
	return out
}

func exponential(n int) []uint64 {
	out := make([]uint64, n)
	size := uint64(10)
	for i := range out {
		out[i] = size
		size *= 3
	}
	return out
}

func TestPickCompaction(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		sizes []uint64
		want  plan
		ok    bool
	}{
		{"four tables of the same size", repeat(10, 4), plan{start: 0, end: 4, includesOldest: true}, true},
		{"five tables of the same size", repeat(10, 5), plan{start: 0, end: 5, includesOldest: true}, true},
		{"fewer than four tables", repeat(10, 3), plan{}, false},
		{"no table", nil, plan{}, false},
		{"sizes that grow slowly", []uint64{10, 15, 20, 40, 80}, plan{start: 0, end: 5, includesOldest: true}, true},
		{"a large table that cuts a run that is too short", []uint64{10, 10, 10, 1000, 10, 10}, plan{}, false},
		{"a large table that cuts a run that is long enough", []uint64{10, 10, 10, 10, 1000, 10}, plan{start: 0, end: 4, includesOldest: false}, true},
		{"a table that is more than twice the sum", []uint64{10, 50, 50, 50, 50}, plan{}, false},
		{"a table that is exactly twice the sum", []uint64{10, 20, 10, 10}, plan{start: 0, end: 4, includesOldest: true}, true},
		{"a run limited to sixteen tables", repeat(10, 20), plan{start: 0, end: 16, includesOldest: false}, true},
		{"sixteen tables of the same size", repeat(10, 16), plan{start: 0, end: 16, includesOldest: true}, true},
		{"twenty four tables of sizes that grow fast", exponential(24), plan{}, false},
		{"twenty five tables of sizes that grow fast", exponential(25), plan{start: 0, end: 8, includesOldest: false, byCount: true}, true},
		{"thirty tables of sizes that grow fast", exponential(30), plan{start: 0, end: 8, includesOldest: false, byCount: true}, true},
		{"the rule by size goes before the rule by count", repeat(10, 30), plan{start: 0, end: 16, includesOldest: false}, true},
	}
	for _, tc := range cases {
		t.Run("Should choose for "+tc.name, func(t *testing.T) {
			got, ok := pickCompaction(infos(tc.sizes...))
			if ok != tc.ok || got != tc.want {
				t.Fatalf("got %+v, %v, want %+v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}

	t.Run("Should always choose a run that starts at the newest table and does not go past the list", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(1, 1))
		for range 5000 {
			sizes := make([]uint64, rng.IntN(40))
			for i := range sizes {
				sizes[i] = uint64(1 + rng.IntN(1000))
			}
			got, ok := pickCompaction(infos(sizes...))
			if !ok {
				if len(sizes) > tableCountTrigger {
					t.Fatalf("no plan for %d tables", len(sizes))
				}
				continue
			}
			if got.start != 0 || got.end > len(sizes) || got.end < minRun || got.end > maxRun {
				t.Fatalf("plan %+v for %d tables", got, len(sizes))
			}
			if got.includesOldest != (got.end == len(sizes)) {
				t.Fatalf("plan %+v for %d tables", got, len(sizes))
			}
		}
	})
}
