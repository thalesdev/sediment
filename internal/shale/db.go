// SPDX-License-Identifier: MIT

package shale

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"

	"github.com/thalesdev/sediment/internal/batch"
	"github.com/thalesdev/sediment/internal/ikey"
	"github.com/thalesdev/sediment/internal/sstable"
	"github.com/thalesdev/sediment/internal/wal"
)

// DB is an open database. It is safe for concurrent use.
//
// A write goes to the write-ahead log and to the memtable. When the memtable is
// larger than Options.MemtableSize, the DB freezes it and a goroutine writes it
// to a table file in level 0, while new writes go to a new memtable. The set of
// table files is the one that the MANIFEST names.
//
// A goroutine compacts the tables with a size-tiered rule: it joins tables that
// are next to each other by age and have a similar size into one table, and it
// drops the versions that a newer version hides. All the tables are in level 0.
// The output of a compaction is one table with no limit of size, so a large
// compaction can need as much free space as the tables that it reads. A table
// that a compaction replaced is removed when no read uses it. The one that
// drops the last reference can be a Get, and then the Get closes and removes
// the file. If many tables pile up, a write that needs to freeze the memtable
// waits for the compaction. A crash during a compaction can leave table files
// that the MANIFEST does not name. The next Open removes them, or moves them to
// the lost directory if the MANIFEST had damage.
//
// Each Open that finds writes in the logs also makes one table.
type DB struct {
	dir   string
	opts  Options
	lg    *slog.Logger
	lock  *os.File
	cache *sstable.BlockCache

	stallFlush      stallStat
	stallCompaction stallStat
	freezes         durStat
	flushes         atomic.Uint64
	flushNanos      atomic.Int64
	compactNanos    atomic.Int64

	mu        sync.Mutex
	cond      *sync.Cond
	writeCond *sync.Cond
	queue     []*writer
	file      *os.File
	log       *wal.Writer
	err       error

	batch  batch.Batch
	keyBuf []byte

	manifestMu  sync.Mutex
	manifest    *os.File
	manifestLog *wal.Writer

	state    atomic.Pointer[readState]
	lastSeq  atomic.Uint64
	nextFile atomic.Uint64
	closed   atomic.Bool

	readMu      sync.RWMutex
	flushDone   chan struct{}
	compactDone chan struct{}
	compacting  atomic.Bool
	recovery    Recovery

	compactions    atomic.Uint64
	byCount        atomic.Uint64
	groups         atomic.Uint64
	grouped        atomic.Uint64
	compactRead    atomic.Uint64
	compactWritten atomic.Uint64
	flushBytes     atomic.Uint64
}

// Recovery tells what Open found in the directory.
type Recovery struct {
	// Logs is the number of log files that Open read.
	Logs int

	// Batches is the number of write batches that Open applied.
	Batches int

	// LastSeq is the largest sequence number that Open recovered.
	LastSeq uint64

	// DroppedBytes is the number of bytes that Open skipped in the log files
	// because they were cut or not valid. Open treats the first such bytes of a
	// log file as the end of that file. After a crash, the last log file can have
	// such bytes. If an older log file has them, the writes that follow in that
	// file are lost, but the writes in the newer files are still applied.
	DroppedBytes int

	// Tables is the number of table files that the DB has after Open.
	Tables int

	// ManifestDroppedBytes is the number of bytes that Open skipped at the end of
	// the MANIFEST because they were cut or not valid. A crash during a flush can
	// cut the last edit. Other damage can hide the edits that follow it.
	ManifestDroppedBytes int

	// KeptTables lists the numbers of the table files that Open found and that
	// the MANIFEST does not name, when ManifestDroppedBytes is not 0. Open moved
	// them to the lost directory, and it never removes files from there.
	KeptTables []uint64
}

// Recovery returns what Open found in the directory.
func (db *DB) Recovery() Recovery {
	return db.recovery
}

// Put sets the value of key. The caller can use key and value again after Put
// returns. If Put returns an error, the write can be in the log or not, and the
// DB refuses all later writes. Writes that are at the same time can share one
// record of the log and, if Options.Sync is true, one flush.
func (db *DB) Put(key, value []byte) error {
	return db.write(batch.KindPut, key, value)
}

// Delete deletes key. It is not an error if key does not exist. The caller can
// use key again after Delete returns.
func (db *DB) Delete(key []byte) error {
	return db.write(batch.KindDelete, key, nil)
}

// DeleteRange deletes every key from start up to, and not including, end. It is
// one write, so it is all or nothing after a crash, and a Get never sees a part
// of it. It does not read or write each key, so its cost does not depend on how
// many keys it covers, and the space of the keys is freed by the compactions. A
// range that is empty or inverted deletes nothing. The caller can use start and
// end again after DeleteRange returns.
func (db *DB) DeleteRange(start, end []byte) error {
	if bytes.Compare(start, end) >= 0 {
		return nil
	}
	return db.write(batch.KindRangeDelete, start, end)
}

