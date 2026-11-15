// SPDX-License-Identifier: MIT

package shale

import (
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type pair struct{ key, value string }

func modelPairs(model map[string]string, lower, upper []byte) []pair {
	var out []pair
	for k, v := range model {
		if lower != nil && k < string(lower) {
			continue
		}
		if upper != nil && k >= string(upper) {
			continue
		}
		out = append(out, pair{k, v})
	}
	slices.SortFunc(out, func(a, b pair) int {
		if a.key < b.key {
			return -1
		}
		if a.key > b.key {
			return 1
		}
		return 0
	})
	return out
}

func drainIter(t testing.TB, it Iterator) []pair {
	t.Helper()
	var out []pair
	for it.SeekToFirst(); it.Valid(); it.Next() {
		out = append(out, pair{string(it.Key()), string(it.Value())})
	}
	if err := it.Err(); err != nil {
		t.Fatalf("Err = %v", err)
	}
	return out
}

func checkPairs(t testing.TB, got, want []pair) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %d pairs %v, want %d pairs %v", len(got), got, len(want), want)
	}
}

func newIter(t testing.TB, db *DB, opts *IterOptions) Iterator {
	t.Helper()
	it, err := db.NewIterator(opts)
	if err != nil {
		t.Fatalf("NewIterator: %v", err)
	}
	return it
}

func randomOps(t testing.TB, db *DB, model map[string]string, rng *rand.Rand, n, keys int) {
	t.Helper()
	for range n {
		k := fmt.Sprintf("k%04d", rng.Intn(keys))
		if rng.Intn(4) == 0 {
			mustDelete(t, db, k)
			delete(model, k)
			continue
		}
		v := fmt.Sprintf("v%d-%s", rng.Int(), value100(rng.Intn(50)))
		mustPut(t, db, k, v)
		model[k] = v
	}
}

func TestUserIterator(t *testing.T) {
	t.Parallel()

	t.Run("Should iterate like a sorted map through flushes and compactions", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), smallOptions())
		defer db.Close()
		model := map[string]string{}
		rng := rand.New(rand.NewSource(1))
		for range sweepStep(30, 8) {
			randomOps(t, db, model, rng, 200, 300)
			it := newIter(t, db, nil)
			checkPairs(t, drainIter(t, it), modelPairs(model, nil, nil))
			it.Close()
		}
		waitQuiet(t, db)
		it := newIter(t, db, nil)
		defer it.Close()
		checkPairs(t, drainIter(t, it), modelPairs(model, nil, nil))
	})

	t.Run("Should be empty on an empty DB", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		it := newIter(t, db, nil)
		defer it.Close()
		it.SeekToFirst()
		if it.Valid() || it.Err() != nil {
			t.Fatalf("valid=%v err=%v", it.Valid(), it.Err())
		}
		it.Seek([]byte("a"))
		if it.Valid() {
			t.Fatal("Seek found a key in an empty DB")
		}
	})

	t.Run("Should hide a deleted key and an overwritten version", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		mustPut(t, db, "a", "1")
		mustPut(t, db, "b", "1")
		mustPut(t, db, "b", "2")
		mustPut(t, db, "c", "1")
		mustDelete(t, db, "c")
		mustDelete(t, db, "never")
		it := newIter(t, db, nil)
		defer it.Close()
		checkPairs(t, drainIter(t, it), []pair{{"a", "1"}, {"b", "2"}})
	})

	t.Run("Should honor the bounds and Seek", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), smallOptions())
		defer db.Close()
		model := map[string]string{}
		randomOps(t, db, model, rand.New(rand.NewSource(2)), 600, 120)
		waitQuiet(t, db)
		rng := rand.New(rand.NewSource(3))
		for range 60 {
			lo := []byte(fmt.Sprintf("k%04d", rng.Intn(130)))
			hi := []byte(fmt.Sprintf("k%04d", rng.Intn(130)))
			if string(lo) > string(hi) {
				lo, hi = hi, lo
			}
			opts := &IterOptions{Lower: lo, Upper: hi}
			it := newIter(t, db, opts)
			want := modelPairs(model, lo, hi)
			checkPairs(t, drainIter(t, it), want)
			target := []byte(fmt.Sprintf("k%04d", rng.Intn(130)))
			it.Seek(target)
			var rest []pair
			for ; it.Valid(); it.Next() {
				rest = append(rest, pair{string(it.Key()), string(it.Value())})
			}
			var wantRest []pair
			for _, p := range want {
				if p.key >= string(target) {
					wantRest = append(wantRest, p)
				}
			}
			checkPairs(t, rest, wantRest)
			it.Close()
		}
		it := newIter(t, db, &IterOptions{Lower: []byte("zzz")})
		defer it.Close()
		it.SeekToFirst()
		if it.Valid() {
			t.Fatal("a range past the last key should be empty")
		}
	})

	t.Run("Should not see a write that comes after the iterator started", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		mustPut(t, db, "a", "1")
		it := newIter(t, db, nil)
		defer it.Close()
		mustPut(t, db, "b", "1")
		mustPut(t, db, "a", "2")
		checkPairs(t, drainIter(t, it), []pair{{"a", "1"}})
	})

	t.Run("Should not return a pair after Close", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		mustPut(t, db, "a", "1")
		it := newIter(t, db, nil)
		it.SeekToFirst()
		if !it.Valid() {
			t.Fatal("not valid")
		}
		if err := it.Close(); err != nil {
			t.Fatal(err)
		}
		if err := it.Close(); err != nil {
			t.Fatalf("second Close = %v", err)
		}
		if it.Valid() {
			t.Fatal("valid after Close")
		}
	})

	t.Run("Should return ErrClosed after the DB is closed", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), Options{})
		mustPut(t, db, "a", "1")
		it := newIter(t, db, nil)
		it.SeekToFirst()
		closeDB(t, db)
		it.Next()
		if it.Valid() || !errors.Is(it.Err(), ErrClosed) {
			t.Fatalf("valid=%v err=%v", it.Valid(), it.Err())
		}
		it.Close()
		if _, err := db.NewIterator(nil); !errors.Is(err, ErrClosed) {
			t.Fatalf("NewIterator = %v", err)
		}
		if _, err := db.Snapshot(); !errors.Is(err, ErrClosed) {
			t.Fatalf("Snapshot = %v", err)
		}
	})
}

