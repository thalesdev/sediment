// SPDX-License-Identifier: MIT

package shale

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/thalesdev/sediment/internal/ikey"
	"github.com/thalesdev/sediment/internal/manifest"
	"github.com/thalesdev/sediment/internal/merge"
	"github.com/thalesdev/sediment/internal/sstable"
)

// dropIter iterates over the entries of a source that a compaction keeps. It
// keeps the first entry of each user key, which is the newest one because of the
// order of ikey.Compare, and it drops the other entries of the key. If
// dropDeletes is true, it also drops a first entry that is a delete. Nothing
// reads an old version of a key yet, so a compaction does not keep one. A source
// must be sorted with ikey.Compare, and an entry with a key that is not valid is
// an error.
type dropIter struct {
	src         merge.Source
	dropDeletes bool
	last        []byte
	haveLast    bool
	err         error
}

// newDropIter returns an iterator over src that applies the rules of a
// compaction.
func newDropIter(src merge.Source, dropDeletes bool) *dropIter {
	return &dropIter{src: src, dropDeletes: dropDeletes}
}

// SeekToFirst moves the iterator to the first entry that the compaction keeps.
func (d *dropIter) SeekToFirst() {
	d.src.SeekToFirst()
	d.haveLast = false
	d.skip()
}

// Next moves the iterator to the next entry that the compaction keeps.
func (d *dropIter) Next() {
	d.src.Next()
	d.skip()
}

// Valid returns true if the iterator is at an entry.
func (d *dropIter) Valid() bool {
	return d.err == nil && d.src.Valid()
}

// Key returns the key of the current entry.
func (d *dropIter) Key() []byte { return d.src.Key() }

// Value returns the value of the current entry.
func (d *dropIter) Value() []byte { return d.src.Value() }

// Err returns the error that stopped the iteration, or nil.
func (d *dropIter) Err() error {
	if d.err != nil {
		return d.err
	}
	return d.src.Err()
}

// skip moves the source forward to the next entry that the compaction keeps.
func (d *dropIter) skip() {
	for d.src.Valid() {
		user, _, kind, err := ikey.Parse(d.src.Key())
		if err != nil {
			d.err = err
			return
		}
		if d.haveLast && bytes.Equal(user, d.last) {
			d.src.Next()
			continue
		}
		d.last = append(d.last[:0], user...)
		d.haveLast = true
		if kind == ikey.KindDelete && d.dropDeletes {
			d.src.Next()
			continue
		}
		return
	}
}

// compactStage calls the compaction hook of the options, if there is one.
func (db *DB) compactStage(stage string) error {
	if db.opts.compactHook == nil {
		return nil
	}
	return db.opts.compactHook(stage)
}

// compactLoop is the goroutine that compacts the tables, one compaction at a
// time. It asks the picker for a plan each time that the state changes. It leaves
// when the DB is closed, when the DB has an error, and after a compaction that
// fails. It never starts a compaction after the DB is closed.
func (db *DB) compactLoop() {
	defer close(db.compactDone)
	db.mu.Lock()
	defer db.mu.Unlock()
	for {
		var inputs []*table
		var chosen plan
		for {
			if db.closed.Load() || db.err != nil {
				db.compacting.Store(false)
				return
			}
			state := db.state.Load()
			if !db.opts.disableCompaction {
				if p, ok := pickCompaction(tableInfos(state.tables)); ok {
					chosen = p
					inputs = slices.Clone(state.tables[p.start:p.end])
					break
				}
			}
			db.compacting.Store(false)
			db.cond.Wait()
		}
		db.compacting.Store(true)
		db.mu.Unlock()
		output, err := db.compact(inputs, chosen.includesOldest)
		db.mu.Lock()
		if err != nil {
			db.compacting.Store(false)
			db.fail("compaction", err)
			return
		}
		old, err := db.installCompaction(inputs, output)
		if err != nil {
			db.compacting.Store(false)
			db.fail("compaction", err)
			return
		}
		db.compactions.Add(1)
		if chosen.byCount {
			db.byCount.Add(1)
		}
		db.cond.Broadcast()
		db.mu.Unlock()
		old.release()
		err = db.compactStage("done")
		db.mu.Lock()
		if err != nil {
			db.compacting.Store(false)
			db.fail("compaction", err)
			return
		}
	}
}

