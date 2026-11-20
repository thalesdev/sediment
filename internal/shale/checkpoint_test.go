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
)

func TestFlush(t *testing.T) {
	t.Parallel()

	t.Run("Should write the memtable to a table", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		mustPut(t, db, "a", "1")
		mustDeleteRange(t, db, "x", "y")
		if tableCount(db) != 0 {
			t.Fatal("setup")
		}
		if err := db.Flush(); err != nil {
			t.Fatal(err)
		}
		state := db.state.Load()
		if state.imm != nil || !state.mem.empty() || len(state.tables) != 1 {
			t.Fatalf("imm=%v empty=%v tables=%d", state.imm != nil, state.mem.empty(), len(state.tables))
		}
		mustGet(t, db, "a", "1")
		if err := db.Flush(); err != nil {
			t.Fatal(err)
		}
		if tableCount(db) != 1 {
			t.Fatal("a Flush with nothing to flush made a table")
		}
	})

	t.Run("Should return ErrClosed after Close", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), Options{})
		closeDB(t, db)
		if err := db.Flush(); !errors.Is(err, ErrClosed) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("Should not lose or block writes that run at the same time", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), smallOptions())
		defer db.Close()
		var acked atomic.Int64
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
					t.Error(err)
					return
				}
				acked.Store(int64(i + 1))
			}
		}()
		for range sweepStep(40, 10) {
			if err := db.Flush(); err != nil {
				t.Fatal(err)
			}
		}
		close(stop)
		wg.Wait()
		for i := range int(acked.Load()) {
			mustGet(t, db, string(keyN(i)), value100(i))
		}
	})
}

func openCheckpoint(t *testing.T, dir string) *DB {
	t.Helper()
	return openDB(t, dir, noCompactionOptions())
}

