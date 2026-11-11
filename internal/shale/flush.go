// SPDX-License-Identifier: MIT

package shale

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/thalesdev/sediment/internal/manifest"
	"github.com/thalesdev/sediment/internal/skiplist"
	"github.com/thalesdev/sediment/internal/sstable"
	"github.com/thalesdev/sediment/internal/wal"
)

// makeRoom makes sure that the memtable can take a write. If the memtable is
// larger than the limit, it freezes it, or it waits if a frozen memtable is
// still in the flush. It returns the error of the DB if the DB is closed or
// failed. The caller must hold db.mu.
func (db *DB) makeRoom() error {
	for {
		if db.closed.Load() {
			return ErrClosed
		}
		if db.err != nil {
			return db.err
		}
		state := db.state.Load()
		if state.mem.list.ApproximateSize() <= db.opts.memtableSize() {
			return nil
		}
		if state.imm != nil {
			db.cond.Wait()
			continue
		}
		if err := db.freeze(state); err != nil {
			return db.fail("memtable switch", err)
		}
	}
}

// freeze makes a new log file and a new memtable, and keeps the current memtable
// as the frozen one. It flushes the old log first, so that it is on disk before
// the writes go on in the new one. The caller must hold db.mu.
func (db *DB) freeze(state *readState) error {
	number := db.nextFile.Add(1) - 1
	path := filepath.Join(db.dir, logName(number))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("creating the log: %w", err)
	}
	abandon := func(err error) error {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := syncDir(db.dir); err != nil {
		return abandon(fmt.Errorf("syncing the directory: %w", err))
	}
	if err := db.log.Sync(); err != nil {
		return abandon(fmt.Errorf("syncing the log: %w", err))
	}
	old := db.file
	db.file, db.log = f, wal.NewWriter(f)
	state.mem.maxSeq = db.lastSeq.Load()
	db.state.Store(&readState{mem: newMemtable(number), imm: state.mem, tables: state.tables})
	db.cond.Broadcast()
	if err := old.Close(); err != nil {
		return fmt.Errorf("closing the log: %w", err)
	}
	return nil
}

// hook calls the flush hook of the options, if there is one.
func (db *DB) hook(stage string) error {
	if db.opts.flushHook == nil {
		return nil
	}
	return db.opts.flushHook(stage)
}

// flushLoop is the goroutine that writes the frozen memtables to tables. It
// leaves when the DB has an error, and when the DB is closed and there is no
// frozen memtable. After a flush that fails, it stores the error and leaves.
func (db *DB) flushLoop() {
	defer close(db.flushDone)
	db.mu.Lock()
	defer db.mu.Unlock()
	for {
		state := db.state.Load()
		for state.imm == nil && !db.closed.Load() && db.err == nil {
			db.cond.Wait()
			state = db.state.Load()
		}
		if db.err != nil || state.imm == nil {
			return
		}
		frozen, activeLog := state.imm, state.mem.wal
		db.mu.Unlock()
		tbl, err := db.flush(frozen, activeLog)
		db.mu.Lock()
		if err != nil {
			db.fail("flush", err)
			return
		}
		current := db.state.Load()
		db.state.Store(&readState{mem: current.mem, tables: append([]*table{tbl}, current.tables...)})
		db.cond.Broadcast()
		db.mu.Unlock()
		err = db.cleanup(frozen)
		db.mu.Lock()
		if err != nil {
			db.fail("flush", err)
			return
		}
	}
}

// flush writes the frozen memtable to a table, opens the table and commits it
// with an edit in the MANIFEST. activeLog is the number of the log file of the
// active memtable, which is the first log that the table does not cover.
func (db *DB) flush(frozen *memtable, activeLog uint64) (*table, error) {
	number := db.nextFile.Add(1) - 1
	meta, err := db.writeTable(frozen.list, number)
	if err != nil {
		return nil, err
	}
	if err := db.hook("table"); err != nil {
		return nil, err
	}
	tbl, err := openTable(db.dir, meta)
	if err != nil {
		return nil, err
	}
	edit := manifest.Edit{
		HasLogNumber:      true,
		LogNumber:         activeLog,
		HasNextFileNumber: true,
		NextFileNumber:    db.nextFile.Load(),
		HasLastSeq:        true,
		LastSeq:           frozen.maxSeq,
		Add:               []manifest.TableMeta{meta},
	}
	if err := db.appendEdit(edit); err != nil {
		tbl.file.Close()
		return nil, err
	}
	if err := db.hook("commit"); err != nil {
		return nil, err
	}
	return tbl, nil
}

// cleanup removes the log file of the frozen memtable, which a table covers now.
func (db *DB) cleanup(frozen *memtable) error {
	if err := db.hook("cleanup"); err != nil {
		return err
	}
	return removeFile(filepath.Join(db.dir, logName(frozen.wal)))
}

// writeTable writes the entries of list to the table file with number, flushes
// the file and the directory, and returns the description of the table. The list
// must not be empty and must not change.
func (db *DB) writeTable(list *skiplist.LockedList, number uint64) (manifest.TableMeta, error) {
	path := filepath.Join(db.dir, tableName(number))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return manifest.TableMeta{}, fmt.Errorf("shale: creating table %s: %w", tableName(number), err)
	}
	fail := func(err error) (manifest.TableMeta, error) {
		f.Close()
		return manifest.TableMeta{}, fmt.Errorf("shale: writing table %s: %w", tableName(number), err)
	}
	w := sstable.NewWriter(f, tableOptions())
	it := list.NewIterator()
	defer it.Close()
	var smallest, largest []byte
	for it.SeekToFirst(); it.Valid(); it.Next() {
		if smallest == nil {
			smallest = bytes.Clone(it.Key())
		}
		largest = append(largest[:0], it.Key()...)
		if err := w.Add(it.Key(), it.Value()); err != nil {
			return fail(err)
		}
	}
	if smallest == nil {
		return fail(fmt.Errorf("the memtable is empty"))
	}
	if err := w.Finish(); err != nil {
		return fail(err)
	}
	info, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return manifest.TableMeta{}, fmt.Errorf("shale: closing table %s: %w", tableName(number), err)
	}
	if err := syncDir(db.dir); err != nil {
		return manifest.TableMeta{}, fmt.Errorf("shale: syncing the directory: %w", err)
	}
	return manifest.TableMeta{Level: 0, Number: number, Size: uint64(info.Size()), Smallest: smallest, Largest: largest}, nil
}