func TestSnapshot(t *testing.T) {
	t.Parallel()

	t.Run("Should keep its view after writes, deletes, flushes and compactions", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), smallOptions())
		defer db.Close()
		model := map[string]string{}
		rng := rand.New(rand.NewSource(4))
		randomOps(t, db, model, rng, 400, 150)
		snap, err := db.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		defer snap.Release()
		frozen := map[string]string{}
		for k, v := range model {
			frozen[k] = v
		}
		randomOps(t, db, model, rng, 3000, 150)
		waitQuiet(t, db)
		it, err := snap.NewIterator(nil)
		if err != nil {
			t.Fatal(err)
		}
		defer it.Close()
		checkPairs(t, drainIter(t, it), modelPairs(frozen, nil, nil))
		for k, v := range frozen {
			got, err := snap.Get([]byte(k))
			if err != nil || string(got) != v {
				t.Fatalf("snapshot Get(%q) = %q, %v, want %q", k, got, err, v)
			}
		}
		for _, k := range []string{"k9999", "missing"} {
			if _, err := snap.Get([]byte(k)); !errors.Is(err, ErrNotFound) {
				t.Fatalf("snapshot Get(%q) = %v", k, err)
			}
		}
		now := newIter(t, db, nil)
		defer now.Close()
		checkPairs(t, drainIter(t, now), modelPairs(model, nil, nil))
	})

	t.Run("Should keep the files of the tables that it uses until it is released", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), smallOptions())
		defer db.Close()
		mustPut(t, db, "trap", "v1")
		next := 0
		fillUntilTables(t, db, &next, 3)
		waitIdle(t, db)
		snap, err := db.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		before := snap.state.tables
		mustPut(t, db, "trap", "v2")
		for db.Metrics().Compactions < 1 {
			putFiller(t, db, next)
			next++
			if next > 20000 {
				t.Fatal("no compaction")
			}
		}
		waitQuiet(t, db)
		for _, tb := range before {
			if !tb.obsolete.Load() {
				continue
			}
			if tb.refs.Load() < 1 {
				t.Fatalf("table %d was closed while the snapshot holds it", tb.meta.Number)
			}
		}
		got, err := snap.Get([]byte("trap"))
		if err != nil || string(got) != "v1" {
			t.Fatalf("Get = %q, %v", got, err)
		}
		it, err := snap.NewIterator(nil)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for it.SeekToFirst(); it.Valid(); it.Next() {
			n++
		}
		if it.Err() != nil || n == 0 {
			t.Fatalf("n=%d err=%v", n, it.Err())
		}
		it.Close()
		snap.Release()
		for _, tb := range before {
			if tb.obsolete.Load() && tb.refs.Load() != 0 {
				t.Fatalf("table %d is still held after Release: %d", tb.meta.Number, tb.refs.Load())
			}
		}
	})

	t.Run("Should return ErrReleased after Release and allow a second Release", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		mustPut(t, db, "a", "1")
		snap, err := db.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		it, err := snap.NewIterator(nil)
		if err != nil {
			t.Fatal(err)
		}
		snap.Release()
		snap.Release()
		if _, err := snap.Get([]byte("a")); !errors.Is(err, ErrReleased) {
			t.Fatalf("Get = %v", err)
		}
		if _, err := snap.NewIterator(nil); !errors.Is(err, ErrReleased) {
			t.Fatalf("NewIterator = %v", err)
		}
		checkPairs(t, drainIter(t, it), []pair{{"a", "1"}})
		it.Close()
	})

	t.Run("Should see exactly the writes that returned before it, with a flush in between", func(t *testing.T) {
		t.Parallel()
		opts := smallOptions()
		release := make(chan struct{})
		reached := make(chan struct{}, 1)
		var armed atomic.Bool
		armed.Store(true)
		opts.iterHook = func(stage string) {
			if armed.CompareAndSwap(true, false) {
				reached <- struct{}{}
				<-release
			}
		}
		db := openDB(t, t.TempDir(), opts)
		defer db.Close()
		mustPut(t, db, "before", "1")
		type result struct {
			snap *Snapshot
			err  error
		}
		out := make(chan result, 1)
		go func() {
			s, err := db.Snapshot()
			out <- result{s, err}
		}()
		<-reached
		n := 0
		for db.state.Load().imm == nil && n < 5000 {
			mustPut(t, db, string(keyN(n)), value100(n))
			n++
		}
		close(release)
		r := <-out
		if r.err != nil {
			t.Fatal(r.err)
		}
		defer r.snap.Release()
		if got, err := r.snap.Get([]byte("before")); err != nil || string(got) != "1" {
			t.Fatalf("Get(before) = %q, %v", got, err)
		}
		it, err := r.snap.NewIterator(nil)
		if err != nil {
			t.Fatal(err)
		}
		defer it.Close()
		for p := range slices.Values(drainIter(t, it)) {
			if p.key == "before" {
				return
			}
		}
		t.Fatal("the iterator did not see a write that returned before the snapshot")
	})
}