func TestCheckpoint(t *testing.T) {
	t.Parallel()

	t.Run("Should hold every write that returned before the call", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), smallOptions())
		defer db.Close()
		model := map[string]string{}
		randomOpsWithRanges(t, db, model, randSource(31), 1500, 150)
		dir := filepath.Join(t.TempDir(), "backup")
		info, err := db.Checkpoint(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Tables == 0 || info.Bytes == 0 || info.Seq == 0 {
			t.Fatalf("info %+v", info)
		}
		randomOpsWithRanges(t, db, map[string]string{}, randSource(32), 500, 150)
		cp := openCheckpoint(t, dir)
		defer cp.Close()
		checkAgainstModel(t, cp, model, 150)
		checkDirectory(t, dir, cp)
		checkSeqInvariant(t, cp)
	})

	t.Run("Should be a consistent prefix when the DB takes writes at the same time", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), smallOptions())
		defer db.Close()
		var acked atomic.Int64
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
					t.Error(err)
					return
				}
				acked.Store(int64(i + 1))
			}
		}()
		root := t.TempDir()
		for n := range sweepStep(12, 4) {
			before := int(acked.Load())
			dir := filepath.Join(root, fmt.Sprintf("backup-%d", n))
			if _, err := db.Checkpoint(dir); err != nil {
				t.Fatal(err)
			}
			cp := openCheckpoint(t, dir)
			present := 0
			for i := 0; ; i++ {
				v, err := cp.Get(keyN(i))
				if err != nil {
					break
				}
				if string(v) != value100(i) {
					t.Fatalf("key %d has the wrong value", i)
				}
				present++
			}
			if present < before {
				t.Fatalf("the checkpoint has %d keys, and %d were acknowledged before the call", present, before)
			}
			for i := present; i < present+200; i++ {
				if _, err := cp.Get(keyN(i)); err == nil {
					t.Fatalf("the checkpoint has key %d after a gap at %d", i, present)
				}
			}
			closeDB(t, cp)
		}
		close(stop)
		wg.Wait()
	})

	t.Run("Should keep working while the checkpoint reads a table that a compaction replaced", func(t *testing.T) {
		t.Parallel()
		start, g := newGate("start"), newGate("commit")
		opts := smallOptions()
		opts.compactHook = func(s string) error {
			start.hook(s)
			return g.hook(s)
		}
		db := openDB(t, t.TempDir(), opts)
		defer db.Close()
		next := 0
		for tableCount(db) < 4 {
			putFiller(t, db, next)
			next++
		}
		awaitGate(t, start)
		close(start.release)
		awaitGate(t, g)
		dir := filepath.Join(t.TempDir(), "backup")
		done := make(chan error, 1)
		go func() { _, err := db.Checkpoint(dir); done <- err }()
		close(g.release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		waitQuiet(t, db)
		cp := openCheckpoint(t, dir)
		defer cp.Close()
		for i := range next {
			mustGet(t, cp, string(keyN(i)), value100(i))
		}
	})

	t.Run("Should refuse a directory that exists", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		mustPut(t, db, "a", "1")
		dir := t.TempDir()
		if _, err := db.Checkpoint(dir); err == nil {
			t.Fatal("a directory that exists was accepted")
		}
	})

	t.Run("Should publish the directory only when it is whole", func(t *testing.T) {
		t.Parallel()
		for _, stage := range []string{"linked", "manifest"} {
			opts := smallOptions()
			opts.checkpointHook = func(s string) error {
				if s == stage {
					return errors.New("stopped")
				}
				return nil
			}
			db := openDB(t, t.TempDir(), opts)
			mustPut(t, db, "a", "1")
			dir := filepath.Join(t.TempDir(), "backup")
			if _, err := db.Checkpoint(dir); err == nil {
				t.Fatalf("a checkpoint that failed at %s succeeded", stage)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the directory exists after a failure at %s: %v", stage, err)
			}
			if _, err := os.Stat(dir + ".tmp"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the temporary directory is left after a failure at %s: %v", stage, err)
			}
			mustGet(t, db, "a", "1")
			closeDB(t, db)
		}
	})

	t.Run("Should not have the final name while the files are linked", func(t *testing.T) {
		t.Parallel()
		g := newGate("linked")
		opts := smallOptions()
		opts.checkpointHook = g.hook
		db := openDB(t, t.TempDir(), opts)
		defer db.Close()
		mustPut(t, db, "a", "1")
		dir := filepath.Join(t.TempDir(), "backup")
		done := make(chan error, 1)
		go func() { _, err := db.Checkpoint(dir); done <- err }()
		awaitGate(t, g)
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the directory exists before the end: %v", err)
		}
		if _, err := os.Stat(dir + ".tmp"); err != nil {
			t.Fatalf("no temporary directory: %v", err)
		}
		close(g.release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		cp := openCheckpoint(t, dir)
		defer cp.Close()
		mustGet(t, cp, "a", "1")
	})

	t.Run("Should share the files by a hard link", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		mustPut(t, db, "a", "1")
		dir := filepath.Join(t.TempDir(), "backup")
		if _, err := db.Checkpoint(dir); err != nil {
			t.Fatal(err)
		}
		tables := tableFiles(t, dir)
		if len(tables) != 1 {
			t.Fatalf("%d tables", len(tables))
		}
		a, err := os.Stat(filepath.Join(dir, tableName(tables[0])))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.Stat(filepath.Join(db.dir, tableName(tables[0])))
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(a, b) {
			t.Fatal("the checkpoint copied the table instead of linking it")
		}
	})

	t.Run("Should copy a file when a link is not possible", func(t *testing.T) {
		t.Parallel()
		src := filepath.Join(t.TempDir(), "src")
		dst := filepath.Join(t.TempDir(), "dst")
		if err := os.WriteFile(src, []byte("table bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := copyFile(src, dst); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(dst)
		if err != nil || string(got) != "table bytes" {
			t.Fatalf("%q, %v", got, err)
		}
		if err := copyFile(src, dst); err == nil {
			t.Fatal("copyFile overwrote a file")
		}
	})

	t.Run("Should take a checkpoint of an empty DB", func(t *testing.T) {
		t.Parallel()
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		dir := filepath.Join(t.TempDir(), "backup")
		info, err := db.Checkpoint(dir)
		if err != nil || info.Tables != 0 {
			t.Fatalf("%+v, %v", info, err)
		}
		cp := openCheckpoint(t, dir)
		defer cp.Close()
		mustNotFind(t, cp, "a")
	})
}
