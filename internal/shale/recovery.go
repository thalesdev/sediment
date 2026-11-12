// SPDX-License-Identifier: MIT

package shale

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/thalesdev/sediment/internal/batch"
	"github.com/thalesdev/sediment/internal/ikey"
	"github.com/thalesdev/sediment/internal/manifest"
	"github.com/thalesdev/sediment/internal/wal"
)

// Open opens the database in dir. It creates dir if it does not exist, locks it,
// and returns an error that wraps ErrLocked if another DB has dir open.
//
// Open reads the MANIFEST and opens the tables that it names. It reads the log
// files that the tables do not cover into a memtable, in order of their number,
// and writes that memtable to a new table. Then it starts a new log file and a
// new MANIFEST, and it removes the files that it does not need. After Open, the
// directory has one log file.
//
// A cut or damaged end of a log file or of the MANIFEST is not an error. Open
// skips it, and Recovery reports the number of bytes that it skipped. If the
// MANIFEST had bytes that it skipped, Open does not remove the table files that
// the MANIFEST does not name. It moves them to the lost directory, and it never
// removes files from there. An error is the result for a MANIFEST with no valid
// edit, for a table that the MANIFEST names and that is missing or does not
// open, for a record with a valid checksum that is not valid, and for a
// sequence number that does not follow the one before it.
func Open(dir string, opts Options) (*DB, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("shale: creating the directory: %w", err)
	}
	lock, err := lockDir(dir)
	if err != nil {
		return nil, err
	}
	db := &DB{
		dir:         dir,
		opts:        opts,
		lock:        lock,
		flushDone:   make(chan struct{}),
		compactDone: make(chan struct{}),
	}
	db.cond = sync.NewCond(&db.mu)
	db.writeCond = sync.NewCond(&db.mu)
	if err := db.open(); err != nil {
		unlockDir(lock)
		return nil, err
	}
	go db.flushLoop()
	go db.compactLoop()
	return db, nil
}

// open does the steps of Open after the lock. If it fails, it closes the files
// that it opened.
func (db *DB) open() (err error) {
	var tables []*table
	defer func() {
		if err == nil {
			return
		}
		for _, t := range tables {
			t.file.Close()
		}
		if db.file != nil {
			db.file.Close()
		}
		if db.manifest != nil {
			db.manifest.Close()
		}
	}()

	version, manifestDropped, err := readManifest(db.dir)
	if err != nil {
		return err
	}
	files, err := listDir(db.dir)
	if err != nil {
		return err
	}
	for level := 1; level < len(version.Tables); level++ {
		if len(version.Tables[level]) > 0 {
			return fmt.Errorf("shale: the MANIFEST names tables in level %d, which this version does not support: %w", level, manifest.ErrCorrupt)
		}
	}
	for _, meta := range version.Tables[0] {
		t, err := openTable(db.dir, meta)
		if err != nil {
			return err
		}
		tables = append(tables, t)
	}
	for i, t := range tables {
		if t.meta.HasSeq {
			continue
		}
		if err := fillSeqRange(t); err != nil {
			return err
		}
		version.Tables[0][i] = t.meta
	}
	if err := checkSeqRanges(version.Tables[0], version.LastSeq); err != nil {
		return err
	}

	replayed := newMemtable(0)
	last, entries, err := db.replay(replayed, files.logs, version)
	if err != nil {
		return err
	}
	db.nextFile.Store(max(version.NextFileNumber, files.max+1, 1))

	if entries > 0 {
		number := db.nextFile.Add(1) - 1
		meta, err := db.writeTable(replayed.list, number)
		if err != nil {
			return err
		}
		t, err := openTable(db.dir, meta)
		if err != nil {
			return err
		}
		tables = append(tables, t)
		if err := version.Apply(manifest.Edit{Add: []manifest.TableMeta{meta}}); err != nil {
			return err
		}
	}

	logNumber := db.nextFile.Add(1) - 1
	if err := db.startLog(logNumber); err != nil {
		return err
	}
	version.LogNumber = logNumber
	version.NextFileNumber = db.nextFile.Load()
	version.LastSeq = max(version.LastSeq, last)
	if err := checkSeqRanges(version.Tables[0], version.LastSeq); err != nil {
		return err
	}
	if err := db.writeManifest(version); err != nil {
		return err
	}
	kept, err := db.collect(files, version, manifestDropped)
	if err != nil {
		return err
	}

	slices.SortFunc(tables, func(a, b *table) int {
		switch {
		case a.meta.LargestSeq > b.meta.LargestSeq:
			return -1
		case a.meta.LargestSeq < b.meta.LargestSeq:
			return 1
		}
		return 0
	})
	db.setState(newReadState(newMemtable(logNumber), nil, tables))
	db.lastSeq.Store(version.LastSeq)
	db.recovery.LastSeq = version.LastSeq
	db.recovery.Tables = len(tables)
	db.recovery.ManifestDroppedBytes = manifestDropped
	db.recovery.KeptTables = kept
	return nil
}

// replay reads the log files with a number that the version does not cover into
// mem. It returns the last sequence number and the number of entries that it
// applied. The first sequence number must be larger than the one of the version.
func (db *DB) replay(mem *memtable, logs []uint64, version manifest.Version) (last uint64, entries int, err error) {
	last = version.LastSeq
	for _, n := range logs {
		if n < version.LogNumber {
			continue
		}
		var count int
		if last, count, err = db.replayLog(mem, n, last); err != nil {
			return 0, 0, err
		}
		entries += count
	}
	return last, entries, nil
}

