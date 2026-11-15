// SPDX-License-Identifier: MIT

package shale

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
)

func modelDeleteRange(model map[string]string, start, end string) {
	for k := range model {
		if k >= start && k < end {
			delete(model, k)
		}
	}
}

func mustDeleteRange(t testing.TB, db *DB, start, end string) {
	t.Helper()
	if err := db.DeleteRange([]byte(start), []byte(end)); err != nil {
		t.Fatalf("DeleteRange(%q, %q): %v", start, end, err)
	}
}

func randomOpsWithRanges(t testing.TB, db *DB, model map[string]string, rng *rand.Rand, n, keys int) {
	t.Helper()
	for range n {
		switch rng.Intn(10) {
		case 0:
			a, b := rng.Intn(keys), rng.Intn(keys)
			if a > b {
				a, b = b, a
			}
			start, end := fmt.Sprintf("k%04d", a), fmt.Sprintf("k%04d", b)
			mustDeleteRange(t, db, start, end)
			modelDeleteRange(model, start, end)
		case 1:
			k := fmt.Sprintf("k%04d", rng.Intn(keys))
			mustDelete(t, db, k)
			delete(model, k)
		default:
			k := fmt.Sprintf("k%04d", rng.Intn(keys))
			v := fmt.Sprintf("v%d-%s", rng.Int(), value100(rng.Intn(50)))
			mustPut(t, db, k, v)
			model[k] = v
		}
	}
}

func checkAgainstModel(t testing.TB, db *DB, model map[string]string, keys int) {
	t.Helper()
	it := newIter(t, db, nil)
	checkPairs(t, drainIter(t, it), modelPairs(model, nil, nil))
	it.Close()
	for i := range keys {
		k := fmt.Sprintf("k%04d", i)
		want, ok := model[k]
		got, err := db.Get([]byte(k))
		switch {
		case ok && (err != nil || string(got) != want):
			t.Fatalf("Get(%q) = %q, %v, want %q", k, got, err, want)
		case !ok && !errors.Is(err, ErrNotFound):
			t.Fatalf("Get(%q) = %q, %v, want ErrNotFound", k, got, err)
		}
	}
}

