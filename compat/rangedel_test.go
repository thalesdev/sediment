// SPDX-License-Identifier: MIT

package compat

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"github.com/syndtr/goleveldb/leveldb/table"
	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/thalesdev/sediment/internal/batch"
	"github.com/thalesdev/sediment/internal/ikey"
	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/internal/sstable"
)

func writeOursWithRangeDels(t *testing.T, entries []kv, dels []kv) []byte {
	t.Helper()
	f := &memFile{}
	w := sstable.NewWriter(f, sstable.Options{})
	for _, e := range entries {
		if err := w.Add(e.key, e.value); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	for _, d := range dels {
		if err := w.AddRangeDel(d.key, d.value); err != nil {
			t.Fatalf("AddRangeDel: %v", err)
		}
	}
	if err := w.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return f.Bytes()
}

func TestBatchMatchesGoleveldb(t *testing.T) {
	t.Run("Should write the records of a batch of puts and deletes like goleveldb", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(3, 5))
		for range 50 {
			var ours batch.Batch
			ours.Reset(uint64(1 + rng.IntN(1000)))
			theirs := new(leveldb.Batch)
			for range 1 + rng.IntN(40) {
				k := bytes.Repeat([]byte{byte('a' + rng.IntN(26))}, rng.IntN(300))
				if rng.IntN(3) == 0 {
					ours.Delete(k)
					theirs.Delete(k)
					continue
				}
				v := bytes.Repeat([]byte{byte('A' + rng.IntN(26))}, rng.IntN(300))
				ours.Put(k, v)
				theirs.Put(k, v)
			}
			if !bytes.Equal(ours.Bytes()[batch.HeaderSize:], theirs.Dump()) {
				t.Fatal("the records of the batch are not the records of goleveldb")
			}
			var loaded leveldb.Batch
			if err := loaded.Load(ours.Bytes()[batch.HeaderSize:]); err != nil {
				t.Fatalf("goleveldb could not load our batch: %v", err)
			}
			if loaded.Len() != ours.Count() {
				t.Fatalf("goleveldb sees %d records, we wrote %d", loaded.Len(), ours.Count())
			}
		}
	})

	t.Run("Should be refused by goleveldb when the batch has a range delete", func(t *testing.T) {
		var ours batch.Batch
		ours.Reset(1)
		ours.Put([]byte("a"), []byte("b"))
		ours.DeleteRange([]byte("c"), []byte("d"))
		var loaded leveldb.Batch
		if err := loaded.Load(ours.Bytes()[batch.HeaderSize:]); err == nil {
			t.Fatal("goleveldb loaded a batch with a range delete, which documents the wrong format")
		}
	})
}

