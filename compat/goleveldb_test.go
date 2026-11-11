// SPDX-License-Identifier: MIT

package compat

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/syndtr/goleveldb/leveldb/errors"
	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"github.com/syndtr/goleveldb/leveldb/table"
	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/thalesdev/sediment/internal/sstable"
)

type memFile struct{ bytes.Buffer }

func (*memFile) Sync() error { return nil }

type kv struct{ key, value []byte }

func levelOptions() *opt.Options {
	return &opt.Options{
		BlockSize:            4096,
		BlockRestartInterval: 16,
		Compression:          opt.NoCompression,
		Filter:               filter.NewBloomFilter(10),
	}
}

func writeOurs(t *testing.T, entries []kv) []byte {
	t.Helper()
	f := &memFile{}
	w := sstable.NewWriter(f, sstable.Options{})
	for _, e := range entries {
		if err := w.Add(e.key, e.value); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	if err := w.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return f.Bytes()
}

func writeTheirs(t *testing.T, entries []kv) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := table.NewWriter(&buf, levelOptions())
	for _, e := range entries {
		if err := w.Append(e.key, e.value); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}

func indexOffset(t *testing.T, b []byte) int {
	t.Helper()
	footer := b[len(b)-48:]
	_, n := binary.Uvarint(footer)
	_, m := binary.Uvarint(footer[n:])
	off, k := binary.Uvarint(footer[n+m:])
	if n <= 0 || m <= 0 || k <= 0 {
		t.Fatal("malformed footer")
	}
	return int(off)
}

func openTheirs(t *testing.T, b []byte) *table.Reader {
	t.Helper()
	r, err := table.NewReader(bytes.NewReader(b), int64(len(b)), storage.FileDesc{Type: storage.TypeTable, Num: 1}, nil, util.NewBufferPool(4096), levelOptions())
	if err != nil {
		t.Fatalf("goleveldb could not open the table: %v", err)
	}
	return r
}

func dataset(name string) []kv {
	rng := rand.New(rand.NewPCG(42, 42))
	var out []kv
	switch name {
	case "empty":
	case "single":
		out = []kv{{[]byte("abobrinha"), []byte("verde")}}
	case "many small":
		for i := range 5000 {
			out = append(out, kv{[]byte(fmt.Sprintf("key-%08d", i)), []byte(fmt.Sprintf("value-%d", rng.IntN(1e9)))})
		}
	case "long shared prefixes":
		prefix := bytes.Repeat([]byte("p"), 300)
		for i := range 2000 {
			out = append(out, kv{append(bytes.Clone(prefix), fmt.Sprintf("%08d", i)...), []byte{byte(i)}})
		}
	case "values larger than a block":
		for i := range 40 {
			v := make([]byte, rng.IntN(20000))
			for j := range v {
				v[j] = byte(rng.IntN(256))
			}
			out = append(out, kv{[]byte(fmt.Sprintf("big-%04d", i)), v})
		}
	case "empty values and binary keys":
		seen := map[string]bool{}
		for len(out) < 3000 {
			k := make([]byte, 1+rng.IntN(24))
			for j := range k {
				k[j] = byte(rng.IntN(256))
			}
			if !seen[string(k)] {
				seen[string(k)] = true
				out = append(out, kv{k, nil})
			}
		}
		sortKV(out)
	}
	return out
}

func sortKV(s []kv) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && bytes.Compare(s[j-1].key, s[j].key) > 0; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

var datasets = []string{"empty", "single", "many small", "long shared prefixes", "values larger than a block", "empty values and binary keys"}

func TestGoleveldbReadsOurTables(t *testing.T) {
	for _, name := range datasets {
		t.Run("Should be readable by goleveldb: "+name, func(t *testing.T) {
			entries := dataset(name)
			r := openTheirs(t, writeOurs(t, entries))

			it := r.NewIterator(nil, nil)
			i := 0
			for it.Next() {
				if i >= len(entries) {
					t.Fatalf("goleveldb returned more than %d entries", len(entries))
				}
				if !bytes.Equal(it.Key(), entries[i].key) || !bytes.Equal(it.Value(), entries[i].value) {
					t.Fatalf("entry %d differs: got key %q", i, it.Key())
				}
				i++
			}
			if err := it.Error(); err != nil {
				t.Fatalf("iterator error: %v", err)
			}
			it.Release()
			if i != len(entries) {
				t.Fatalf("goleveldb returned %d entries, want %d", i, len(entries))
			}

			for _, e := range entries {
				v, err := r.Get(e.key, nil)
				if err != nil {
					t.Fatalf("Get(%q): %v", e.key, err)
				}
				if !bytes.Equal(v, e.value) {
					t.Fatalf("Get(%q): wrong value", e.key)
				}
			}
			for i := range 200 {
				missing := []byte(fmt.Sprintf("zzz-missing-%d", i))
				if _, err := r.Get(missing, nil); err != errors.ErrNotFound {
					t.Fatalf("Get(%q): got %v, want ErrNotFound", missing, err)
				}
			}
		})
	}
}

func TestBytesMatchGoleveldb(t *testing.T) {
	for _, name := range datasets[1:] {
		t.Run("Should produce the same data, filter and metaindex bytes as goleveldb: "+name, func(t *testing.T) {
			entries := dataset(name)
			ours, theirs := writeOurs(t, entries), writeTheirs(t, entries)
			oi, ti := indexOffset(t, ours), indexOffset(t, theirs)
			if oi != ti {
				t.Fatalf("index starts at %d, goleveldb at %d", oi, ti)
			}
			if !bytes.Equal(ours[:oi], theirs[:ti]) {
				for i := range oi {
					if ours[i] != theirs[i] {
						t.Fatalf("first differing byte at offset %d", i)
					}
				}
			}
		})
	}
}

func TestOurReaderReadsGoleveldbTables(t *testing.T) {
	for _, name := range datasets {
		t.Run("Should be readable by our reader: "+name, func(t *testing.T) {
			entries := dataset(name)
			b := writeTheirs(t, entries)
			r, err := sstable.Open(bytes.NewReader(b), int64(len(b)), sstable.Options{})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}

			it := r.NewIterator()
			defer it.Close()
			i := 0
			for it.SeekToFirst(); it.Valid(); it.Next() {
				if i >= len(entries) || !bytes.Equal(it.Key(), entries[i].key) || !bytes.Equal(it.Value(), entries[i].value) {
					t.Fatalf("entry %d differs: got key %q", i, it.Key())
				}
				i++
			}
			if err := it.Err(); err != nil {
				t.Fatalf("iterator error: %v", err)
			}
			if i != len(entries) {
				t.Fatalf("iterated %d entries, want %d", i, len(entries))
			}

			for j, e := range entries {
				v, ok, err := r.Get(e.key)
				if err != nil || !ok || !bytes.Equal(v, e.value) {
					t.Fatalf("Get(%q): %v, %v", e.key, ok, err)
				}
				if j%97 == 0 {
					it.Seek(e.key)
					if !it.Valid() || !bytes.Equal(it.Key(), e.key) {
						t.Fatalf("Seek(%q) landed on %q", e.key, it.Key())
					}
				}
			}
			for j := range 200 {
				missing := []byte(fmt.Sprintf("zzz-missing-%d", j))
				if _, ok, err := r.Get(missing); ok || err != nil {
					t.Fatalf("Get(%q): %v, %v", missing, ok, err)
				}
			}
		})
	}
}