func TestDeleteRange(t *testing.T) {
	t.Parallel()

	t.Run("Should delete the keys in the range and keep the others", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		for _, k := range []string{"a", "b", "c", "d", "e"} {
			mustPut(t, db, k, "v-"+k)
		}
		mustDeleteRange(t, db, "b", "d")
		mustGet(t, db, "a", "v-a")
		mustNotFind(t, db, "b")
		mustNotFind(t, db, "c")
		mustGet(t, db, "d", "v-d")
		mustGet(t, db, "e", "v-e")
		it := newIter(t, db, nil)
		defer it.Close()
		checkPairs(t, drainIter(t, it), []pair{{"a", "v-a"}, {"d", "v-d"}, {"e", "v-e"}})
	})

	t.Run("Should let a key come back after the range delete", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		mustPut(t, db, "k", "old")
		mustDeleteRange(t, db, "a", "z")
		mustNotFind(t, db, "k")
		mustPut(t, db, "k", "new")
		mustGet(t, db, "k", "new")
		it := newIter(t, db, nil)
		defer it.Close()
		checkPairs(t, drainIter(t, it), []pair{{"k", "new"}})
	})

	t.Run("Should do nothing for an empty or inverted range", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		mustPut(t, db, "b", "v")
		mustDeleteRange(t, db, "b", "b")
		mustDeleteRange(t, db, "c", "a")
		mustGet(t, db, "b", "v")
		var b Batch
		b.DeleteRange([]byte("a"), []byte("a"))
		b.DeleteRange([]byte("z"), []byte("a"))
		if b.Len() != 0 {
			t.Fatalf("the batch kept %d empty ranges", b.Len())
		}
	})

	t.Run("Should apply the operations of a batch in order", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), smallOptions())
		defer db.Close()
		mustPut(t, db, "x", "before")
		var b Batch
		b.Put([]byte("a"), []byte("1"))
		b.Put([]byte("b"), []byte("2"))
		b.DeleteRange([]byte("a"), []byte("c"))
		b.Put([]byte("b"), []byte("3"))
		b.Put([]byte("x"), []byte("after"))
		if err := db.Write(&b); err != nil {
			t.Fatal(err)
		}
		mustNotFind(t, db, "a")
		mustGet(t, db, "b", "3")
		mustGet(t, db, "x", "after")
	})

	t.Run("Should match a map through flushes and compactions", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), smallOptions())
		defer db.Close()
		model := map[string]string{}
		rng := rand.New(rand.NewSource(7))
		for range sweepStep(25, 8) {
			randomOpsWithRanges(t, db, model, rng, 250, 200)
			checkAgainstModel(t, db, model, 200)
		}
		waitQuiet(t, db)
		checkAgainstModel(t, db, model, 200)
		checkSeqInvariant(t, db)
	})

	t.Run("Should match a map after a reopen", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		db := openDB(t, dir, smallOptions())
		model := map[string]string{}
		rng := rand.New(rand.NewSource(8))
		for range 4 {
			randomOpsWithRanges(t, db, model, rng, 300, 120)
			closeDB(t, db)
			db = openDB(t, dir, smallOptions())
			checkAgainstModel(t, db, model, 120)
		}
		closeDB(t, db)
	})

	t.Run("Should recover a log that has only a range delete", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		db := openDB(t, dir, Options{})
		mustPut(t, db, "a", "1")
		mustPut(t, db, "b", "2")
		closeDB(t, db)
		db = openDB(t, dir, Options{})
		mustDeleteRange(t, db, "a", "b")
		copied := t.TempDir()
		copyDir(t, dir, copied)
		closeDB(t, db)
		recovered := openDB(t, copied, Options{})
		defer recovered.Close()
		mustNotFind(t, recovered, "a")
		mustGet(t, recovered, "b", "2")
		if recovered.Recovery().Batches == 0 {
			t.Fatal("recovery did not replay the log")
		}
	})

	t.Run("Should write a table that has only range deletes and read through it", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), noCompactionOptions())
		defer db.Close()
		for i := range 20 {
			mustPut(t, db, string(keyN(i)), value100(i))
		}
		waitIdle(t, db)
		tables := tableCount(db)
		mustDeleteRange(t, db, string(keyN(0)), string(keyN(1000)))
		for db.state.Load().imm == nil && tableCount(db) == tables {
			mustPut(t, db, "zzz", value100(1))
		}
		waitIdle(t, db)
		for i := range 20 {
			mustNotFind(t, db, string(keyN(i)))
		}
		mustGet(t, db, "zzz", value100(1))
		state := db.acquireState()
		defer state.release()
		withTombs := 0
		for _, tbl := range state.tables {
			if len(tbl.tombs) > 0 {
				withTombs++
			}
		}
		if withTombs == 0 {
			t.Fatal("no table holds the range delete")
		}
		checkSeqInvariant(t, db)
	})

	t.Run("Should flush a memtable that has only range deletes", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), noCompactionOptions())
		defer db.Close()
		for i := 0; db.state.Load().mem.approximateSize() <= db.opts.memtableSize(); i++ {
			mustPut(t, db, string(keyN(i)), value100(i))
		}
		flushes := db.flushes.Load()
		mustDeleteRange(t, db, string(keyN(0)), string(keyN(1000)))
		long := string(make([]byte, 200))
		for i := 0; db.flushes.Load() < flushes+2; i++ {
			mustDeleteRange(t, db, fmt.Sprintf("zz-%06d-%s", i, long), fmt.Sprintf("zz-%06d-%s~", i, long))
			if i > 5000 {
				t.Fatal("the memtable was not frozen twice")
			}
		}
		waitIdle(t, db)
		for i := range 10 {
			mustNotFind(t, db, string(keyN(i)))
		}
		state := db.acquireState()
		onlyTombs := 0
		for _, tbl := range state.tables {
			it := tbl.reader.NewIterator()
			it.SeekToFirst()
			if !it.Valid() && len(tbl.tombs) > 0 {
				onlyTombs++
			}
			it.Close()
		}
		state.release()
		if onlyTombs == 0 {
			t.Fatal("no table has only range deletes")
		}
		checkSeqInvariant(t, db)
		dir := db.dir
		closeDB(t, db)
		reopened := openDB(t, dir, noCompactionOptions())
		defer reopened.Close()
		for i := range 10 {
			mustNotFind(t, reopened, string(keyN(i)))
		}
	})

	t.Run("Should keep the view of a snapshot after a range delete, a flush and a compaction", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), smallOptions())
		defer db.Close()
		model := map[string]string{}
		rng := rand.New(rand.NewSource(9))
		randomOpsWithRanges(t, db, model, rng, 500, 150)
		snap, err := db.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		defer snap.Release()
		frozen := map[string]string{}
		for k, v := range model {
			frozen[k] = v
		}
		mustDeleteRange(t, db, "k0000", "k9999")
		randomOpsWithRanges(t, db, model, rng, 2500, 150)
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
		checkAgainstModel(t, db, model, 150)
	})

	t.Run("Should drop the covered entries and the range delete when a compaction reaches the oldest table", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), smallOptions())
		defer db.Close()
		for i := range 300 {
			mustPut(t, db, string(keyN(i)), value100(i))
		}
		mustDeleteRange(t, db, string(keyN(0)), string(keyN(300)))
		next := 300
		for db.Metrics().Compactions < 2 && next < 20000 {
			putFiller(t, db, next)
			next++
		}
		waitQuiet(t, db)
		for i := range 300 {
			mustNotFind(t, db, string(keyN(i)))
		}
		state := db.acquireState()
		defer state.release()
		tombs := 0
		for _, tbl := range state.tables {
			tombs += len(tbl.tombs)
		}
		if tableCount(db) == 1 && tombs != 0 {
			t.Fatalf("one table is left and it still holds %d range deletes", tombs)
		}
		checkSeqInvariant(t, db)
	})

	t.Run("Should not lose a key that comes after a range delete in a partial compaction", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), smallOptions())
		defer db.Close()
		model := map[string]string{}
		rng := rand.New(rand.NewSource(10))
		for range sweepStep(40, 10) {
			randomOpsWithRanges(t, db, model, rng, 120, 60)
		}
		waitQuiet(t, db)
		checkAgainstModel(t, db, model, 60)
		checkSeqInvariant(t, db)
	})
}

