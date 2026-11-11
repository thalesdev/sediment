// SPDX-License-Identifier: MIT

package sstable

import (
	"bytes"
	"fmt"
	"slices"
	"testing"

	"github.com/thalesdev/sediment/internal/ikey"
)

type versioned struct {
	key   []byte
	value []byte
}

func versionedEntries(users int) []versioned {
	seqs := []uint64{12, 10, 8, 6, 4, 2}
	var out []versioned
	for i := range users {
		user := fmt.Appendf(nil, "user-%04d", i)
		for _, seq := range seqs {
			kind := ikey.KindPut
			if seq == 8 && i%2 == 0 {
				kind = ikey.KindDelete
			}
			out = append(out, versioned{ikey.Append(nil, user, seq, kind), fmt.Appendf(nil, "%s/%d", user, seq)})
		}
	}
	return out
}

func internalOptions() Options {
	return Options{BlockSize: 48, Compare: ikey.Compare, FilterKey: ikey.FilterKey}
}

func writeVersioned(t *testing.T, entries []versioned) []byte {
	t.Helper()
	kvs := make([]kv, len(entries))
	for i, e := range entries {
		kvs[i] = kv{e.key, e.value}
	}
	return writeTable(t, internalOptions(), kvs)
}

func openVersioned(t *testing.T, src *countingReaderAt) *Reader {
	t.Helper()
	r, err := Open(src, int64(len(src.b)), internalOptions())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return r
}

func firstAtOrAfter(entries []versioned, key []byte) (versioned, bool) {
	i, _ := slices.BinarySearchFunc(entries, key, func(e versioned, k []byte) int { return ikey.Compare(e.key, k) })
	if i == len(entries) {
		return versioned{}, false
	}
	return entries[i], true
}

func TestReaderFind(t *testing.T) {
	entries := versionedEntries(60)
	src := &countingReaderAt{b: writeVersioned(t, entries)}
	r := openVersioned(t, src)
	blocks := len(parseTable(t, src.b).dataBlocks)

	t.Run("Should spread the versions of a user key over more than one block", func(t *testing.T) {
		if blocks <= 60 {
			t.Fatalf("table has %d data blocks, want more than the 60 user keys", blocks)
		}
	})

	t.Run("Should find the newest version that a snapshot can see", func(t *testing.T) {
		for i := range 60 {
			user := fmt.Appendf(nil, "user-%04d", i)
			for _, snapshot := range []uint64{0, 1, 2, 3, 5, 7, 8, 9, 11, 12, 13, ikey.MaxSeq} {
				lookup := ikey.AppendLookup(nil, user, snapshot)
				want, ok := firstAtOrAfter(entries, lookup)
				key, value, found, err := r.Find(lookup)
				if err != nil {
					t.Fatalf("Find(%s, %d): %v", user, snapshot, err)
				}
				if !found {
					if ok && bytes.Equal(ikey.UserKey(want.key), user) {
						t.Fatalf("Find(%s, %d) found nothing, want %q", user, snapshot, want.key)
					}
					continue
				}
				if !bytes.Equal(key, want.key) || !bytes.Equal(value, want.value) {
					t.Fatalf("Find(%s, %d) = %x, want %x", user, snapshot, key, want.key)
				}
			}
		}
	})

	t.Run("Should not return an entry of the user key for a user key that is not in the table", func(t *testing.T) {
		for _, absent := range []string{"a", "user-", "user-0030x", "user-00300", "user-0059z", "zzz", ""} {
			lookup := ikey.AppendLookup(nil, []byte(absent), ikey.MaxSeq)
			key, _, found, err := r.Find(lookup)
			if err != nil {
				t.Fatalf("Find(%q): %v", absent, err)
			}
			if found && bytes.Equal(ikey.UserKey(key), []byte(absent)) {
				t.Fatalf("Find(%q) returned an entry of that user key", absent)
			}
		}
	})

	t.Run("Should read one data block for a user key in the table", func(t *testing.T) {
		src.reads = nil
		if _, _, found, err := r.Find(ikey.AppendLookup(nil, []byte("user-0031"), ikey.MaxSeq)); !found || err != nil {
			t.Fatalf("Find: %v %v", found, err)
		}
		if len(src.reads) != 1 {
			t.Fatalf("read %d blocks, want 1", len(src.reads))
		}
	})

	t.Run("Should read no data block for most user keys that are not in the table", func(t *testing.T) {
		src.reads = nil
		const misses = 500
		for i := range misses {
			lookup := ikey.AppendLookup(nil, fmt.Appendf(nil, "user-%04d-x", i%60), ikey.MaxSeq)
			if _, _, found, err := r.Find(lookup); err != nil {
				t.Fatal(err)
			} else if found {
				continue
			}
		}
		if len(src.reads) > misses/10 {
			t.Fatalf("the filter avoided the read in only %d of %d lookups", misses-len(src.reads), misses)
		}
	})

	t.Run("Should return the same result from Get as before for exact keys", func(t *testing.T) {
		for _, e := range entries {
			v, ok, err := r.Get(e.key)
			if err != nil || !ok || !bytes.Equal(v, e.value) {
				t.Fatalf("Get(%x) = %q, %v, %v", e.key, v, ok, err)
			}
		}
		lookup := ikey.AppendLookup(nil, []byte("user-0007"), 5)
		if _, ok, err := r.Get(lookup); ok || err != nil {
			t.Fatalf("Get of a lookup key without an exact entry = %v, %v", ok, err)
		}
	})
}
