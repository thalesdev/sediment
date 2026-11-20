// SPDX-License-Identifier: MIT

package shale

import (
	"bytes"
	"slices"
	"sort"

	"github.com/thalesdev/sediment/internal/ikey"
)

// tombOverhead is the number of bytes that a range delete counts in the size of a
// memtable, besides its two keys.
const tombOverhead = 64

// rangeTomb is a range delete: every key from start up to, and not including,
// end, with a sequence number below seq, is deleted. A key with a sequence number
// above seq is not.
type rangeTomb struct {
	start []byte
	end   []byte
	seq   uint64
}

// tombKey returns the key that orders t in the block of range deletes of a table:
// the internal key of its start, with its sequence number and the kind delete.
func tombKey(t rangeTomb) []byte {
	return ikey.Append(nil, t.start, t.seq, ikey.KindDelete)
}

// sortTombs sorts tombs in the order of their keys in a table: by start, and for
// the same start with the newest first.
func sortTombs(tombs []rangeTomb) {
	slices.SortFunc(tombs, func(a, b rangeTomb) int {
		if c := bytes.Compare(a.start, b.start); c != 0 {
			return c
		}
		switch {
		case a.seq > b.seq:
			return -1
		case a.seq < b.seq:
			return 1
		}
		return 0
	})
}

// tombBounds returns the first and the last internal key that a table that has
// only the range deletes tombs can name. The first is the lowest start, and the
// last is the highest end, as internal keys. tombs must not be empty.
func tombBounds(tombs []rangeTomb) (smallest, largest []byte) {
	lo, hi := tombs[0].start, tombs[0].end
	for _, t := range tombs[1:] {
		if bytes.Compare(t.start, lo) < 0 {
			lo = t.start
		}
		if bytes.Compare(t.end, hi) > 0 {
			hi = t.end
		}
	}
	return ikey.Append(nil, lo, ikey.MaxSeq, ikey.KindPut), ikey.Append(nil, hi, 0, ikey.KindDelete)
}

// tombFrag is a piece of the key space that the same range deletes cover. seqs
// holds the sequence numbers of those range deletes, the newest first.
type tombFrag struct {
	start, end []byte
	seqs       []uint64
}

// tombIndex answers which range deletes cover a key. It holds fragments that do
// not overlap, sorted by start. It does not change after it is built, so any
// number of readers can use it. A nil tombIndex covers nothing.
type tombIndex struct {
	frags []tombFrag
	count int
}

// newTombIndex builds the index of tombs. It returns nil if there are none.
func newTombIndex(tombs []rangeTomb) *tombIndex {
	if len(tombs) == 0 {
		return nil
	}
	sorted := slices.Clone(tombs)
	sortTombs(sorted)
	bounds := make([][]byte, 0, 2*len(sorted))
	for _, t := range sorted {
		bounds = append(bounds, t.start, t.end)
	}
	slices.SortFunc(bounds, bytes.Compare)
	bounds = slices.CompactFunc(bounds, bytes.Equal)

	x := &tombIndex{count: len(tombs)}
	var active []rangeTomb
	next := 0
	for i := 0; i+1 < len(bounds); i++ {
		b := bounds[i]
		for next < len(sorted) && bytes.Compare(sorted[next].start, b) <= 0 {
			active = append(active, sorted[next])
			next++
		}
		active = slices.DeleteFunc(active, func(t rangeTomb) bool { return bytes.Compare(t.end, b) <= 0 })
		if len(active) == 0 {
			continue
		}
		seqs := make([]uint64, len(active))
		for j, t := range active {
			seqs[j] = t.seq
		}
		slices.SortFunc(seqs, func(a, b uint64) int {
			switch {
			case a > b:
				return -1
			case a < b:
				return 1
			}
			return 0
		})
		x.frags = append(x.frags, tombFrag{start: b, end: bounds[i+1], seqs: seqs})
	}
	return x
}

// maxCover returns the largest sequence number, not above snap, of a range delete
// that covers user, or 0 if none does. A point version of user with a sequence
// number below it is deleted.
func (x *tombIndex) maxCover(user []byte, snap uint64) uint64 {
	if x == nil {
		return 0
	}
	i := sort.Search(len(x.frags), func(i int) bool { return bytes.Compare(x.frags[i].start, user) > 0 })
	if i == 0 {
		return 0
	}
	f := x.frags[i-1]
	if bytes.Compare(user, f.end) >= 0 {
		return 0
	}
	for _, s := range f.seqs {
		if s <= snap {
			return s
		}
	}
	return 0
}