func TestUserIteratorConcurrent(t *testing.T) {
	t.Parallel()
	t.Run("Should return ordered keys, one value per key, while writers run", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), smallOptions())
		defer db.Close()
		var stop atomic.Bool
		var wg sync.WaitGroup
		for w := range 3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rng := rand.New(rand.NewSource(int64(w)))
				for !stop.Load() {
					k := fmt.Sprintf("k%04d", rng.Intn(200))
					if rng.Intn(4) == 0 {
						db.Delete([]byte(k))
					} else {
						db.Put([]byte(k), []byte(value100(rng.Intn(100))))
					}
				}
			}()
		}
		deadline := time.Now().Add(time.Duration(sweepStep(3000, 800)) * time.Millisecond)
		for time.Now().Before(deadline) {
			it := newIter(t, db, nil)
			prev := ""
			for it.SeekToFirst(); it.Valid(); it.Next() {
				k := string(it.Key())
				if k <= prev {
					t.Errorf("key %q after %q", k, prev)
					break
				}
				prev = k
			}
			if err := it.Err(); err != nil {
				t.Errorf("Err = %v", err)
			}
			it.Close()
		}
		stop.Store(true)
		wg.Wait()
	})
}

func FuzzUserIterator(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{0, 0, 0, 255, 255, 1, 2, 1, 2, 9})
	f.Fuzz(func(t *testing.T, ops []byte) {
		db := openDB(t, t.TempDir(), smallOptions())
		defer db.Close()
		model := map[string]string{}
		for i, b := range ops {
			k := fmt.Sprintf("k%02d", b%16)
			switch b >> 6 {
			case 0, 1:
				v := fmt.Sprintf("v%d-%s", i, value100(i))
				mustPut(t, db, k, v)
				model[k] = v
			case 2:
				mustDelete(t, db, k)
				delete(model, k)
			default:
				it := newIter(t, db, &IterOptions{Lower: []byte(k)})
				checkPairs(t, drainIter(t, it), modelPairs(model, []byte(k), nil))
				it.Close()
			}
		}
		it := newIter(t, db, nil)
		defer it.Close()
		checkPairs(t, drainIter(t, it), modelPairs(model, nil, nil))
	})
}