// replayLog applies the batches of log file n to mem and returns the last
// sequence number and the number of entries. last is the last sequence number
// before the file. A batch must have a sequence number after the one before it.
func (db *DB) replayLog(mem *memtable, n, last uint64) (uint64, int, error) {
	name := logName(n)
	f, err := os.Open(filepath.Join(db.dir, name))
	if err != nil {
		return 0, 0, fmt.Errorf("shale: opening %s: %w", name, err)
	}
	defer f.Close()
	entries := 0
	r := wal.NewReader(f)
	for {
		record, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, 0, fmt.Errorf("shale: reading %s: %w", name, err)
		}
		view, err := batch.Parse(record)
		if err != nil {
			return 0, 0, fmt.Errorf("shale: %s: %w", name, err)
		}
		if view.Seq() <= last {
			return 0, 0, fmt.Errorf("shale: %s: batch with sequence number %d does not follow %d: %w", name, view.Seq(), last, batch.ErrCorrupt)
		}
		view.ForEach(func(seq uint64, kind ikey.Kind, key, value []byte) {
			mem.list.Insert(ikey.Append(nil, key, seq, kind), value)
		})
		entries += view.Count()
		last = view.Seq() + uint64(view.Count()) - 1
		db.recovery.Batches++
	}
	db.recovery.Logs++
	db.recovery.DroppedBytes += r.Dropped()
	return last, entries, nil
}

// startLog creates the log file with number n for the next writes and flushes
// the directory, so that the name of the file survives a loss of power.
func (db *DB) startLog(n uint64) error {
	f, err := os.OpenFile(filepath.Join(db.dir, logName(n)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("shale: creating the log: %w", err)
	}
	if err := syncDir(db.dir); err != nil {
		f.Close()
		return fmt.Errorf("shale: syncing the directory: %w", err)
	}
	db.file = f
	db.log = wal.NewWriter(syncFile{f})
	return nil
}

// collect removes the files that the new MANIFEST does not need: the log files
// with a number below the log number, and the temporary files. A table file that
// the version does not name is removed, unless the MANIFEST had bytes that Open
// skipped. Then it moves the file to the lost directory. It returns the numbers
// of the files that it moved.
func (db *DB) collect(files listing, version manifest.Version, manifestDropped int) ([]uint64, error) {
	for _, n := range files.logs {
		if n < version.LogNumber {
			if err := removeFile(filepath.Join(db.dir, logName(n))); err != nil {
				return nil, err
			}
		}
	}
	for _, name := range files.tmps {
		if err := removeFile(filepath.Join(db.dir, name)); err != nil {
			return nil, err
		}
	}
	named := map[uint64]bool{}
	for _, level := range version.Tables {
		for _, t := range level {
			named[t.Number] = true
		}
	}
	var orphans []uint64
	for _, n := range files.tables {
		if !named[n] {
			orphans = append(orphans, n)
		}
	}
	if manifestDropped > 0 {
		return orphans, moveToLost(db.dir, orphans)
	}
	for _, n := range orphans {
		if err := removeFile(filepath.Join(db.dir, tableName(n))); err != nil {
			return nil, err
		}
	}
	return nil, syncDir(db.dir)
}

// fillSeqRange reads all the entries of t and sets the range of sequence numbers
// in its description. It is for a table that the MANIFEST names with no range.
func fillSeqRange(t *table) error {
	it := t.reader.NewIterator()
	defer it.Close()
	smallest, largest := uint64(ikey.MaxSeq), uint64(0)
	entries := 0
	for it.SeekToFirst(); it.Valid(); it.Next() {
		_, seq, _, err := ikey.Parse(it.Key())
		if err != nil {
			return fmt.Errorf("shale: table %s: %w", tableName(t.meta.Number), err)
		}
		smallest, largest = min(smallest, seq), max(largest, seq)
		entries++
	}
	if err := it.Err(); err != nil {
		return fmt.Errorf("shale: table %s: %w", tableName(t.meta.Number), err)
	}
	if entries == 0 {
		return fmt.Errorf("shale: table %s has no entry: %w", tableName(t.meta.Number), manifest.ErrCorrupt)
	}
	t.meta.HasSeq, t.meta.SmallestSeq, t.meta.LargestSeq = true, smallest, largest
	return nil
}

// checkSeqRanges checks that the ranges of sequence numbers of the tables do not
// overlap, and that none goes past lastSeq. A table with no range is an error.
// This order of the ranges is what lets a read order the tables by age.
func checkSeqRanges(metas []manifest.TableMeta, lastSeq uint64) error {
	sorted := slices.Clone(metas)
	slices.SortFunc(sorted, func(a, b manifest.TableMeta) int {
		switch {
		case a.LargestSeq > b.LargestSeq:
			return -1
		case a.LargestSeq < b.LargestSeq:
			return 1
		}
		return 0
	})
	for i, m := range sorted {
		if !m.HasSeq {
			return fmt.Errorf("shale: table %s has no sequence range: %w", tableName(m.Number), manifest.ErrCorrupt)
		}
		if m.LargestSeq > lastSeq {
			return fmt.Errorf("shale: table %s has sequence number %d, above the last one, %d: %w", tableName(m.Number), m.LargestSeq, lastSeq, manifest.ErrCorrupt)
		}
		if i > 0 && sorted[i-1].SmallestSeq <= m.LargestSeq {
			return fmt.Errorf("shale: tables %s and %s have sequence ranges that overlap: %w", tableName(sorted[i-1].Number), tableName(m.Number), manifest.ErrCorrupt)
		}
	}
	return nil
}

// syncDir flushes the directory dir to stable storage.
func syncDir(dir string) error {
	if skipSync.Load() {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
