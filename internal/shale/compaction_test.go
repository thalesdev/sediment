// SPDX-License-Identifier: MIT

package shale

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thalesdev/sediment/internal/ikey"
	"github.com/thalesdev/sediment/internal/manifest"
)

type entry struct {
	user  string
	seq   uint64
	kind  ikey.Kind
	value string
}

type sliceSource struct {
	entries []kv
	pos     int
	err     error
}

type kv struct{ key, value []byte }

func newSliceSource(entries ...entry) *sliceSource {
	s := &sliceSource{}
	for _, e := range entries {
		s.entries = append(s.entries, kv{ikey.Append(nil, []byte(e.user), e.seq, e.kind), []byte(e.value)})
	}
	return s
}

func (s *sliceSource) SeekToFirst()  { s.pos = 0 }
func (s *sliceSource) Seek(k []byte) { panic("not used") }
func (s *sliceSource) Next()         { s.pos++ }
func (s *sliceSource) Valid() bool   { return s.err == nil && s.pos < len(s.entries) }
func (s *sliceSource) Key() []byte   { return s.entries[s.pos].key }
func (s *sliceSource) Value() []byte { return s.entries[s.pos].value }
func (s *sliceSource) Err() error    { return s.err }
func (s *sliceSource) Close() error  { return nil }

func drainDrop(t *testing.T, d *dropIter) []entry {
	t.Helper()
	var out []entry
	for d.SeekToFirst(); d.Valid(); d.Next() {
		user, seq, kind, err := ikey.Parse(d.Key())
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, entry{string(user), seq, kind, string(d.Value())})
	}
	if err := d.Err(); err != nil {
		t.Fatalf("Err = %v", err)
	}
	return out
}

func assertEntries(t *testing.T, got, want []entry) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDropIter(t *testing.T) {
	t.Parallel()
	put, del := ikey.KindPut, ikey.KindDelete

	t.Run("Should keep only the newest version of each key", func(t *testing.T) {
		src := newSliceSource(
			entry{"a", 9, put, "v9"}, entry{"a", 5, put, "v5"}, entry{"a", 1, put, "v1"},
			entry{"b", 3, put, "b3"},
		)
		got := drainDrop(t, newDropIter(src, false))
		assertEntries(t, got, []entry{{"a", 9, put, "v9"}, {"b", 3, put, "b3"}})
	})

	t.Run("Should keep a delete when the run does not include the oldest table and drop the older version", func(t *testing.T) {
		src := newSliceSource(entry{"a", 9, del, ""}, entry{"a", 5, put, "v5"})
		assertEntries(t, drainDrop(t, newDropIter(src, false)), []entry{{"a", 9, del, ""}})
	})

	t.Run("Should drop a delete and the older versions when the run includes the oldest table", func(t *testing.T) {
		src := newSliceSource(entry{"a", 9, del, ""}, entry{"a", 5, put, "v5"}, entry{"b", 2, put, "b2"})
		assertEntries(t, drainDrop(t, newDropIter(src, true)), []entry{{"b", 2, put, "b2"}})
	})

	t.Run("Should keep a put that is newer than a delete", func(t *testing.T) {
		for _, dropDeletes := range []bool{false, true} {
			src := newSliceSource(entry{"a", 9, put, "v9"}, entry{"a", 5, del, ""})
			assertEntries(t, drainDrop(t, newDropIter(src, dropDeletes)), []entry{{"a", 9, put, "v9"}})
		}
	})

	t.Run("Should handle keys that are interleaved", func(t *testing.T) {
		entries := []entry{
			{"a", 3, put, "a3"},
			{"b", 9, del, ""}, {"b", 4, put, "b4"},
			{"c", 7, put, "c7"}, {"c", 2, del, ""},
		}
		assertEntries(t, drainDrop(t, newDropIter(newSliceSource(entries...), true)), []entry{{"a", 3, put, "a3"}, {"c", 7, put, "c7"}})
		assertEntries(t, drainDrop(t, newDropIter(newSliceSource(entries...), false)), []entry{{"a", 3, put, "a3"}, {"b", 9, del, ""}, {"c", 7, put, "c7"}})
	})

	t.Run("Should be empty when it drops everything", func(t *testing.T) {
		src := newSliceSource(entry{"a", 9, del, ""}, entry{"a", 5, put, "v5"}, entry{"b", 4, del, ""})
		d := newDropIter(src, true)
		d.SeekToFirst()
		if d.Valid() || d.Err() != nil {
			t.Fatalf("valid=%v err=%v", d.Valid(), d.Err())
		}
	})

	t.Run("Should return the error of a key that is not valid", func(t *testing.T) {
		src := &sliceSource{entries: []kv{{[]byte("short"), []byte("v")}}}
		d := newDropIter(src, false)
		d.SeekToFirst()
		if d.Valid() || !errors.Is(d.Err(), ikey.ErrCorrupt) {
			t.Fatalf("valid=%v err=%v", d.Valid(), d.Err())
		}
	})

	t.Run("Should return the error of the source", func(t *testing.T) {
		boom := errors.New("boom")
		src := newSliceSource(entry{"a", 9, put, "v"})
		src.err = boom
		d := newDropIter(src, false)
		d.SeekToFirst()
		if d.Valid() || !errors.Is(d.Err(), boom) {
			t.Fatalf("valid=%v err=%v", d.Valid(), d.Err())
		}
	})

	t.Run("Should start again at SeekToFirst", func(t *testing.T) {
		src := newSliceSource(entry{"a", 9, put, "v9"}, entry{"a", 5, put, "v5"}, entry{"b", 1, put, "b1"})
		d := newDropIter(src, false)
		first := drainDrop(t, d)
		second := drainDrop(t, d)
		assertEntries(t, second, first)
	})
}

