// SPDX-License-Identifier: MIT

package compat

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/thalesdev/sediment/internal/shale"
)

type kvPair struct{ key, value string }

func collectShale(t *testing.T, it shale.Iterator) []kvPair {
	t.Helper()
	var out []kvPair
	for it.SeekToFirst(); it.Valid(); it.Next() {
		out = append(out, kvPair{string(it.Key()), string(it.Value())})
	}
	if err := it.Err(); err != nil {
		t.Fatalf("shale iterator: %v", err)
	}
	return out
}

func collectLevel(t *testing.T, db *leveldb.DB, r *util.Range) []kvPair {
	t.Helper()
	it := db.NewIterator(r, nil)
	defer it.Release()
	var out []kvPair
	for it.Next() {
		out = append(out, kvPair{string(it.Key()), string(it.Value())})
	}
	if err := it.Error(); err != nil {
		t.Fatalf("goleveldb iterator: %v", err)
	}
	return out
}

func TestIteratorMatchesGoleveldb(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	sdb, err := shale.Open(t.TempDir(), shale.Options{MemtableSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	ldb, err := leveldb.Open(storage.NewMemStorage(), &opt.Options{WriteBuffer: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer ldb.Close()

	apply := func(n int) {
		for range n {
			k := []byte(fmt.Sprintf("key-%03d", rng.IntN(200)))
			if rng.IntN(4) == 0 {
				if err := sdb.Delete(k); err != nil {
					t.Fatal(err)
				}
				if err := ldb.Delete(k, nil); err != nil {
					t.Fatal(err)
				}
				continue
			}
			v := []byte(fmt.Sprintf("v%d-%s", rng.Int(), bytes.Repeat([]byte("x"), rng.IntN(120))))
			if err := sdb.Put(k, v); err != nil {
				t.Fatal(err)
			}
			if err := ldb.Put(k, v, nil); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("Should iterate the whole keyspace like goleveldb", func(t *testing.T) {
		for range 6 {
			apply(500)
			it, err := sdb.NewIterator(nil)
			if err != nil {
				t.Fatal(err)
			}
			got := collectShale(t, it)
			it.Close()
			want := collectLevel(t, ldb, nil)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("shale has %d pairs, goleveldb has %d", len(got), len(want))
			}
		}
	})

	t.Run("Should honor bounds like goleveldb", func(t *testing.T) {
		for range 40 {
			lo := []byte(fmt.Sprintf("key-%03d", rng.IntN(210)))
			hi := []byte(fmt.Sprintf("key-%03d", rng.IntN(210)))
			if bytes.Compare(lo, hi) > 0 {
				lo, hi = hi, lo
			}
			it, err := sdb.NewIterator(&shale.IterOptions{Lower: lo, Upper: hi})
			if err != nil {
				t.Fatal(err)
			}
			got := collectShale(t, it)
			it.Close()
			want := collectLevel(t, ldb, &util.Range{Start: lo, Limit: hi})
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("range [%s, %s): shale has %d pairs, goleveldb has %d", lo, hi, len(got), len(want))
			}
		}
	})

	t.Run("Should Seek like goleveldb", func(t *testing.T) {
		sit, err := sdb.NewIterator(nil)
		if err != nil {
			t.Fatal(err)
		}
		defer sit.Close()
		lit := ldb.NewIterator(nil, nil)
		defer lit.Release()
		for range 100 {
			target := []byte(fmt.Sprintf("key-%03d", rng.IntN(210)))
			sit.Seek(target)
			ok := lit.Seek(target)
			if sit.Valid() != ok {
				t.Fatalf("Seek(%s): shale valid=%v, goleveldb valid=%v", target, sit.Valid(), ok)
			}
			if ok && (!bytes.Equal(sit.Key(), lit.Key()) || !bytes.Equal(sit.Value(), lit.Value())) {
				t.Fatalf("Seek(%s): shale at %q, goleveldb at %q", target, sit.Key(), lit.Key())
			}
		}
	})

	t.Run("Should keep a snapshot like goleveldb", func(t *testing.T) {
		ssnap, err := sdb.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		defer ssnap.Release()
		lsnap, err := ldb.GetSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		defer lsnap.Release()
		apply(1500)
		it, err := ssnap.NewIterator(nil)
		if err != nil {
			t.Fatal(err)
		}
		defer it.Close()
		got := collectShale(t, it)
		lit := lsnap.NewIterator(nil, nil)
		defer lit.Release()
		var want []kvPair
		for lit.Next() {
			want = append(want, kvPair{string(lit.Key()), string(lit.Value())})
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("snapshot: shale has %d pairs, goleveldb has %d", len(got), len(want))
		}
	})
}
