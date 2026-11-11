// SPDX-License-Identifier: MIT

package compat

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/comparer"
	"github.com/syndtr/goleveldb/leveldb/errors"
	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"github.com/syndtr/goleveldb/leveldb/table"
	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/thalesdev/sediment/internal/ikey"
	"github.com/thalesdev/sediment/internal/sstable"
)

type internalComparer struct{}

func (internalComparer) Compare(a, b []byte) int           { return ikey.Compare(a, b) }
func (internalComparer) Name() string                      { return "sediment.InternalKeyComparer" }
func (internalComparer) Separator(dst, a, b []byte) []byte { return nil }
func (internalComparer) Successor(dst, b []byte) []byte    { return nil }

var _ comparer.Comparer = internalComparer{}

type internalFilter struct{ filter.Filter }

func (f internalFilter) NewGenerator() filter.FilterGenerator {
	return internalGenerator{f.Filter.NewGenerator()}
}

func (f internalFilter) Contains(fb, key []byte) bool {
	return f.Filter.Contains(fb, ikey.UserKey(key))
}

type internalGenerator struct{ filter.FilterGenerator }

func (g internalGenerator) Add(key []byte) { g.FilterGenerator.Add(ikey.UserKey(key)) }

func internalLevelOptions() *opt.Options {
	return &opt.Options{
		BlockSize:            256,
		BlockRestartInterval: 16,
		Compression:          opt.NoCompression,
		Comparer:             internalComparer{},
		Filter:               internalFilter{filter.NewBloomFilter(10)},
	}
}

func ourInternalOptions() sstable.Options {
	return sstable.Options{BlockSize: 256, Compare: ikey.Compare, FilterKey: ikey.FilterKey}
}

type version struct {
	user  []byte
	seq   uint64
	kind  ikey.Kind
	value []byte
}

func (v version) key() []byte { return ikey.Append(nil, v.user, v.seq, v.kind) }

func randomUsers(rng *rand.Rand, n int) [][]byte {
	seen := map[string]bool{}
	var out [][]byte
	for len(out) < n {
		k := make([]byte, 1+rng.IntN(14))
		for i := range k {
			k[i] = byte('a' + rng.IntN(6))
		}
		if !seen[string(k)] {
			seen[string(k)] = true
			out = append(out, k)
		}
	}
	slices.SortFunc(out, bytes.Compare)
	return out
}

func randomVersions(rng *rand.Rand, users [][]byte) []version {
	var out []version
	for _, user := range users {
		seqs := map[uint64]bool{}
		for range 1 + rng.IntN(6) {
			seqs[uint64(1+rng.IntN(1000))] = true
		}
		sorted := make([]uint64, 0, len(seqs))
		for s := range seqs {
			sorted = append(sorted, s)
		}
		slices.SortFunc(sorted, func(a, b uint64) int { return int(b) - int(a) })
		for _, seq := range sorted {
			v := version{user: user, seq: seq, kind: ikey.KindPut, value: fmt.Appendf(nil, "%s@%d", user, seq)}
			if rng.IntN(5) == 0 {
				v.kind, v.value = ikey.KindDelete, nil
			}
			out = append(out, v)
		}
	}
	return out
}

func toKV(versions []version) []kv {
	out := make([]kv, len(versions))
	for i, v := range versions {
		out[i] = kv{v.key(), v.value}
	}
	return out
}

func firstAtOrAfter(versions []version, lookup []byte) (version, bool) {
	i, _ := slices.BinarySearchFunc(versions, lookup, func(v version, k []byte) int { return ikey.Compare(v.key(), k) })
	if i == len(versions) {
		return version{}, false
	}
	return versions[i], true
}

func probeUsers(users [][]byte) [][]byte {
	probes := slices.Clone(users)
	for _, u := range users {
		probes = append(probes, append(bytes.Clone(u), 0), u[:len(u)-1], append(bytes.Clone(u), 'z'))
	}
	return append(probes, nil, []byte("zzzz"), []byte{0})
}

