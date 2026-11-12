// SPDX-License-Identifier: MIT

package shale

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thalesdev/sediment/internal/batch"
	"github.com/thalesdev/sediment/internal/ikey"
	"github.com/thalesdev/sediment/internal/wal"
)

func openDB(t testing.TB, dir string, opts Options) *DB {
	t.Helper()
	db, err := Open(dir, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db
}

func mustPut(t testing.TB, db *DB, key, value string) {
	t.Helper()
	if err := db.Put([]byte(key), []byte(value)); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
}

func mustDelete(t testing.TB, db *DB, key string) {
	t.Helper()
	if err := db.Delete([]byte(key)); err != nil {
		t.Fatalf("Delete(%q): %v", key, err)
	}
}

func mustGet(t testing.TB, db *DB, key, want string) {
	t.Helper()
	got, err := db.Get([]byte(key))
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	if !bytes.Equal(got, []byte(want)) {
		t.Fatalf("Get(%q) = %q, want %q", key, got, want)
	}
}

func mustNotFind(t testing.TB, db *DB, key string) {
	t.Helper()
	if got, err := db.Get([]byte(key)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(%q) = %q, %v, want ErrNotFound", key, got, err)
	}
}

func closeDB(t testing.TB, db *DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func logFiles(t testing.TB, dir string) []uint64 {
	t.Helper()
	numbers, err := listLogs(dir)
	if err != nil {
		t.Fatal(err)
	}
	return numbers
}

func tableFiles(t testing.TB, dir string) []uint64 {
	t.Helper()
	l, err := listDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return l.tables
}

func writeLog(t testing.TB, dir string, n uint64, records ...[]byte) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, logName(n)))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := wal.NewWriter(f)
	for _, r := range records {
		if err := w.AddRecord(r); err != nil {
			t.Fatal(err)
		}
	}
}

func batchBytes(seq uint64, key, value string) []byte {
	var b batch.Batch
	b.Reset(seq)
	b.Put([]byte(key), []byte(value))
	return append([]byte(nil), b.Bytes()...)
}

type fakeLog struct {
	writeErr error
	syncs    int
	writes   int
}

func (f *fakeLog) Write(p []byte) (int, error) {
	f.writes++
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(p), nil
}

func (f *fakeLog) Sync() error {
	f.syncs++
	return nil
}

func swapLog(db *DB, dst *fakeLog) {
	db.mu.Lock()
	db.log = wal.NewWriter(dst)
	db.mu.Unlock()
}

func keyN(n int) []byte { return fmt.Appendf(nil, "key-%08d", n) }

func smallOptions() Options { return Options{MemtableSize: 2048} }

func noCompactionOptions() Options {
	return Options{MemtableSize: 2048, disableCompaction: true}
}

func copyDir(t testing.TB, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == lockName {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func fileNames(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.Name() != lockName {
			names = append(names, e.Name())
		}
	}
	return names
}

func checkDirectory(t testing.TB, dir string, db *DB) {
	t.Helper()
	waitQuiet(t, db)
	l, err := listDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.logs) != 1 {
		t.Fatalf("directory has %d log files %v, want 1", len(l.logs), l.logs)
	}
	if len(l.tmps) != 0 {
		t.Fatalf("directory has temporary files %v", l.tmps)
	}
	named := map[uint64]bool{}
	for _, tbl := range db.state.Load().tables {
		named[tbl.meta.Number] = true
	}
	for _, n := range l.tables {
		if !named[n] {
			t.Fatalf("table file %d is not in the state", n)
		}
		delete(named, n)
	}
	if len(named) != 0 {
		t.Fatalf("tables %v are in the state and not in the directory", named)
	}
}

func waitIdle(t testing.TB, db *DB) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for db.state.Load().imm != nil {
		if time.Now().After(deadline) {
			t.Fatal("the flush did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func waitQuiet(t testing.TB, db *DB) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		state := db.state.Load()
		_, planned := pickCompaction(tableInfos(state.tables))
		if state.imm == nil && !db.compacting.Load() && (!planned || db.opts.disableCompaction) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the flush and the compaction did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func scanTables(t testing.TB, db *DB) {
	t.Helper()
	state := db.acquireState()
	defer state.release()
	seen := map[string]bool{}
	for _, tbl := range state.tables {
		it := tbl.reader.NewIterator()
		var previous []byte
		for it.SeekToFirst(); it.Valid(); it.Next() {
			if previous != nil && ikey.Compare(previous, it.Key()) >= 0 {
				t.Fatalf("table %d: keys %x and %x are not in order", tbl.meta.Number, previous, it.Key())
			}
			previous = append(previous[:0], it.Key()...)
			_, seq, _, err := ikey.Parse(it.Key())
			if err != nil {
				t.Fatal(err)
			}
			id := fmt.Sprintf("%x", it.Key())
			if seen[id] {
				t.Fatalf("internal key %s is in two tables, seq %d", id, seq)
			}
			seen[id] = true
		}
		if err := it.Err(); err != nil {
			t.Fatal(err)
		}
		it.Close()
	}
}

func value100(i int) string {
	return fmt.Sprintf("value-%06d-%s", i, strings.Repeat("v", 80))
}

func sweepStep(full, short int) int {
	if testing.Short() {
		return short
	}
	return full
}