func checkSeqInvariant(t testing.TB, db *DB) {
	t.Helper()
	state := db.acquireState()
	defer state.release()
	metas := make([]manifest.TableMeta, len(state.tables))
	for i, tbl := range state.tables {
		metas[i] = tbl.meta
		if i > 0 && state.tables[i-1].meta.LargestSeq < tbl.meta.LargestSeq {
			t.Fatalf("tables %d and %d are not in order of sequence number", state.tables[i-1].meta.Number, tbl.meta.Number)
		}
	}
	if err := checkSeqRanges(metas, db.lastSeq.Load()); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range state.tables {
		it := tbl.reader.NewIterator()
		for it.SeekToFirst(); it.Valid(); it.Next() {
			_, seq, _, err := ikey.Parse(it.Key())
			if err != nil {
				t.Fatal(err)
			}
			if seq < tbl.meta.SmallestSeq || seq > tbl.meta.LargestSeq {
				t.Fatalf("table %d has sequence number %d outside of %d to %d", tbl.meta.Number, seq, tbl.meta.SmallestSeq, tbl.meta.LargestSeq)
			}
		}
		if err := it.Err(); err != nil {
			t.Fatal(err)
		}
		it.Close()
	}
}

type getGate struct {
	reached chan struct{}
	release chan struct{}
	armed   atomic.Bool
}

func newGetGate() *getGate {
	g := &getGate{reached: make(chan struct{}, 1), release: make(chan struct{})}
	g.armed.Store(true)
	return g
}

func (g *getGate) hook(stage string) {
	if stage != "state" || !g.armed.CompareAndSwap(true, false) {
		return
	}
	g.reached <- struct{}{}
	<-g.release
}

func tableCount(db *DB) int { return len(db.state.Load().tables) }

func putFiller(t testing.TB, db *DB, n int) {
	t.Helper()
	mustPut(t, db, string(keyN(n)), value100(n))
}

func fillUntilTables(t testing.TB, db *DB, next *int, want int) {
	t.Helper()
	for tableCount(db) < want {
		putFiller(t, db, *next)
		*next++
		if *next > 20000 {
			t.Fatalf("the DB has %d tables, want %d", tableCount(db), want)
		}
	}
}