func TestRangeDelTables(t *testing.T) {
	t.Run("Should let goleveldb read the data of a table that has range deletes", func(t *testing.T) {
		entries := dataset(datasets[len(datasets)-1])
		sortKV(entries)
		dels := []kv{{[]byte("a"), []byte("b")}, {[]byte("m"), []byte("p")}, {[]byte("x"), []byte("y")}}
		b := writeOursWithRangeDels(t, entries, dels)
		r := openTheirs(t, b)
		it := r.NewIterator(nil, nil)
		i := 0
		for it.Next() {
			if i >= len(entries) || !bytes.Equal(it.Key(), entries[i].key) || !bytes.Equal(it.Value(), entries[i].value) {
				t.Fatalf("entry %d differs", i)
			}
			i++
		}
		if err := it.Error(); err != nil {
			t.Fatal(err)
		}
		it.Release()
		if i != len(entries) {
			t.Fatalf("goleveldb returned %d entries, want %d", i, len(entries))
		}
		for _, e := range entries[:min(len(entries), 100)] {
			v, err := r.Get(e.key, nil)
			if err != nil || !bytes.Equal(v, e.value) {
				t.Fatalf("Get(%q): %v", e.key, err)
			}
		}
	})

	t.Run("Should keep the data and the filter bytes of a table that has range deletes", func(t *testing.T) {
		entries := dataset(datasets[len(datasets)-1])
		sortKV(entries)
		plain := writeOurs(t, entries)
		with := writeOursWithRangeDels(t, entries, []kv{{[]byte("a"), []byte("b")}})
		end := metaindexOffset(t, plain)
		if !bytes.Equal(plain[:end], with[:end]) {
			t.Fatal("the data blocks or the filter changed")
		}
	})

	t.Run("Should let goleveldb open a table that has only range deletes", func(t *testing.T) {
		b := writeOursWithRangeDels(t, nil, []kv{{[]byte("a"), []byte("z")}})
		r := openTheirs(t, b)
		it := r.NewIterator(nil, nil)
		defer it.Release()
		if it.Next() {
			t.Fatal("a table with no data entries returned one")
		}
		if err := it.Error(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Should read back the range deletes with our reader", func(t *testing.T) {
		var dels []kv
		for i := range 40 {
			dels = append(dels, kv{[]byte(fmt.Sprintf("k%03d", i)), []byte(fmt.Sprintf("k%03d", i+1))})
		}
		b := writeOursWithRangeDels(t, nil, dels)
		r, err := sstable.Open(bytes.NewReader(b), int64(len(b)), sstable.Options{})
		if err != nil {
			t.Fatal(err)
		}
		got := r.RangeDels()
		if len(got) != len(dels) {
			t.Fatalf("got %d, want %d", len(got), len(dels))
		}
		for i := range dels {
			if !bytes.Equal(got[i].Key, dels[i].key) || !bytes.Equal(got[i].End, dels[i].value) {
				t.Fatalf("range delete %d differs", i)
			}
		}
	})
}

func metaindexOffset(t *testing.T, b []byte) int {
	t.Helper()
	footer := b[len(b)-48:]
	off, n := binary.Uvarint(footer)
	if n <= 0 {
		t.Fatal("malformed footer")
	}
	return int(off)
}

func TestGoleveldbReadsShaleTablesWithRangeDeletes(t *testing.T) {
	rng := rand.New(rand.NewPCG(61, 67))
	dir := t.TempDir()
	db, err := shale.Open(dir, shale.Options{MemtableSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	model := map[string][]byte{}
	for i := range 4000 {
		user := []byte(fmt.Sprintf("user-%03d", rng.IntN(300)))
		switch rng.IntN(12) {
		case 0:
			a, b := rng.IntN(300), rng.IntN(300)
			if a > b {
				a, b = b, a
			}
			start, end := fmt.Sprintf("user-%03d", a), fmt.Sprintf("user-%03d", b)
			if err := db.DeleteRange([]byte(start), []byte(end)); err != nil {
				t.Fatal(err)
			}
			for k := range model {
				if k >= start && k < end {
					delete(model, k)
				}
			}
		case 1:
			if err := db.Delete(user); err != nil {
				t.Fatal(err)
			}
			delete(model, string(user))
		default:
			value := fmt.Appendf(nil, "%s=%d", user, i)
			if err := db.Put(user, value); err != nil {
				t.Fatal(err)
			}
			model[string(user)] = value
		}
	}
	it, err := db.NewIterator(nil)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for it.SeekToFirst(); it.Valid(); it.Next() {
		want, ok := model[string(it.Key())]
		if !ok || !bytes.Equal(want, it.Value()) {
			t.Fatalf("shale returned %q, which the model does not have", it.Key())
		}
		n++
	}
	if err := it.Err(); err != nil {
		t.Fatal(err)
	}
	it.Close()
	if n != len(model) {
		t.Fatalf("shale returned %d keys, the model has %d", n, len(model))
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	paths, err := filepath.Glob(filepath.Join(dir, "*.sd"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("found %d table files, %v", len(paths), err)
	}
	withRangeDels := 0
	for i, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("shale.range_del")) {
			withRangeDels++
		}
		r, err := table.NewReader(bytes.NewReader(b), int64(len(b)), storage.FileDesc{Type: storage.TypeTable, Num: int64(i + 1)}, nil, util.NewBufferPool(4096), internalLevelOptions())
		if err != nil {
			t.Fatalf("goleveldb could not open %s: %v", filepath.Base(path), err)
		}
		tit := r.NewIterator(nil, nil)
		var previous []byte
		for tit.Next() {
			if previous != nil && ikey.Compare(previous, tit.Key()) >= 0 {
				t.Fatalf("%s: keys are not in order", filepath.Base(path))
			}
			previous = append(previous[:0], tit.Key()...)
			if _, _, _, err := ikey.Parse(tit.Key()); err != nil {
				t.Fatalf("%s: %v", filepath.Base(path), err)
			}
		}
		if err := tit.Error(); err != nil {
			t.Fatalf("%s: %v", filepath.Base(path), err)
		}
		tit.Release()
	}
	if withRangeDels == 0 {
		t.Fatal("no table has range deletes, so the test does not cover them")
	}
}