// compact joins the input tables into one table and commits the change in the
// MANIFEST. It returns the new table, or nil if the compaction dropped all the
// entries. includesOldest tells if the inputs include the oldest table, so that
// the deletes can go. The output has the range of sequence numbers of all the
// inputs.
func (db *DB) compact(inputs []*table, includesOldest bool) (*table, error) {
	if err := db.compactStage("start"); err != nil {
		return nil, err
	}
	sources := make([]merge.Source, len(inputs))
	smallestSeq, largestSeq := uint64(ikey.MaxSeq), uint64(0)
	var read uint64
	for i, t := range inputs {
		sources[i] = t.reader.NewIterator()
		read += t.meta.Size
		smallestSeq, largestSeq = min(smallestSeq, t.meta.SmallestSeq), max(largestSeq, t.meta.LargestSeq)
	}
	merged := merge.New(ikey.Compare, sources...)
	defer merged.Close()

	number := db.nextFile.Add(1) - 1
	meta, err := db.writeCompacted(number, newDropIter(merged, includesOldest), smallestSeq, largestSeq)
	if err != nil {
		return nil, err
	}
	if err := db.compactStage("output"); err != nil {
		return nil, err
	}

	var output *table
	edit := manifest.Edit{}
	for _, t := range inputs {
		edit.Delete = append(edit.Delete, manifest.TableRef{Level: 0, Number: t.meta.Number})
	}
	if meta != nil {
		if output, err = openTable(db.dir, *meta); err != nil {
			return nil, err
		}
		edit.Add = []manifest.TableMeta{*meta}
	}
	if err := db.appendEdit(edit); err != nil {
		if output != nil {
			output.file.Close()
		}
		return nil, err
	}
	if err := db.compactStage("commit"); err != nil {
		return nil, err
	}
	db.compactRead.Add(read)
	if meta != nil {
		db.compactWritten.Add(meta.Size)
	}
	return output, nil
}

// writeCompacted writes the entries of it to the table file with number, flushes
// the file and the directory, and returns the description of the table. It
// returns nil, and writes no file, if it has no entry.
func (db *DB) writeCompacted(number uint64, it *dropIter, smallestSeq, largestSeq uint64) (*manifest.TableMeta, error) {
	it.SeekToFirst()
	if !it.Valid() {
		if err := it.Err(); err != nil {
			return nil, fmt.Errorf("shale: compacting: %w", err)
		}
		return nil, nil
	}
	path := filepath.Join(db.dir, tableName(number))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("shale: creating table %s: %w", tableName(number), err)
	}
	fail := func(err error) (*manifest.TableMeta, error) {
		f.Close()
		os.Remove(path)
		return nil, fmt.Errorf("shale: writing table %s: %w", tableName(number), err)
	}
	w := sstable.NewWriter(syncFile{f}, tableOptions())
	var smallest, largest []byte
	for ; it.Valid(); it.Next() {
		if smallest == nil {
			smallest = bytes.Clone(it.Key())
		}
		largest = append(largest[:0], it.Key()...)
		if err := w.Add(it.Key(), it.Value()); err != nil {
			return fail(err)
		}
	}
	if err := it.Err(); err != nil {
		return fail(err)
	}
	if err := w.Finish(); err != nil {
		return fail(err)
	}
	info, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("shale: closing table %s: %w", tableName(number), err)
	}
	if err := syncDir(db.dir); err != nil {
		return nil, fmt.Errorf("shale: syncing the directory: %w", err)
	}
	return &manifest.TableMeta{
		Level: 0, Number: number, Size: uint64(info.Size()), Smallest: smallest, Largest: largest,
		HasSeq: true, SmallestSeq: smallestSeq, LargestSeq: largestSeq,
	}, nil
}

// installCompaction replaces the input tables with the output in the state. The
// output takes the place of the first input. It marks the inputs as obsolete, so
// that the last read that uses one of them removes its file. It returns the
// state that it replaced, which the caller must release without holding db.mu.
// The caller must hold db.mu.
func (db *DB) installCompaction(inputs []*table, output *table) (*readState, error) {
	current := db.state.Load()
	dropped := make(map[*table]bool, len(inputs))
	for _, t := range inputs {
		dropped[t] = true
	}
	tables := make([]*table, 0, len(current.tables)+1)
	found := 0
	for _, t := range current.tables {
		if !dropped[t] {
			tables = append(tables, t)
			continue
		}
		found++
		if found == 1 && output != nil {
			tables = append(tables, output)
		}
	}
	if found != len(inputs) {
		return nil, fmt.Errorf("the tables of the compaction are not in the state")
	}
	for _, t := range inputs {
		t.obsolete.Store(true)
	}
	return db.setState(newReadState(current.mem, current.imm, tables)), nil
}