func checkFind(t *testing.T, find func(lookup []byte) (key, value []byte, found bool), versions []version, users [][]byte) {
	t.Helper()
	present := map[string]bool{}
	for _, u := range users {
		present[string(u)] = true
	}
	for _, probe := range probeUsers(users) {
		for _, snapshot := range []uint64{0, 1, 2, 3, 7, 50, 333, 999, 1000, ikey.MaxSeq} {
			lookup := ikey.AppendLookup(nil, probe, snapshot)
			want, ok := firstAtOrAfter(versions, lookup)
			key, value, found := find(lookup)
			if !present[string(probe)] {
				if found && bytes.Equal(ikey.UserKey(key), probe) {
					t.Fatalf("Find(%q, %d) returned an entry of an absent user key", probe, snapshot)
				}
				continue
			}
			if !found {
				if ok && bytes.Equal(want.user, probe) {
					t.Fatalf("Find(%q, %d) found nothing, want %x", probe, snapshot, want.key())
				}
				continue
			}
			if !ok || !bytes.Equal(key, want.key()) || !bytes.Equal(value, want.value) {
				t.Fatalf("Find(%q, %d) = %x, want %x", probe, snapshot, key, want.key())
			}
		}
	}
}

func TestGoleveldbReadsOurInternalKeyTables(t *testing.T) {
	rng := rand.New(rand.NewPCG(21, 22))
	users := randomUsers(rng, 300)
	versions := randomVersions(rng, users)
	entries := toKV(versions)

	f := &memFile{}
	w := sstable.NewWriter(f, ourInternalOptions())
	for _, e := range entries {
		if err := w.Add(e.key, e.value); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	b := f.Bytes()
	r, err := table.NewReader(bytes.NewReader(b), int64(len(b)), storage.FileDesc{Type: storage.TypeTable, Num: 1}, nil, util.NewBufferPool(4096), internalLevelOptions())
	if err != nil {
		t.Fatalf("goleveldb could not open the table: %v", err)
	}

	t.Run("Should iterate every version in order", func(t *testing.T) {
		it := r.NewIterator(nil, nil)
		defer it.Release()
		i := 0
		for it.Next() {
			if i >= len(entries) || !bytes.Equal(it.Key(), entries[i].key) || !bytes.Equal(it.Value(), entries[i].value) {
				t.Fatalf("entry %d differs", i)
			}
			i++
		}
		if err := it.Error(); err != nil || i != len(entries) {
			t.Fatalf("got %d entries and error %v, want %d", i, err, len(entries))
		}
	})

	t.Run("Should find the version for each snapshot, with the filter on the user key", func(t *testing.T) {
		checkFind(t, func(lookup []byte) ([]byte, []byte, bool) {
			key, value, err := r.Find(lookup, true, nil)
			if err == errors.ErrNotFound {
				return nil, nil, false
			}
			if err != nil {
				t.Fatalf("Find: %v", err)
			}
			return key, value, true
		}, versions, users)
	})
}

func TestOurReaderReadsGoleveldbInternalKeyTables(t *testing.T) {
	rng := rand.New(rand.NewPCG(31, 32))
	users := randomUsers(rng, 300)
	versions := randomVersions(rng, users)
	entries := toKV(versions)

	var buf bytes.Buffer
	w := table.NewWriter(&buf, internalLevelOptions())
	for _, e := range entries {
		if err := w.Append(e.key, e.value); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	r, err := sstable.Open(bytes.NewReader(b), int64(len(b)), ourInternalOptions())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	t.Run("Should iterate every version in order", func(t *testing.T) {
		it := r.NewIterator()
		defer it.Close()
		i := 0
		for it.SeekToFirst(); it.Valid(); it.Next() {
			if i >= len(entries) || !bytes.Equal(it.Key(), entries[i].key) || !bytes.Equal(it.Value(), entries[i].value) {
				t.Fatalf("entry %d differs", i)
			}
			i++
		}
		if err := it.Err(); err != nil || i != len(entries) {
			t.Fatalf("got %d entries and error %v, want %d", i, err, len(entries))
		}
	})

	t.Run("Should find the version for each snapshot, with the filter on the user key", func(t *testing.T) {
		checkFind(t, func(lookup []byte) ([]byte, []byte, bool) {
			key, value, found, err := r.Find(lookup)
			if err != nil {
				t.Fatalf("Find: %v", err)
			}
			return key, value, found
		}, versions, users)
	})
}

type spyReaderAt struct {
	b     []byte
	reads int
}

func (s *spyReaderAt) ReadAt(p []byte, off int64) (int, error) {
	s.reads++
	if off >= int64(len(s.b)) {
		return 0, io.EOF
	}
	n := copy(p, s.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func tableKeys(t *testing.T, b []byte, handleIndex int) [][]byte {
	t.Helper()
	footer := b[len(b)-48:]
	pos := 0
	var handles [4]uint64
	for i := range handles {
		v, n := binary.Uvarint(footer[pos:])
		if n <= 0 {
			t.Fatal("malformed footer")
		}
		handles[i], pos = v, pos+n
	}
	block := b[handles[2]:][:handles[3]]
	count := int(binary.LittleEndian.Uint32(block[len(block)-4:]))
	data := block[:len(block)-4-4*count]
	var keys [][]byte
	var key []byte
	for p := 0; p < len(data); {
		var v [3]uint64
		for i := range v {
			x, n := binary.Uvarint(data[p:])
			if n <= 0 {
				t.Fatal("malformed index entry")
			}
			v[i], p = x, p+n
		}
		key = append(key[:v[0]], data[p:p+int(v[1])]...)
		p += int(v[1]) + int(v[2])
		keys = append(keys, bytes.Clone(key))
	}
	return keys
}

func TestRealDatabase(t *testing.T) {
	rng := rand.New(rand.NewPCG(41, 43))
	users := randomUsers(rng, 250)
	stor := storage.NewMemStorage()
	options := &opt.Options{
		Compression: opt.NoCompression,
		Filter:      filter.NewBloomFilter(10),
		BlockSize:   256,
		WriteBuffer: 64 << 20,
	}

	db, err := leveldb.Open(stor, options)
	if err != nil {
		t.Fatal(err)
	}
	var ops []version
	for len(ops) < 3000 {
		batch := new(leveldb.Batch)
		for range 1 + rng.IntN(8) {
			user := users[rng.IntN(len(users))]
			seq := uint64(len(ops) + 1)
			if rng.IntN(5) == 0 {
				batch.Delete(user)
				ops = append(ops, version{user, seq, ikey.KindDelete, nil})
			} else {
				value := fmt.Appendf(nil, "%s=%d", user, seq)
				batch.Put(user, value)
				ops = append(ops, version{user, seq, ikey.KindPut, value})
			}
		}
		if err := db.Write(batch, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = leveldb.Open(stor, options)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	fds, err := stor.List(storage.TypeTable)
	if err != nil {
		t.Fatal(err)
	}
	if len(fds) != 1 {
		t.Fatalf("database has %d table files, want 1", len(fds))
	}
	rd, err := stor.Open(fds[0])
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rd)
	rd.Close()
	if err != nil {
		t.Fatal(err)
	}

	spy := &spyReaderAt{b: data}
	r, err := sstable.Open(spy, int64(len(data)), ourInternalOptions())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	byUser := map[string][]version{}
	for _, op := range ops {
		byUser[string(op.user)] = append(byUser[string(op.user)], op)
	}
	for _, versions := range byUser {
		slices.Reverse(versions)
	}

	t.Run("Should read every operation as an internal key in the order of ikey.Compare", func(t *testing.T) {
		want := map[string]version{}
		for _, op := range ops {
			want[string(op.key())] = op
		}
		it := r.NewIterator()
		defer it.Close()
		var previous []byte
		count := 0
		for it.SeekToFirst(); it.Valid(); it.Next() {
			if previous != nil && ikey.Compare(previous, it.Key()) >= 0 {
				t.Fatalf("keys %x and %x are not in order", previous, it.Key())
			}
			previous = append(previous[:0], it.Key()...)
			user, seq, kind, err := ikey.Parse(it.Key())
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			op, ok := want[string(it.Key())]
			if !ok || !bytes.Equal(op.user, user) || op.seq != seq || op.kind != kind || !bytes.Equal(op.value, it.Value()) {
				t.Fatalf("entry %q seq %d kind %d is not in the history", user, seq, kind)
			}
			count++
		}
		if err := it.Err(); err != nil {
			t.Fatal(err)
		}
		if count != len(ops) {
			t.Fatalf("table has %d entries, want %d", count, len(ops))
		}
	})

	var separators int
	var probes [][]byte
	t.Run("Should use separators in the index that are not the last key of a block", func(t *testing.T) {
		all := map[string]bool{}
		it := r.NewIterator()
		for it.SeekToFirst(); it.Valid(); it.Next() {
			all[string(it.Key())] = true
		}
		it.Close()
		for _, k := range tableKeys(t, data, 0) {
			if !all[string(k)] {
				separators++
				probes = append(probes, bytes.Clone(ikey.UserKey(k)), append(bytes.Clone(ikey.UserKey(k)), 0))
			}
		}
		if separators == 0 {
			t.Fatal("the index has no shortened separator, so this test does not cover them")
		}
	})

	t.Run("Should agree with the database for the latest version of present, absent and in-between keys", func(t *testing.T) {
		probes := append(probeUsers(users), probes...)
		absent := 0
		absentReads := 0
		present := map[string]bool{}
		for _, u := range users {
			present[string(u)] = true
		}
		for _, probe := range probes {
			spy.reads = 0
			key, value, found, err := r.Find(ikey.AppendLookup(nil, probe, ikey.MaxSeq))
			if err != nil {
				t.Fatalf("Find(%q): %v", probe, err)
			}
			if !present[string(probe)] {
				absent++
				absentReads += spy.reads
			}
			dbValue, dbErr := db.Get(probe, nil)
			match := found && bytes.Equal(ikey.UserKey(key), probe)
			if match {
				_, _, kind, _ := ikey.Parse(key)
				if kind == ikey.KindPut {
					if dbErr != nil || !bytes.Equal(dbValue, value) {
						t.Fatalf("Find(%q) is a put of %q but Get gave %q, %v", probe, value, dbValue, dbErr)
					}
					continue
				}
			}
			if dbErr != errors.ErrNotFound {
				t.Fatalf("Find(%q) shows no live value but Get gave %q, %v", probe, dbValue, dbErr)
			}
		}
		if absentReads > absent/10 {
			t.Fatalf("the filter avoided the data block read for only %d of %d absent keys", absent-absentReads, absent)
		}
	})

	t.Run("Should find the version that each snapshot can see, also across blocks", func(t *testing.T) {
		probes := append(probeUsers(users), probes...)
		for range 40 {
			snapshot := uint64(1 + rng.IntN(len(ops)))
			for _, probe := range probes {
				key, value, found, err := r.Find(ikey.AppendLookup(nil, probe, snapshot))
				if err != nil {
					t.Fatalf("Find(%q, %d): %v", probe, snapshot, err)
				}
				var want *version
				for i := range byUser[string(probe)] {
					if byUser[string(probe)][i].seq <= snapshot {
						want = &byUser[string(probe)][i]
						break
					}
				}
				got := found && bytes.Equal(ikey.UserKey(key), probe)
				if want == nil {
					if got {
						t.Fatalf("Find(%q, %d) returned %x, want no version", probe, snapshot, key)
					}
					continue
				}
				if !got || !bytes.Equal(key, want.key()) || !bytes.Equal(value, want.value) {
					t.Fatalf("Find(%q, %d) = %x, %v, want %x", probe, snapshot, key, got, want.key())
				}
			}
		}
	})
}
