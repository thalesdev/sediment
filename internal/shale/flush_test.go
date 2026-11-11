// SPDX-License-Identifier: MIT

package shale

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFlushAndRead(t *testing.T) {
	t.Run("Should read keys from tables, with overwrites and deletes in newer tables", func(t *testing.T) {
		dir := t.TempDir()
		db := openDB(t, dir, smallOptions())
		model := map[string]string{}
		put := func(i int, gen string) {
			key, value := string(keyN(i)), gen+value100(i)
			mustPut(t, db, key, value)
			model[key] = value
		}
		del := func(i int) {
			mustDelete(t, db, string(keyN(i)))
			delete(model, string(keyN(i)))
		}
		const keys = 400
		for i := range keys {
			put(i, "a")
		}
		for i := 0; i < keys; i += 2 {
			put(i, "b")
		}
		for i := 0; i < keys; i += 3 {
			del(i)
		}
		for i := 0; i < keys; i += 9 {
			put(i, "c")
		}
		for i := 1; i < keys; i += 7 {
			del(i)
			put(i, "d")
		}
		waitIdle(t, db)
		if n := len(db.state.Load().tables); n < 5 {
			t.Fatalf("only %d tables, the test does not cover flushes", n)
		}
		check := func(db *DB) {
			for i := range keys {
				key := string(keyN(i))
				if want, ok := model[key]; ok {
					mustGet(t, db, key, want)
				} else {
					mustNotFind(t, db, key)
				}
			}
		}
		check(db)
		scanTables(t, db)
		closeDB(t, db)

		db = openDB(t, dir, smallOptions())
		defer db.Close()
		checkDirectory(t, dir, db)
		check(db)
		if rec := db.Recovery(); rec.Tables != len(tableFiles(t, dir)) || rec.Tables < 5 {
			t.Fatalf("Recovery = %+v", rec)
		}
	})

	t.Run("Should keep one log and no temporary file after each reopen", func(t *testing.T) {
		dir := t.TempDir()
		next := 0
		for range 5 {
			db := openDB(t, dir, smallOptions())
			checkDirectory(t, dir, db)
			for i := range next {
				mustGet(t, db, string(keyN(i)), value100(i))
			}
			for range 60 {
				mustPut(t, db, string(keyN(next)), value100(next))
				next++
			}
			closeDB(t, db)
		}
	})
}

type gate struct {
	stage   string
	reached chan struct{}
	release chan struct{}
	armed   atomic.Bool
	err     error
}

func newGate(stage string) *gate {
	g := &gate{stage: stage, reached: make(chan struct{}, 1), release: make(chan struct{})}
	g.armed.Store(true)
	return g
}

func (g *gate) hook(stage string) error {
	if stage != g.stage || !g.armed.CompareAndSwap(true, false) {
		return nil
	}
	g.reached <- struct{}{}
	<-g.release
	return g.err
}

func fillUntilFrozen(t testing.TB, db *DB) int {
	t.Helper()
	n := 0
	for db.state.Load().imm == nil {
		mustPut(t, db, string(keyN(n)), value100(n))
		n++
		if n > 5000 {
			t.Fatal("the memtable was not frozen")
		}
	}
	return n
}

func awaitGate(t testing.TB, g *gate) {
	t.Helper()
	select {
	case <-g.reached:
	case <-time.After(20 * time.Second):
		t.Fatal("the flush did not reach the stage")
	}
}

func TestFlushWaiting(t *testing.T) {
	t.Run("Should make a write wait while the frozen memtable is in the flush and let it go after", func(t *testing.T) {
		g := newGate("table")
		opts := smallOptions()
		opts.flushHook = g.hook
		db := openDB(t, t.TempDir(), opts)
		defer db.Close()

		written := fillUntilFrozen(t, db)
		awaitGate(t, g)
		done := make(chan struct{})
		var total atomic.Int64
		go func() {
			defer close(done)
			for i := range 200 {
				n := written + i
				if err := db.Put(keyN(n), []byte(value100(n))); err != nil {
					t.Errorf("Put: %v", err)
					return
				}
				total.Store(int64(n + 1))
			}
		}()
		select {
		case <-done:
			t.Fatal("writes finished while the flush was blocked")
		case <-time.After(300 * time.Millisecond):
		}
		stuck := total.Load()
		time.Sleep(100 * time.Millisecond)
		if total.Load() != stuck {
			t.Fatal("writes went on while the flush was blocked")
		}
		close(g.release)
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatal("writes did not go on after the flush")
		}
		for i := range written + 200 {
			mustGet(t, db, string(keyN(i)), value100(i))
		}
	})
}

