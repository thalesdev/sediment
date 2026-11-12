// SPDX-License-Identifier: MIT

package shale

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/thalesdev/sediment/internal/batch"
	"github.com/thalesdev/sediment/internal/manifest"
	"github.com/thalesdev/sediment/internal/wal"
)

func flushedDB(t *testing.T) (string, int) {
	t.Helper()
	dir := t.TempDir()
	db := openDB(t, dir, noCompactionOptions())
	written := 400
	if testing.Short() {
		written = 150
	}
	for i := range written {
		mustPut(t, db, string(keyN(i)), value100(i))
	}
	closeDB(t, db)
	if len(tableFiles(t, dir)) < 3 {
		t.Fatalf("directory has %d tables", len(tableFiles(t, dir)))
	}
	return dir, written
}

func writeManifestFile(t *testing.T, dir string, records ...[]byte) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, manifestName))
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

func assertOpenFails(t *testing.T, dir string, target error) {
	t.Helper()
	before := fileNames(t, dir)
	for range 2 {
		_, err := Open(dir, smallOptions())
		if err == nil {
			t.Fatal("Open succeeded")
		}
		if errors.Is(err, ErrLocked) {
			t.Fatal("Open reported a lock instead of the error")
		}
		if target != nil && !errors.Is(err, target) {
			t.Fatalf("Open = %v, want %v", err, target)
		}
	}
	if after := fileNames(t, dir); !slices.Equal(before, after) {
		t.Fatalf("a failed Open changed the directory from %v to %v", before, after)
	}
}

func TestOpenErrors(t *testing.T) {
	t.Parallel()
	t.Run("Should fail and remove nothing for a table that the MANIFEST names and that is missing", func(t *testing.T) {
		dir, _ := flushedDB(t)
		tables := tableFiles(t, dir)
		if err := os.Remove(filepath.Join(dir, tableName(tables[len(tables)/2]))); err != nil {
			t.Fatal(err)
		}
		assertOpenFails(t, dir, nil)
	})

	t.Run("Should fail and remove nothing for a table with a size that the MANIFEST does not say", func(t *testing.T) {
		dir, _ := flushedDB(t)
		path := filepath.Join(dir, tableName(tableFiles(t, dir)[0]))
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, info.Size()-1); err != nil {
			t.Fatal(err)
		}
		assertOpenFails(t, dir, manifest.ErrCorrupt)
	})

	t.Run("Should fail and remove nothing for a MANIFEST that has no valid edit", func(t *testing.T) {
		for name, content := range map[string][]byte{
			"empty file":    {},
			"garbage bytes": {1, 2, 3, 4, 5},
			"a cut header":  {0, 0, 0},
		} {
			dir, _ := flushedDB(t)
			if err := os.WriteFile(filepath.Join(dir, manifestName), content, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Log(name)
			assertOpenFails(t, dir, manifest.ErrCorrupt)
		}
	})

	t.Run("Should fail for a record with a valid checksum that is not an edit", func(t *testing.T) {
		dir, _ := flushedDB(t)
		writeManifestFile(t, dir, []byte{99, 1})
		assertOpenFails(t, dir, manifest.ErrCorrupt)
	})

	t.Run("Should fail for an edit that does not fit the state", func(t *testing.T) {
		valid := manifest.Edit{HasLogNumber: true, LogNumber: 1, HasNextFileNumber: true, NextFileNumber: 3, HasLastSeq: true, LastSeq: 1}
		for name, edit := range map[string]manifest.Edit{
			"delete of a table that does not exist": {Delete: []manifest.TableRef{{Level: 0, Number: 7}}},
			"table above level 0":                   {Add: []manifest.TableMeta{{Level: 1, Number: 7, Size: 1, Smallest: []byte("a"), Largest: []byte("b")}}},
		} {
			dir := t.TempDir()
			writeManifestFile(t, dir, valid.Encode(nil), edit.Encode(nil))
			t.Log(name)
			assertOpenFails(t, dir, manifest.ErrCorrupt)
		}
	})

	t.Run("Should fail for a log whose sequence numbers do not pass the one of the MANIFEST", func(t *testing.T) {
		dir := t.TempDir()
		edit := manifest.Edit{HasLogNumber: true, LogNumber: 1, HasNextFileNumber: true, NextFileNumber: 3, HasLastSeq: true, LastSeq: 100}
		writeManifestFile(t, dir, edit.Encode(nil))
		writeLog(t, dir, 1, batchBytes(5, "a", "1"))
		assertOpenFails(t, dir, batch.ErrCorrupt)
	})
}

