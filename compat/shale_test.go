// SPDX-License-Identifier: MIT

package compat

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/syndtr/goleveldb/leveldb/storage"
	"github.com/syndtr/goleveldb/leveldb/table"
	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/thalesdev/sediment/internal/ikey"
	"github.com/thalesdev/sediment/internal/shale"
)

type newest struct {
	seq   uint64
	kind  ikey.Kind
	value []byte
	file  int
}

func TestGoleveldbReadsShaleTables(t *testing.T) {
	rng := rand.New(rand.NewPCG(51, 53))
	users := randomUsers(rng, 300)
	dir := t.TempDir()

	db, err := shale.Open(dir, shale.Options{MemtableSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	model := map[string][]byte{}
	written := map[string]bool{}
	for i := range 4000 {
		user := users[rng.IntN(len(users))]
		if rng.IntN(5) == 0 {
			if err := db.Delete(user); err != nil {
				t.Fatal(err)
			}
			delete(model, string(user))
		} else {
			value := fmt.Appendf(nil, "%s=%d", user, i)
			if err := db.Put(user, value); err != nil {
				t.Fatal(err)
			}
			model[string(user)] = value
		}
		written[string(user)] = true
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = shale.Open(dir, shale.Options{MemtableSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	paths, err := filepath.Glob(filepath.Join(dir, "*.sd"))
	if err != nil || len(paths) < 3 {
		t.Fatalf("found %d table files, %v", len(paths), err)
	}
	readers := make([]*table.Reader, len(paths))
	latest := map[string]newest{}
	for i, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		r, err := table.NewReader(bytes.NewReader(b), int64(len(b)), storage.FileDesc{Type: storage.TypeTable, Num: int64(i + 1)}, nil, util.NewBufferPool(4096), internalLevelOptions())
		if err != nil {
			t.Fatalf("goleveldb could not open %s: %v", filepath.Base(path), err)
		}
		readers[i] = r
		it := r.NewIterator(nil, nil)
		var previous []byte
		for it.Next() {
			if previous != nil && ikey.Compare(previous, it.Key()) >= 0 {
				t.Fatalf("%s: keys %x and %x are not in order", filepath.Base(path), previous, it.Key())
			}
			previous = append(previous[:0], it.Key()...)
			user, seq, kind, err := ikey.Parse(it.Key())
			if err != nil {
				t.Fatalf("%s: %v", filepath.Base(path), err)
			}
			if cur, ok := latest[string(user)]; !ok || seq > cur.seq {
				latest[string(user)] = newest{seq, kind, bytes.Clone(it.Value()), i}
			}
		}
		if err := it.Error(); err != nil {
			t.Fatal(err)
		}
		it.Release()
	}

	for user := range written {
		got, ok := latest[user]
		want, live := model[user]
		if !ok {
			t.Fatalf("user key %q has no entry in any table", user)
		}
		if live != (got.kind == ikey.KindPut) || (live && !bytes.Equal(got.value, want)) {
			t.Fatalf("user key %q: newest entry has kind %d and value %q, want live=%v value %q", user, got.kind, got.value, live, want)
		}
		key, _, err := readers[got.file].Find(ikey.AppendLookup(nil, []byte(user), ikey.MaxSeq), true, nil)
		if err != nil {
			t.Fatalf("goleveldb Find(%q): %v", user, err)
		}
		if gotUser, seq, _, err := ikey.Parse(key); err != nil || string(gotUser) != user || seq != got.seq {
			t.Fatalf("goleveldb Find(%q) = %x, %v", user, key, err)
		}
	}
}
