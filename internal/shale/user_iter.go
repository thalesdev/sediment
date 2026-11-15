// SPDX-License-Identifier: MIT

package shale

import (
	"bytes"
	"slices"

	"github.com/thalesdev/sediment/internal/ikey"
	"github.com/thalesdev/sediment/internal/merge"
)

// IterOptions sets the range of an iterator. A nil bound is no bound.
type IterOptions struct {
	// Lower is the smallest key that the iterator returns.
	Lower []byte

	// Upper is the first key that the iterator does not return.
	Upper []byte
}

// userIter iterates over the user keys of a state, as of one sequence number. For
// each key it returns the newest version that is not above the sequence number,
// and it hides a key whose newest version is a delete. It holds one reference on
// the state until Close.
type userIter struct {
	db     *DB
	state  *readState
	seq    uint64
	lower  []byte
	upper  []byte
	src    *merge.Iterator
	valid  bool
	closed bool
	err    error
	lookup []byte
	last   []byte
}

// NewIterator returns an iterator over the keys of the DB, in ascending order,
// as of the moment of the call. It has no position until the caller calls
// SeekToFirst or Seek. Key and Value are valid until the next move. The caller
// must close the iterator, and must do so before Close of the DB. An iterator
// holds the table files that it reads, so a long one keeps disk space that a
// compaction freed.
func (db *DB) NewIterator(opts *IterOptions) (Iterator, error) {
	state, seq, err := db.pin()
	if err != nil {
		return nil, err
	}
	return newUserIter(db, state, seq, opts), nil
}

// newUserIter returns an iterator that takes over the reference of the caller on
// state.
func newUserIter(db *DB, state *readState, seq uint64, opts *IterOptions) *userIter {
	it := &userIter{db: db, state: state, seq: seq}
	if opts != nil {
		it.lower, it.upper = slices.Clone(opts.Lower), slices.Clone(opts.Upper)
	}
	var sources []merge.Source
	for _, m := range []*memtable{state.mem, state.imm} {
		if m != nil {
			sources = append(sources, m.list.NewIterator())
		}
	}
	for _, t := range state.tables {
		if it.outside(ikey.UserKey(t.meta.Smallest), ikey.UserKey(t.meta.Largest)) {
			continue
		}
		sources = append(sources, t.reader.NewIterator())
	}
	it.src = merge.New(ikey.Compare, sources...)
	return it
}

// outside tells if no key between smallest and largest can be in the range.
func (it *userIter) outside(smallest, largest []byte) bool {
	if it.upper != nil && bytes.Compare(smallest, it.upper) >= 0 {
		return true
	}
	return it.lower != nil && bytes.Compare(largest, it.lower) < 0
}

// usable tells if the iterator can move. It sets the error if the DB is closed.
func (it *userIter) usable() bool {
	if it.closed {
		return false
	}
	if it.err != nil {
		return false
	}
	if it.db.closed.Load() {
		it.err = ErrClosed
		it.valid = false
		return false
	}
	return true
}

// SeekToFirst moves the iterator to the first key of the range.
func (it *userIter) SeekToFirst() {
	if !it.usable() {
		return
	}
	if it.lower == nil {
		it.src.SeekToFirst()
	} else {
		it.seekUser(it.lower)
	}
	it.settle()
}

// Seek moves the iterator to the first key that is not below key and is in the
// range.
func (it *userIter) Seek(key []byte) {
	if !it.usable() {
		return
	}
	if it.lower != nil && bytes.Compare(key, it.lower) < 0 {
		key = it.lower
	}
	it.seekUser(key)
	it.settle()
}

// seekUser moves the source to the newest version of user that the sequence
// number sees, or to the next key.
func (it *userIter) seekUser(user []byte) {
	it.lookup = ikey.AppendLookup(it.lookup[:0], user, it.seq)
	it.src.Seek(it.lookup)
}

// Next moves the iterator to the next key.
func (it *userIter) Next() {
	if !it.usable() || !it.valid {
		return
	}
	it.skipCurrentUser()
	it.settle()
}

// skipCurrentUser moves the source past every version of the key that it is at.
func (it *userIter) skipCurrentUser() {
	it.last = append(it.last[:0], ikey.UserKey(it.src.Key())...)
	it.skipUser()
}

// skipUser moves the source past every version of the key in it.last.
func (it *userIter) skipUser() {
	for it.src.Valid() && bytes.Equal(ikey.UserKey(it.src.Key()), it.last) {
		it.src.Next()
	}
}

// settle moves the source forward to the newest visible version of a key that is
// a put and is in the range, and sets valid.
func (it *userIter) settle() {
	it.valid = false
	for it.src.Valid() {
		user, seq, kind, err := ikey.Parse(it.src.Key())
		if err != nil {
			it.err = err
			return
		}
		if it.upper != nil && bytes.Compare(user, it.upper) >= 0 {
			return
		}
		if seq > it.seq {
			it.src.Next()
			continue
		}
		if kind == ikey.KindDelete {
			it.last = append(it.last[:0], user...)
			it.skipUser()
			continue
		}
		it.valid = true
		return
	}
	if err := it.src.Err(); err != nil {
		it.err = err
	}
}

// Valid returns true if the iterator is at a key.
func (it *userIter) Valid() bool { return !it.closed && it.err == nil && it.valid }

// Key returns the key of the current pair. It is valid until the next move.
func (it *userIter) Key() []byte { return ikey.UserKey(it.src.Key()) }

// Value returns the value of the current pair. It is valid until the next move.
func (it *userIter) Value() []byte { return it.src.Value() }

// Err returns the error that stopped the iteration, or nil.
func (it *userIter) Err() error { return it.err }

// Close releases the iterator and its hold on the state. It is safe to call
// Close more than one time.
func (it *userIter) Close() error {
	if it.closed {
		return nil
	}
	it.closed = true
	it.valid = false
	err := it.src.Close()
	it.state.release()
	return err
}