func awaitCompactions(t testing.TB, db *DB, want uint64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for db.Metrics().Compactions < want {
		if time.Now().After(deadline) {
			t.Fatal("the compaction did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCompactionOrder(t *testing.T) {
	t.Parallel()
	t.Run("Should order the tables by sequence number and not by file number", func(t *testing.T) {
		start, done := newGate("start"), newGate("done")
		opts := smallOptions()
		opts.compactHook = func(stage string) error {
			start.hook(stage)
			done.hook(stage)
			return nil
		}
		dir := t.TempDir()
		db := openDB(t, dir, opts)
		defer db.Close()
		next := 0
		mustPut(t, db, "trap", "old")
		fillUntilTables(t, db, &next, 4)
		awaitGate(t, start)

		mustPut(t, db, "trap", "new")
		frozen := db.state.Load()
		fillUntilTables(t, db, &next, len(frozen.tables)+1)
		waitIdle(t, db)
		newest := db.state.Load().tables[0]

		close(start.release)
		awaitGate(t, done)
		state := db.state.Load()
		mustGet(t, db, "trap", "new")
		var output *table
		for _, tbl := range state.tables {
			if tbl.meta.LargestSeq < newest.meta.SmallestSeq {
				output = tbl
			}
		}
		if output == nil {
			t.Fatal("the compaction output is not older than the table of the flush")
		}
		if output.meta.Number <= newest.meta.Number {
			t.Fatalf("output %d has a number below the table of the flush, %d, and the test does not cover the order", output.meta.Number, newest.meta.Number)
		}
		if state.tables[0] != newest {
			t.Fatalf("the newest table is %d, want %d", state.tables[0].meta.Number, newest.meta.Number)
		}
		checkSeqInvariant(t, db)
		close(done.release)
		waitQuiet(t, db)
		mustGet(t, db, "trap", "new")
		checkSeqInvariant(t, db)
		closeDB(t, db)

		reopened := openDB(t, dir, noCompactionOptions())
		defer reopened.Close()
		mustGet(t, reopened, "trap", "new")
		checkSeqInvariant(t, reopened)
	})
}

func TestCompactionGet(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, opts Options) (*DB, *gate, *int) {
		t.Helper()
		start := newGate("start")
		prev := opts.compactHook
		opts.compactHook = func(stage string) error {
			start.hook(stage)
			if prev != nil {
				return prev(stage)
			}
			return nil
		}
		db := openDB(t, t.TempDir(), opts)
		next := 0
		mustPut(t, db, "trap", "v1")
		fillUntilTables(t, db, &next, 3)
		waitIdle(t, db)
		return db, start, &next
	}

	t.Run("Should not lose a version that a compaction drops while a Get is between the state and the sequence number", func(t *testing.T) {
		gg := newGetGate()
		opts := smallOptions()
		opts.getHook = gg.hook
		db, start, next := setup(t, opts)
		defer db.Close()

		type result struct {
			value []byte
			err   error
		}
		results := make(chan result, 1)
		go func() {
			v, err := db.Get([]byte("trap"))
			results <- result{v, err}
		}()
		select {
		case <-gg.reached:
		case <-time.After(10 * time.Second):
			t.Fatal("the Get did not reach the hook")
		}

		mustPut(t, db, "trap", "v2")
		fillUntilTables(t, db, next, 4)
		awaitGate(t, start)
		close(start.release)
		awaitCompactions(t, db, 1)
		waitQuiet(t, db)
		close(gg.release)

		select {
		case r := <-results:
			if r.err != nil || (string(r.value) != "v1" && string(r.value) != "v2") {
				t.Fatalf("Get = %q, %v, want v1 or v2", r.value, r.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the Get did not return")
		}
		mustGet(t, db, "trap", "v2")
	})

	t.Run("Should keep the files of the tables that a Get uses until the Get ends", func(t *testing.T) {
		gg := newGetGate()
		opts := smallOptions()
		opts.getHook = gg.hook
		db, start, next := setup(t, opts)
		dir := db.dir
		defer db.Close()

		inputs := make([]string, 0, 4)
		results := make(chan error, 1)
		go func() {
			_, err := db.Get([]byte("trap"))
			results <- err
		}()
		select {
		case <-gg.reached:
		case <-time.After(10 * time.Second):
			t.Fatal("the Get did not reach the hook")
		}
		for _, tbl := range db.state.Load().tables {
			inputs = append(inputs, tbl.path)
		}

		fillUntilTables(t, db, next, 4)
		awaitGate(t, start)
		close(start.release)
		awaitCompactions(t, db, 1)
		time.Sleep(50 * time.Millisecond)
		for _, path := range inputs {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("the file %s is gone while a Get uses it: %v", filepath.Base(path), err)
			}
		}
		close(gg.release)
		if err := <-results; err != nil {
			t.Fatalf("Get = %v", err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for _, path := range inputs {
			for {
				if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the file %s was not removed", filepath.Base(path))
				}
				time.Sleep(time.Millisecond)
			}
		}
		_ = dir
	})
}

func TestCompactionFreesSpace(t *testing.T) {
	t.Parallel()
	t.Run("Should keep the number of tables and the size on disk limited when the same keys are written many times", func(t *testing.T) {
		db := openDB(t, t.TempDir(), Options{MemtableSize: 16 << 10})
		defer db.Close()
		const keys = 100
		rounds, finals := sweepStep(200, 40), sweepStep(100, 20)

		var peak atomic.Int64
		stop := make(chan struct{})
		sampled := make(chan struct{})
		go func() {
			defer close(sampled)
			for {
				select {
				case <-stop:
					return
				default:
				}
				if n := int64(tableCount(db)); n > peak.Load() {
					peak.Store(n)
				}
				time.Sleep(200 * time.Microsecond)
			}
		}()

		for round := range rounds {
			for i := range keys {
				mustPut(t, db, string(keyN(i)), fmt.Sprintf("round-%04d-%s", round, value100(i)))
			}
		}
		for i := 10; i < keys; i++ {
			mustDelete(t, db, string(keyN(i)))
		}
		for round := range finals {
			for i := range 10 {
				mustPut(t, db, string(keyN(i)), fmt.Sprintf("final-%04d-%s", round, value100(i)))
			}
		}
		waitQuiet(t, db)
		close(stop)
		<-sampled
		m := db.Metrics()
		t.Logf("peak tables %d, tables at the end %d, compactions %d (%d by count), flushed %d bytes, disk %d bytes",
			peak.Load(), m.Tables, m.Compactions, db.byCount.Load(), m.FlushBytes, m.DiskBytes)
		if m.Tables > tableCountTrigger || m.DiskBytes > 256<<10 || peak.Load() > stallTables {
			t.Fatalf("Metrics = %+v, peak tables %d", m, peak.Load())
		}
		if m.Compactions == 0 || m.CompactionBytesRead == 0 || m.CompactionBytesWritten == 0 || m.FlushBytes < uint64(rounds*keys*40) {
			t.Fatalf("Metrics = %+v", m)
		}
		for i := range 10 {
			mustGet(t, db, string(keyN(i)), fmt.Sprintf("final-%04d-%s", finals-1, value100(i)))
		}
		for i := 10; i < keys; i++ {
			mustNotFind(t, db, string(keyN(i)))
		}
		checkSeqInvariant(t, db)
	})

	t.Run("Should remove a delete and the versions that it hides when the compaction includes the oldest table", func(t *testing.T) {
		opts := Options{MemtableSize: 8192}
		start := newGate("start")
		opts.compactHook = start.hook
		db := openDB(t, t.TempDir(), opts)
		defer db.Close()
		const doomed = 20
		for i := range doomed {
			putFiller(t, db, i)
		}
		next := doomed
		fillUntilTables(t, db, &next, 1)
		waitIdle(t, db)
		for i := range doomed {
			mustDelete(t, db, string(keyN(i)))
		}
		fillUntilTables(t, db, &next, 2)
		waitIdle(t, db)
		fillUntilTables(t, db, &next, 3)
		waitIdle(t, db)
		fillUntilTables(t, db, &next, 4)
		awaitGate(t, start)
		close(start.release)
		awaitCompactions(t, db, 1)
		waitQuiet(t, db)

		state := db.acquireState()
		defer state.release()
		found := 0
		for _, tbl := range state.tables {
			it := tbl.reader.NewIterator()
			for it.SeekToFirst(); it.Valid(); it.Next() {
				user, _, _, err := ikey.Parse(it.Key())
				if err != nil {
					t.Fatal(err)
				}
				var n int
				if _, err := fmt.Sscanf(string(user), "key-%08d", &n); err == nil && n < doomed {
					found++
				}
			}
			it.Close()
		}
		if found != 0 {
			t.Fatalf("%d entries of the deleted keys are still in the tables", found)
		}
		for i := range doomed {
			mustNotFind(t, db, string(keyN(i)))
		}
		for i := doomed; i < next; i++ {
			if _, err := db.Get(keyN(i)); err != nil && !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
		}
	})
}

type compactionImage struct {
	dir     string
	written int
	before  int64
	after   int64
}

func takeCompactionImage(t *testing.T, stage string) compactionImage {
	t.Helper()
	start, g := newGate("start"), newGate(stage)
	dir := t.TempDir()
	size := func() int64 {
		info, err := os.Stat(filepath.Join(dir, manifestName))
		if err != nil {
			t.Fatal(err)
		}
		return info.Size()
	}
	opts := smallOptions()
	opts.compactHook = func(s string) error {
		start.hook(s)
		return g.hook(s)
	}
	db := openDB(t, dir, opts)
	next := 0
	fillUntilTables(t, db, &next, 4)
	awaitGate(t, start)
	waitIdle(t, db)
	before := size()
	close(start.release)
	awaitGate(t, g)
	after := size()
	image := t.TempDir()
	copyDir(t, dir, image)
	close(g.release)
	closeDB(t, db)
	return compactionImage{dir: image, written: next, before: before, after: after}
}

func verifyFillers(t *testing.T, db *DB, written int) {
	t.Helper()
	for i := range written {
		mustGet(t, db, string(keyN(i)), value100(i))
	}
}

func TestCompactionCrashWindows(t *testing.T) {
	t.Parallel()
	t.Run("Should remove an output that is not committed and keep the inputs", func(t *testing.T) {
		img := takeCompactionImage(t, "output")
		before := tableFiles(t, img.dir)
		db := openDB(t, img.dir, noCompactionOptions())
		defer db.Close()
		verifyFillers(t, db, img.written)
		rec := db.Recovery()
		if rec.ManifestDroppedBytes != 0 || len(rec.KeptTables) != 0 {
			t.Fatalf("Recovery = %+v", rec)
		}
		named := map[uint64]bool{}
		for _, tbl := range db.state.Load().tables {
			named[tbl.meta.Number] = true
		}
		orphans := 0
		for _, n := range before {
			if !named[n] {
				orphans++
				if _, err := os.Stat(filepath.Join(img.dir, tableName(n))); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("the orphan %d was not removed", n)
				}
			}
		}
		if orphans != 1 {
			t.Fatalf("%d orphans, want the output", orphans)
		}
		checkDirectory(t, img.dir, db)
		checkSeqInvariant(t, db)
	})

	t.Run("Should use the output and remove the inputs when the commit is whole", func(t *testing.T) {
		img := takeCompactionImage(t, "commit")
		before := tableFiles(t, img.dir)
		if len(before) < 5 {
			t.Fatalf("image has %d tables, want the inputs and the output", len(before))
		}
		db := openDB(t, img.dir, noCompactionOptions())
		defer db.Close()
		verifyFillers(t, db, img.written)
		checkDirectory(t, img.dir, db)
		if n := len(tableFiles(t, img.dir)); n >= len(before) {
			t.Fatalf("%d tables after Open, %d before: the inputs were not removed", n, len(before))
		}
		checkSeqInvariant(t, db)
	})

	t.Run("Should open as if the compaction did not happen when the last edit is cut", func(t *testing.T) {
		img := takeCompactionImage(t, "commit")
		if img.after <= img.before {
			t.Fatalf("the MANIFEST went from %d to %d bytes", img.before, img.after)
		}
		for cut := int64(0); cut < img.after-img.before; cut += 1 + cut/8 {
			dir := t.TempDir()
			copyDir(t, img.dir, dir)
			if err := os.Truncate(filepath.Join(dir, manifestName), img.before+cut); err != nil {
				t.Fatal(err)
			}
			db := openDB(t, dir, noCompactionOptions())
			verifyFillers(t, db, img.written)
			rec := db.Recovery()
			if rec.ManifestDroppedBytes != int(cut) {
				t.Fatalf("cut %d: ManifestDroppedBytes = %d", cut, rec.ManifestDroppedBytes)
			}
			if (cut > 0) != (len(rec.KeptTables) == 1) {
				t.Fatalf("cut %d: KeptTables = %v", cut, rec.KeptTables)
			}
			checkDirectory(t, dir, db)
			checkSeqInvariant(t, db)
			closeDB(t, db)
		}
	})
}

func TestCompactionStall(t *testing.T) {
	t.Parallel()
	t.Run("Should make writes wait at 36 tables and let them go when the compaction goes on", func(t *testing.T) {
		start := newGate("start")
		opts := smallOptions()
		opts.compactHook = start.hook
		db := openDB(t, t.TempDir(), opts)
		defer db.Close()

		var written atomic.Int64
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if err := db.Put(keyN(i), []byte(value100(i))); err != nil {
					return
				}
				written.Store(int64(i + 1))
			}
		}()
		awaitGate(t, start)
		deadline := time.Now().Add(30 * time.Second)
		for tableCount(db) < stallTables {
			if time.Now().After(deadline) {
				t.Fatalf("only %d tables", tableCount(db))
			}
			time.Sleep(time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
		stuck := written.Load()
		time.Sleep(300 * time.Millisecond)
		if written.Load() != stuck {
			t.Fatal("writes went on at 36 tables")
		}
		if n := tableCount(db); n != stallTables {
			t.Fatalf("%d tables while the writes wait, want %d", n, stallTables)
		}
		close(start.release)
		deadline = time.Now().Add(30 * time.Second)
		for written.Load() < stuck+200 {
			if time.Now().After(deadline) {
				t.Fatal("writes did not go on after the compaction")
			}
			time.Sleep(time.Millisecond)
		}
		close(stop)
		wg.Wait()
		for i := range int(written.Load()) {
			mustGet(t, db, string(keyN(i)), value100(i))
		}
	})

	t.Run("Should return ErrClosed to a write that waits when the DB closes", func(t *testing.T) {
		start := newGate("start")
		opts := smallOptions()
		opts.compactHook = start.hook
		db := openDB(t, t.TempDir(), opts)

		errs := make(chan error, 1)
		go func() {
			for i := 0; ; i++ {
				if err := db.Put(keyN(i), []byte(value100(i))); err != nil {
					errs <- err
					return
				}
			}
		}()
		awaitGate(t, start)
		deadline := time.Now().Add(30 * time.Second)
		for tableCount(db) < stallTables {
			if time.Now().After(deadline) {
				t.Fatalf("only %d tables", tableCount(db))
			}
			time.Sleep(time.Millisecond)
		}
		time.Sleep(300 * time.Millisecond)
		closed := make(chan error, 1)
		go func() { closed <- db.Close() }()
		select {
		case err := <-errs:
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("Put = %v, want ErrClosed", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the write that waits did not return")
		}
		close(start.release)
		select {
		case err := <-closed:
			if err != nil {
				t.Fatalf("Close = %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("Close did not return")
		}
	})
}

func TestCompactionErrors(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"start", "output", "commit"} {
		for _, persistent := range []bool{false, true} {
			name := "Should make the error of the compaction at " + stage + " sticky and let Close and Open work"
			if persistent {
				name += ", when the error does not go away"
			}
			t.Run(name, func(t *testing.T) {
				boom := errors.New("injected")
				dir := t.TempDir()
				opts := smallOptions()
				var once sync.Once
				opts.compactHook = func(s string) error {
					if s != stage {
						return nil
					}
					if persistent {
						return boom
					}
					var err error
					once.Do(func() { err = boom })
					return err
				}
				db := openDB(t, dir, opts)
				acked := 0
				var failure error
				deadline := time.Now().Add(30 * time.Second)
				for failure == nil {
					if time.Now().After(deadline) {
						t.Fatal("the error of the compaction did not reach the writes")
					}
					if err := db.Put(keyN(acked), []byte(value100(acked))); err != nil {
						failure = err
						break
					}
					acked++
					if acked%20 == 0 {
						time.Sleep(time.Millisecond)
					}
				}
				if !errors.Is(failure, boom) {
					t.Fatalf("Put = %v, want the injected error", failure)
				}
				if err := db.Delete([]byte("x")); !errors.Is(err, boom) {
					t.Fatalf("Delete = %v", err)
				}
				for i := range acked {
					mustGet(t, db, string(keyN(i)), value100(i))
				}
				closed := make(chan error, 1)
				go func() { closed <- db.Close() }()
				select {
				case err := <-closed:
					if err != nil {
						t.Fatalf("Close = %v", err)
					}
				case <-time.After(20 * time.Second):
					t.Fatal("Close did not return")
				}
				db = openDB(t, dir, smallOptions())
				defer db.Close()
				checkDirectory(t, dir, db)
				for i := range acked {
					mustGet(t, db, string(keyN(i)), value100(i))
				}
				checkSeqInvariant(t, db)
			})
		}
	}
}

func TestOpenSeqRanges(t *testing.T) {
	t.Parallel()
	stripAndWrite := func(t *testing.T, dir string, edit func(*manifest.Version)) {
		t.Helper()
		version, _, err := readManifest(dir)
		if err != nil {
			t.Fatal(err)
		}
		edit(&version)
		writeManifestFile(t, dir, version.Compact().Encode(nil))
	}

	t.Run("Should open a database of the version 6.2, compute the ranges and write them", func(t *testing.T) {
		dir, written := flushedDB(t)
		stripAndWrite(t, dir, func(v *manifest.Version) {
			for i := range v.Tables[0] {
				v.Tables[0][i].HasSeq = false
				v.Tables[0][i].SmallestSeq, v.Tables[0][i].LargestSeq = 0, 0
			}
		})
		version, _, err := readManifest(dir)
		if err != nil || version.Tables[0][0].HasSeq {
			t.Fatalf("the MANIFEST still has a range: %v", err)
		}
		db := openDB(t, dir, noCompactionOptions())
		verifyFillers(t, db, written)
		checkSeqInvariant(t, db)
		closeDB(t, db)
		version, _, err = readManifest(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range version.Tables[0] {
			if !m.HasSeq || m.SmallestSeq == 0 || m.LargestSeq < m.SmallestSeq {
				t.Fatalf("table %d has the range %+v after Open", m.Number, m)
			}
		}
	})

	t.Run("Should fail and remove nothing for ranges that overlap", func(t *testing.T) {
		dir, _ := flushedDB(t)
		stripAndWrite(t, dir, func(v *manifest.Version) {
			v.Tables[0][1].SmallestSeq = v.Tables[0][0].SmallestSeq
		})
		assertOpenFails(t, dir, manifest.ErrCorrupt)
	})

	t.Run("Should fail and remove nothing for a range above the last sequence number", func(t *testing.T) {
		dir, _ := flushedDB(t)
		stripAndWrite(t, dir, func(v *manifest.Version) {
			v.LastSeq = v.Tables[0][len(v.Tables[0])-1].LargestSeq - 1
		})
		assertOpenFails(t, dir, manifest.ErrCorrupt)
	})
}
