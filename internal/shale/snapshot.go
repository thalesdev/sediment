// SPDX-License-Identifier: MIT

package shale

import "sync/atomic"

// Snapshot is a view of the DB at one point in time. Its Get and its iterators
// see the writes that returned before the snapshot was taken, and nothing that
// came after, even if a flush or a compaction runs in between.
//
// A snapshot holds the memtables and the table files that it needs, so a
// compaction can replace those tables but the DB does not remove their files
// until the snapshot is released. Release the snapshot as soon as it is not
// needed, and before Close. It is safe for concurrent use.
type Snapshot struct {
	db       *DB
	state    *readState
	seq      uint64
	released atomic.Bool
}

// pin returns the current state, with a reference that the caller must release,
// and a sequence number that is the last one that the state can hold. It takes
// the state before the sequence number, as Get does, and then checks that the
// state did not change. A write that returned before pin started is in the
// state, so the pair sees it.
func (db *DB) pin() (*readState, uint64, error) {
	db.readMu.RLock()
	defer db.readMu.RUnlock()
	for {
		if db.closed.Load() {
			return nil, 0, ErrClosed
		}
		state := db.acquireState()
		if db.opts.iterHook != nil {
			db.opts.iterHook("state")
		}
		seq := db.lastSeq.Load()
		if db.state.Load() == state {
			return state, seq, nil
		}
		state.release()
	}
}

// Snapshot returns a snapshot of the DB. The caller must call Release.
func (db *DB) Snapshot() (*Snapshot, error) {
	state, seq, err := db.pin()
	if err != nil {
		return nil, err
	}
	return &Snapshot{db: db, state: state, seq: seq}, nil
}

// Get returns a copy of the value of key in the snapshot. It returns
// ErrNotFound if the key did not exist or was deleted at that time.
func (s *Snapshot) Get(key []byte) ([]byte, error) {
	if s.released.Load() {
		return nil, ErrReleased
	}
	if s.db.closed.Load() {
		return nil, ErrClosed
	}
	return getAt(s.state, s.seq, key)
}

// NewIterator returns an iterator over the keys of the snapshot, in ascending
// order. It has no position until the caller calls SeekToFirst or Seek. The
// iterator has its own hold on the snapshot, so Release does not stop it. The
// caller must close it.
func (s *Snapshot) NewIterator(opts *IterOptions) (Iterator, error) {
	if s.released.Load() {
		return nil, ErrReleased
	}
	if s.db.closed.Load() {
		return nil, ErrClosed
	}
	if !s.state.tryAcquire() {
		return nil, ErrReleased
	}
	return newUserIter(s.db, s.state, s.seq, opts), nil
}

// Release lets the DB remove the files that only the snapshot used. It is safe
// to call Release more than one time. Each call after the first does nothing.
func (s *Snapshot) Release() {
	if s.released.Swap(true) {
		return
	}
	s.state.release()
}