// Get returns a copy of the value of key. It returns ErrNotFound if the key
// does not exist or was deleted. It sees all the writes that returned before it
// started, and never a part of a write. It looks in the memtable, in the frozen
// memtable and in the tables, from the newest to the oldest, and the first
// version that it finds decides.
//
// Get takes the state before it reads the last sequence number. A compaction can
// drop an old version of a key if a newer one is in the tables. Every entry in
// the tables has a sequence number that is not above the last one, so a last
// sequence number that is read after the state always sees the newest version
// that the state holds. The other order can miss a version that was dropped.
func (db *DB) Get(key []byte) ([]byte, error) {
	db.readMu.RLock()
	defer db.readMu.RUnlock()
	if db.closed.Load() {
		return nil, ErrClosed
	}
	state := db.acquireState()
	defer state.release()
	if db.opts.getHook != nil {
		db.opts.getHook("state")
	}
	return getAt(state, db.lastSeq.Load(), key)
}

// getAt returns a copy of the value of key as the state and the sequence number
// see it.
func getAt(state *readState, seq uint64, key []byte) ([]byte, error) {
	lookup := ikey.AppendLookup(nil, key, seq)
	for _, m := range []*memtable{state.mem, state.imm} {
		if m == nil {
			continue
		}
		value, found, err := decide(key, seq, m.cover(), func() ([]byte, uint64, bool, bool, error) {
			return lookupMemtable(m, key, lookup)
		})
		if err != nil || found != foundNone {
			return answerOf(value, found, err)
		}
	}
	for _, t := range state.tables {
		value, found, err := decide(key, seq, t.tombIdx, func() ([]byte, uint64, bool, bool, error) {
			return t.lookup(key, lookup)
		})
		if err != nil || found != foundNone {
			return answerOf(value, found, err)
		}
	}
	return nil, ErrNotFound
}

// found says what a source of a read knows about a key.
type found int

const (
	// foundNone means that the source has nothing that decides the read, and
	// the read goes on to the next one.
	foundNone found = iota

	// foundValue means that the source has the newest version, a put.
	foundValue

	// foundGone means that the key is deleted, by a delete or by a range delete.
	foundGone
)

// decide asks one source, through lookup, for the newest version of key, and
// combines the answer with the range deletes of the source. A range delete with a
// sequence number above the one of the version hides it, and a range delete alone
// hides every version in the older sources, because they have smaller sequence
// numbers. cover holds the range deletes of the source, and snap is the sequence
// number of the read.
func decide(key []byte, snap uint64, cover *tombIndex, lookup func() ([]byte, uint64, bool, bool, error)) ([]byte, found, error) {
	value, seq, deleted, ok, err := lookup()
	if err != nil {
		return nil, foundNone, err
	}
	covered := cover.maxCover(key, snap)
	switch {
	case ok && !deleted && covered < seq:
		return value, foundValue, nil
	case ok || covered > 0:
		return nil, foundGone, nil
	}
	return nil, foundNone, nil
}

// answerOf turns the result of decide into the result of a read.
func answerOf(value []byte, f found, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	if f == foundValue {
		return bytes.Clone(value), nil
	}
	return nil, ErrNotFound
}

// acquireState returns the current state with a reference that the caller must
// release.
func (db *DB) acquireState() *readState {
	for {
		if s := db.state.Load(); s.tryAcquire() {
			return s
		}
	}
}

// setState makes s the current state, and takes a reference on each of its
// tables. It returns the state that it replaced, which the caller must release,
// after it unlocks db.mu if the release can close files.
func (db *DB) setState(s *readState) *readState {
	for _, t := range s.tables {
		t.refs.Add(1)
	}
	return db.state.Swap(s)
}

// Close stops the writes, waits for the group of writes that is in progress and
// for the flush and the compaction that are in progress, syncs the log, closes
// the files and unlocks the directory. A write that waits in the queue gets
// ErrClosed. It does not flush the active
// memtable, because the log holds it and the next Open reads it. After Close,
// all methods, also Close, return ErrClosed. It returns the first error that it
// meets, but it always does all its steps.
func (db *DB) Close() error {
	db.mu.Lock()
	if db.closed.Load() {
		db.mu.Unlock()
		return ErrClosed
	}
	db.closed.Store(true)
	db.cond.Broadcast()
	db.writeCond.Broadcast()
	for len(db.queue) > 0 {
		db.writeCond.Wait()
	}
	db.mu.Unlock()
	<-db.flushDone
	<-db.compactDone

	db.mu.Lock()
	defer db.mu.Unlock()
	var first error
	keep := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	if db.err == nil {
		keep(db.log.Sync())
	}
	keep(db.file.Close())
	keep(db.manifest.Close())
	db.readMu.Lock()
	db.state.Load().release()
	db.readMu.Unlock()
	keep(unlockDir(db.lock))
	return first
}

// fail stores the error of a part of the DB, so that the DB refuses later
// writes, and returns it. It wakes the goroutines that wait. The caller must
// hold db.mu.
func (db *DB) fail(part string, err error) error {
	if db.err == nil {
		db.err = fmt.Errorf("shale: %s failed: %w", part, err)
		db.lg.Error("the DB failed and refuses writes", "part", part, "err", err)
	}
	db.cond.Broadcast()
	return db.err
}

// Err returns the error that stopped the DB, or nil if the DB works. After an error
// the DB refuses every write. It is the signal for a health check.
func (db *DB) Err() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.err
}