func TestFlushErrors(t *testing.T) {
	for _, stage := range []string{"table", "commit", "cleanup"} {
		for _, persistent := range []bool{false, true} {
			testFlushError(t, stage, persistent)
		}
	}
}

func testFlushError(t *testing.T, stage string, persistent bool) {
	name := "Should make the error of the flush at " + stage + " sticky and let Close and Open work"
	if persistent {
		name += ", when the error does not go away"
	}
	{
		t.Run(name, func(t *testing.T) {
			boom := errors.New("injected")
			dir := t.TempDir()
			opts := smallOptions()
			var once sync.Once
			opts.flushHook = func(s string) error {
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
			deadline := time.Now().Add(20 * time.Second)
			for failure == nil {
				if time.Now().After(deadline) {
					t.Fatal("the error of the flush did not reach the writes")
				}
				if err := db.Put(keyN(acked), []byte(value100(acked))); err != nil {
					failure = err
					break
				}
				acked++
				if acked%50 == 0 {
					time.Sleep(time.Millisecond)
				}
			}
			if !errors.Is(failure, boom) {
				t.Fatalf("Put = %v, want the injected error", failure)
			}
			if err := db.Put([]byte("x"), []byte("y")); !errors.Is(err, boom) {
				t.Fatalf("second Put = %v", err)
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
			case <-time.After(10 * time.Second):
				t.Fatal("Close did not return")
			}

			db = openDB(t, dir, smallOptions())
			defer db.Close()
			checkDirectory(t, dir, db)
			for i := range acked {
				mustGet(t, db, string(keyN(i)), value100(i))
			}
			scanTables(t, db)
		})
	}
}

func TestCloseAndFlush(t *testing.T) {
	t.Run("Should wait for the flush in progress and write the frozen memtable before it returns", func(t *testing.T) {
		g := newGate("table")
		dir := t.TempDir()
		opts := smallOptions()
		opts.flushHook = g.hook
		db := openDB(t, dir, opts)
		written := fillUntilFrozen(t, db)
		awaitGate(t, g)
		closed := make(chan error, 1)
		go func() { closed <- db.Close() }()
		select {
		case <-closed:
			t.Fatal("Close returned while the flush was blocked")
		case <-time.After(300 * time.Millisecond):
		}
		close(g.release)
		select {
		case err := <-closed:
			if err != nil {
				t.Fatalf("Close = %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("Close did not return")
		}
		if n := len(tableFiles(t, dir)); n < 1 {
			t.Fatalf("no table was written, %d", n)
		}

		db = openDB(t, dir, smallOptions())
		defer db.Close()
		for i := range written {
			mustGet(t, db, string(keyN(i)), value100(i))
		}
	})

	t.Run("Should let Gets that are running finish before the files close", func(t *testing.T) {
		dir := t.TempDir()
		db := openDB(t, dir, smallOptions())
		for i := range 300 {
			mustPut(t, db, string(keyN(i)), value100(i))
		}
		waitIdle(t, db)
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; ; i++ {
					got, err := db.Get(keyN(i % 300))
					if errors.Is(err, ErrClosed) {
						return
					}
					if err != nil || string(got) != value100(i%300) {
						t.Errorf("Get(%d) = %q, %v", i%300, got, err)
						return
					}
				}
			}()
		}
		time.Sleep(20 * time.Millisecond)
		closeDB(t, db)
		wg.Wait()
	})
}

type crashImage struct {
	dir     string
	written int
	before  int64
	after   int64
}

func takeImage(t *testing.T, stage string) crashImage {
	t.Helper()
	g := newGate(stage)
	dir := t.TempDir()
	var before, after int64
	size := func() int64 {
		info, err := os.Stat(filepath.Join(dir, manifestName))
		if err != nil {
			t.Error(err)
			return 0
		}
		return info.Size()
	}
	opts := smallOptions()
	opts.flushHook = func(s string) error {
		switch s {
		case "table":
			before = size()
		case "commit":
			after = size()
		}
		return g.hook(s)
	}
	db := openDB(t, dir, opts)
	written := fillUntilFrozen(t, db)
	awaitGate(t, g)
	image := t.TempDir()
	copyDir(t, dir, image)
	close(g.release)
	closeDB(t, db)
	return crashImage{dir: image, written: written, before: before, after: after}
}

func verifyImage(t *testing.T, db *DB, written int) {
	t.Helper()
	for i := range written {
		mustGet(t, db, string(keyN(i)), value100(i))
	}
}

func TestCrashWindows(t *testing.T) {
	t.Run("Should remove a table that the MANIFEST does not name and read the data from the logs", func(t *testing.T) {
		img := takeImage(t, "table")
		before := tableFiles(t, img.dir)
		if len(before) != 1 {
			t.Fatalf("image has %d tables, want the one that is not committed", len(before))
		}
		db := openDB(t, img.dir, smallOptions())
		defer db.Close()
		verifyImage(t, db, img.written)
		rec := db.Recovery()
		if rec.ManifestDroppedBytes != 0 || len(rec.KeptTables) != 0 {
			t.Fatalf("Recovery = %+v", rec)
		}
		for _, n := range tableFiles(t, img.dir) {
			if n == before[0] {
				t.Fatalf("table %d was not removed", n)
			}
		}
		if _, err := os.Stat(filepath.Join(img.dir, lostName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the lost directory exists: %v", err)
		}
		checkDirectory(t, img.dir, db)
	})

	t.Run("Should ignore the old log of a table that is committed and never repeat an internal key", func(t *testing.T) {
		img := takeImage(t, "commit")
		oldLogs := logFiles(t, img.dir)
		if len(oldLogs) != 2 {
			t.Fatalf("image has logs %v, want the old one and the active one", oldLogs)
		}
		db := openDB(t, img.dir, smallOptions())
		defer db.Close()
		verifyImage(t, db, img.written)
		if rec := db.Recovery(); rec.Logs != 1 || rec.Tables < 1 {
			t.Fatalf("Recovery = %+v, want only the active log read", rec)
		}
		checkDirectory(t, img.dir, db)
		for i := img.written; i < img.written+600; i++ {
			mustPut(t, db, string(keyN(i)), value100(i))
		}
		waitIdle(t, db)
		verifyImage(t, db, img.written+600)
		scanTables(t, db)
	})

	t.Run("Should open as if the flush did not happen when the last edit of the MANIFEST is cut", func(t *testing.T) {
		img := takeImage(t, "commit")
		if img.after <= img.before {
			t.Fatalf("the MANIFEST went from %d to %d bytes", img.before, img.after)
		}
		for cut := int64(0); cut < img.after-img.before; cut++ {
			dir := t.TempDir()
			copyDir(t, img.dir, dir)
			if err := os.Truncate(filepath.Join(dir, manifestName), img.before+cut); err != nil {
				t.Fatal(err)
			}
			named := tableFiles(t, dir)
			db := openDB(t, dir, smallOptions())
			verifyImage(t, db, img.written)
			rec := db.Recovery()
			if rec.ManifestDroppedBytes != int(cut) {
				t.Fatalf("cut %d: ManifestDroppedBytes = %d", cut, rec.ManifestDroppedBytes)
			}
			if cut == 0 {
				if len(rec.KeptTables) != 0 {
					t.Fatalf("cut 0: KeptTables = %v", rec.KeptTables)
				}
			} else if len(rec.KeptTables) != len(named) {
				t.Fatalf("cut %d: KeptTables = %v, want %v", cut, rec.KeptTables, named)
			}
			for _, n := range named {
				_, errMain := os.Stat(filepath.Join(dir, tableName(n)))
				_, errLost := os.Stat(filepath.Join(dir, lostName, tableName(n)))
				if !errors.Is(errMain, os.ErrNotExist) || (cut > 0) != (errLost == nil) {
					t.Fatalf("cut %d: table %d, main %v, lost %v", cut, n, errMain, errLost)
				}
			}
			checkDirectory(t, dir, db)
			closeDB(t, db)
			if cut > 0 && cut%7 == 0 {
				db = openDB(t, dir, smallOptions())
				verifyImage(t, db, img.written)
				for _, n := range named {
					if _, err := os.Stat(filepath.Join(dir, lostName, tableName(n))); err != nil {
						t.Fatalf("cut %d: table %d left the lost directory: %v", cut, n, err)
					}
				}
				closeDB(t, db)
			}
		}
	})
}
