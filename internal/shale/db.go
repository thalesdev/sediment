// SPDX-License-Identifier: MIT

package shale

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"github.com/thalesdev/sediment/internal/batch"
	"github.com/thalesdev/sediment/internal/ikey"
	"github.com/thalesdev/sediment/internal/wal"
)

// DB is an open database. It is safe for concurrent use.
//
// A write goes to the write-ahead log and to the memtable. When the memtable is
// larger than Options.MemtableSize, the DB freezes it and a goroutine writes it
// to a table file in level 0, while new writes go to a new memtable. The set of
// table files is the one that the MANIFEST names.
//
// The DB does not compact the tables yet. The number of tables, and of open
// files, grows without a limit, and a read looks in each table that can hold
// the key, so Get gets slower with time. Each Open that finds writes in the
// logs also makes one table.
type DB struct {
	dir  string
	opts Options
	lock *os.File

	mu   sync.Mutex
	cond *sync.Cond
	file *os.File
	log  *wal.Writer
	err  error

	batch batch.Batch

	manifest    *os.File
	manifestLog *wal.Writer

	state    atomic.Pointer[readState]
	lastSeq  atomic.Uint64
	nextFile atomic.Uint64
	closed   atomic.Bool

	readMu    sync.RWMutex
	flushDone chan struct{}
	recovery  Recovery
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
// DB refuses all later writes.
func (db *DB) Put(key, value []byte) error {
	return db.write(ikey.KindPut, key, value)
}

// Delete deletes key. It is not an error if key does not exist. The caller can
// use key again after Delete returns.
func (db *DB) Delete(key []byte) error {
	return db.write(ikey.KindDelete, key, nil)
}

// Get returns a copy of the value of key. It returns ErrNotFound if the key
// does not exist or was deleted. It sees all the writes that returned before it
// started, and never a part of a write. It looks in the memtable, in the frozen
// memtable and in the tables, from the newest to the oldest, and the first
// version that it finds decides.
func (db *DB) Get(key []byte) ([]byte, error) {
	db.readMu.RLock()
	defer db.readMu.RUnlock()
	if db.closed.Load() {
		return nil, ErrClosed
	}
	seq := db.lastSeq.Load()
	state := db.state.Load()
	lookup := ikey.AppendLookup(nil, key, seq)
	for _, m := range []*memtable{state.mem, state.imm} {
		if m == nil {
			continue
		}
		value, deleted, found, err := lookupMemtable(m, key, lookup)
		if err != nil {
			return nil, err
		}
		if found {
			return answer(value, deleted)
		}
	}
	for _, t := range state.tables {
		value, deleted, found, err := t.lookup(key, lookup)
		if err != nil {
			return nil, err
		}
		if found {
			return answer(value, deleted)
		}
	}
	return nil, ErrNotFound
}

// answer returns a copy of value, or ErrNotFound for a delete.
func answer(value []byte, deleted bool) ([]byte, error) {
	if deleted {
		return nil, ErrNotFound
	}
	return append([]byte{}, value...), nil
}

// Close stops the writes, waits for the flush that is in progress, syncs the
// log, closes the files and unlocks the directory. It does not flush the active
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
	db.mu.Unlock()
	<-db.flushDone

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
	for _, t := range db.state.Load().tables {
		keep(t.file.Close())
	}
	db.readMu.Unlock()
	keep(unlockDir(db.lock))
	return first
}

// write does a Put or a Delete. It makes room in the memtable, writes the batch
// to the log, and then to the memtable, and then it makes the sequence number
// visible to Get.
func (db *DB) write(kind ikey.Kind, key, value []byte) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.makeRoom(); err != nil {
		return err
	}
	seq := db.lastSeq.Load() + 1
	if seq > ikey.MaxSeq {
		return errSeqExhausted
	}
	db.batch.Reset(seq)
	if kind == ikey.KindPut {
		db.batch.Put(key, value)
	} else {
		db.batch.Delete(key)
	}
	if err := db.log.AddRecord(db.batch.Bytes()); err != nil {
		return db.fail("write-ahead log", err)
	}
	if db.opts.Sync {
		if err := db.log.Sync(); err != nil {
			return db.fail("write-ahead log", err)
		}
	}
	db.state.Load().mem.list.Insert(ikey.Append(nil, key, seq, kind), value)
	db.lastSeq.Store(seq)
	return nil
}

// fail stores the error of a part of the DB, so that the DB refuses later
// writes, and returns it. It wakes the goroutines that wait. The caller must
// hold db.mu.
func (db *DB) fail(part string, err error) error {
	if db.err == nil {
		db.err = fmt.Errorf("shale: %s failed: %w", part, err)
	}
	db.cond.Broadcast()
	return db.err
}