func TestMigrationFromLogsOnly(t *testing.T) {
	t.Parallel()
	t.Run("Should open a directory that has only log files and make a MANIFEST and a table", func(t *testing.T) {
		dir := t.TempDir()
		writeLog(t, dir, 1, batchBytes(1, "a", "1"), batchBytes(2, "b", "2"))
		writeLog(t, dir, 2, batchBytes(3, "c", "3"))
		db := openDB(t, dir, Options{})
		defer db.Close()
		mustGet(t, db, "a", "1")
		mustGet(t, db, "b", "2")
		mustGet(t, db, "c", "3")
		if rec := db.Recovery(); rec.Logs != 2 || rec.Batches != 3 || rec.LastSeq != 3 || rec.Tables != 1 {
			t.Fatalf("Recovery = %+v", rec)
		}
		checkDirectory(t, dir, db)
		if _, err := os.Stat(filepath.Join(dir, manifestName)); err != nil {
			t.Fatal(err)
		}
		mustPut(t, db, "d", "4")
		mustGet(t, db, "d", "4")
	})
}

func TestMidManifestCorruption(t *testing.T) {
	t.Parallel()
	t.Run("Should move the tables that the MANIFEST no longer names to the lost directory and never remove them", func(t *testing.T) {
		dir, _ := flushedDB(t)
		tables := tableFiles(t, dir)
		path := filepath.Join(dir, manifestName)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		first := int(binary.LittleEndian.Uint16(data[4:6]))
		data[7+first+7+1] ^= 0xff
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}

		for round := range 3 {
			db := openDB(t, dir, smallOptions())
			rec := db.Recovery()
			if round == 0 {
				if rec.ManifestDroppedBytes == 0 || !slices.Equal(rec.KeptTables, tables) {
					t.Fatalf("Recovery = %+v, want the tables %v kept", rec, tables)
				}
			} else if rec.ManifestDroppedBytes != 0 || len(rec.KeptTables) != 0 {
				t.Fatalf("round %d: Recovery = %+v", round, rec)
			}
			for _, n := range tables {
				lost := filepath.Join(dir, lostName, tableName(n))
				if _, err := os.Stat(lost); err != nil {
					t.Fatalf("round %d: table %d is not in the lost directory: %v", round, n, err)
				}
				if _, err := os.Stat(filepath.Join(dir, tableName(n))); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("round %d: table %d is still in the directory: %v", round, n, err)
				}
			}
			checkDirectory(t, dir, db)
			mustPut(t, db, "round", string(rune('a'+round)))
			closeDB(t, db)
		}
	})

	t.Run("Should not replace a file of the lost directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, lostName), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, lostName, tableName(2)), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := moveToLost(dir, []uint64{2}); err == nil {
			t.Fatal("moveToLost replaced the file")
		}
		if err := os.WriteFile(filepath.Join(dir, tableName(2)), []byte("new"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := moveToLost(dir, []uint64{2}); err == nil {
			t.Fatal("moveToLost replaced the file")
		}
		got, _ := os.ReadFile(filepath.Join(dir, lostName, tableName(2)))
		if !bytes.Equal(got, []byte("old")) {
			t.Fatalf("the file in the lost directory is %q", got)
		}
	})

	t.Run("Should never give a number that is in the lost directory to a new file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, lostName), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, lostName, tableName(40)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		db := openDB(t, dir, Options{})
		defer db.Close()
		if next := db.nextFile.Load(); next <= 40 {
			t.Fatalf("next file number %d is not above the lost table", next)
		}
	})
}