func TestDeleteRangeConcurrent(t *testing.T) {
	t.Parallel()
	t.Run("Should return ordered keys while range deletes and writes run", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), smallOptions())
		defer db.Close()
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			rng := rand.New(rand.NewSource(11))
			for {
				select {
				case <-stop:
					return
				default:
				}
				a, b := rng.Intn(200), rng.Intn(200)
				if a > b {
					a, b = b, a
				}
				if rng.Intn(3) == 0 {
					db.DeleteRange([]byte(fmt.Sprintf("k%04d", a)), []byte(fmt.Sprintf("k%04d", b)))
				} else {
					db.Put([]byte(fmt.Sprintf("k%04d", a)), []byte(value100(a)))
				}
			}
		}()
		for range sweepStep(300, 60) {
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
		close(stop)
		<-done
	})
}

func FuzzDeleteRange(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{255, 0, 255, 1, 200, 40, 9, 128})
	f.Fuzz(func(t *testing.T, ops []byte) {
		db := openDB(t, t.TempDir(), smallOptions())
		defer db.Close()
		model := map[string]string{}
		for i := 0; i+1 < len(ops); i += 2 {
			a, b := fmt.Sprintf("k%02d", ops[i]%16), fmt.Sprintf("k%02d", ops[i+1]%16)
			switch ops[i] >> 6 {
			case 0, 1:
				v := fmt.Sprintf("v%d-%s", i, value100(i))
				mustPut(t, db, a, v)
				model[a] = v
			case 2:
				if a > b {
					a, b = b, a
				}
				mustDeleteRange(t, db, a, b)
				modelDeleteRange(model, a, b)
			default:
				it := newIter(t, db, &IterOptions{Lower: []byte(a)})
				checkPairs(t, drainIter(t, it), modelPairs(model, []byte(a), nil))
				it.Close()
			}
		}
		it := newIter(t, db, nil)
		defer it.Close()
		checkPairs(t, drainIter(t, it), modelPairs(model, nil, nil))
	})
}

func copyModel(model map[string]string) map[string]string {
	out := make(map[string]string, len(model))
	for k, v := range model {
		out[k] = v
	}
	return out
}

func TestRangeDeleteCrashWindows(t *testing.T) {
	t.Parallel()
	const keys = 80

	flushImage := func(t *testing.T, stage string) (string, map[string]string) {
		t.Helper()
		g := newGate(stage)
		dir := t.TempDir()
		opts := smallOptions()
		opts.flushHook = g.hook
		db := openDB(t, dir, opts)
		model := map[string]string{}
		rng := rand.New(rand.NewSource(21))
		for db.state.Load().imm == nil {
			randomOpsWithRanges(t, db, model, rng, 5, keys)
		}
		awaitGate(t, g)
		image := t.TempDir()
		copyDir(t, dir, image)
		want := copyModel(model)
		close(g.release)
		closeDB(t, db)
		return image, want
	}

	compactionImage := func(t *testing.T, stage string) (string, map[string]string) {
		t.Helper()
		start, g := newGate("start"), newGate(stage)
		dir := t.TempDir()
		opts := smallOptions()
		opts.compactHook = func(s string) error {
			start.hook(s)
			return g.hook(s)
		}
		db := openDB(t, dir, opts)
		model := map[string]string{}
		rng := rand.New(rand.NewSource(22))
		for tableCount(db) < 4 {
			randomOpsWithRanges(t, db, model, rng, 5, keys)
		}
		awaitGate(t, start)
		waitIdle(t, db)
		close(start.release)
		awaitGate(t, g)
		image := t.TempDir()
		copyDir(t, dir, image)
		want := copyModel(model)
		close(g.release)
		closeDB(t, db)
		return image, want
	}

	verify := func(t *testing.T, dir string, want map[string]string) {
		t.Helper()
		db := openDB(t, dir, noCompactionOptions())
		defer db.Close()
		checkAgainstModel(t, db, want, keys)
		checkDirectory(t, dir, db)
		checkSeqInvariant(t, db)
	}

	for _, stage := range []string{"table", "commit"} {
		t.Run("Should recover every range delete from an image taken at the flush stage "+stage, func(t *testing.T) {
			image, want := flushImage(t, stage)
			verify(t, image, want)
		})
	}
	for _, stage := range []string{"output", "commit"} {
		t.Run("Should recover every range delete from an image taken at the compaction stage "+stage, func(t *testing.T) {
			image, want := compactionImage(t, stage)
			verify(t, image, want)
		})
	}
}
