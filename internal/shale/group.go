// SPDX-License-Identifier: MIT

package shale

import (
	"sync"

	"github.com/thalesdev/sediment/internal/ikey"
)

const (
	// maxGroupBytes is the largest size of the writes that one group can hold.
	maxGroupBytes = 1 << 20

	// smallWriteBytes is the size up to which a first write is small. A group
	// that starts with a small write grows by at most smallGroupGrowth bytes, so
	// that a small write does not wait for a long group.
	smallWriteBytes = 128 << 10

	// smallGroupGrowth is how many bytes a group that starts with a small write
	// can add to the size of that write.
	smallGroupGrowth = 128 << 10

	// writeOverhead is the number of bytes that the picker of a group counts for
	// each write, besides its key and value.
	writeOverhead = 16
)

// writer is a Put or a Delete that waits in the queue of the DB.
type writer struct {
	kind  ikey.Kind
	key   []byte
	value []byte
	done  bool
	err   error
}

// size returns the bytes that the write counts in a group.
func (w *writer) size() int {
	return len(w.key) + len(w.value) + writeOverhead
}

var writerPool = sync.Pool{New: func() any { return new(writer) }}

// write does a Put or a Delete. The write goes into a queue. The first write in
// the queue is the leader. It takes the writes that wait behind it, up to a limit
// of size, as one group. It writes the group to the log as one record, flushes
// the log once if Options.Sync is true, adds the group to the memtable, and makes
// the last sequence number visible to Get. Then it wakes the writes of the group
// with the result. A write that is not the leader waits until the leader of its
// group wakes it, or until it becomes the leader.
func (db *DB) write(kind ikey.Kind, key, value []byte) error {
	w := writerPool.Get().(*writer)
	*w = writer{kind: kind, key: key, value: value}
	defer func() {
		*w = writer{}
		writerPool.Put(w)
	}()

	db.mu.Lock()
	db.queue = append(db.queue, w)
	for !w.done && db.queue[0] != w {
		db.writeCond.Wait()
	}
	if w.done {
		db.mu.Unlock()
		return w.err
	}
	err := db.lead(w)
	db.mu.Unlock()
	return err
}

// lead commits the group that starts with the first write of the queue, which is
// w. It holds db.mu when it is called and when it returns, and it releases db.mu
// while it writes the log, so that other writes can join the queue. If the DB
// cannot take writes, it fails only w, and the next write in the queue leads.
func (db *DB) lead(w *writer) error {
	if err := db.makeRoom(); err != nil {
		db.finish(1, err)
		return err
	}
	group := db.pick()
	first := db.lastSeq.Load() + 1
	last := first + uint64(len(group)) - 1
	if last > ikey.MaxSeq {
		db.finish(len(group), errSeqExhausted)
		return errSeqExhausted
	}
	db.batch.Reset(first)
	for _, g := range group {
		if g.kind == ikey.KindPut {
			db.batch.Put(g.key, g.value)
		} else {
			db.batch.Delete(g.key)
		}
	}
	mem := db.state.Load().mem
	db.mu.Unlock()

	err := db.log.AddRecord(db.batch.Bytes())
	if err == nil && db.opts.Sync {
		err = db.log.Sync()
	}
	if err == nil {
		for i, g := range group {
			db.keyBuf = ikey.Append(db.keyBuf[:0], g.key, first+uint64(i), g.kind)
			mem.list.Insert(db.keyBuf, g.value)
		}
		db.lastSeq.Store(last)
	}

	db.mu.Lock()
	db.groups.Add(1)
	db.grouped.Add(uint64(len(group)))
	if err != nil {
		err = db.fail("write-ahead log", err)
	}
	db.finish(len(group), err)
	return err
}

// pick returns the writes at the front of the queue that make one group. The
// group has at least the first write. The caller must hold db.mu.
func (db *DB) pick() []*writer {
	limit := maxGroupBytes
	if first := db.queue[0].size(); first <= smallWriteBytes {
		limit = min(limit, first+smallGroupGrowth)
	}
	size := db.queue[0].size()
	n := 1
	for n < len(db.queue) && size+db.queue[n].size() <= limit {
		size += db.queue[n].size()
		n++
	}
	return db.queue[:n:n]
}

// finish gives the result to the first n writes of the queue, removes them from
// the queue, and wakes the writes that wait. The caller must hold db.mu.
func (db *DB) finish(n int, err error) {
	for _, g := range db.queue[:n] {
		g.err = err
		g.done = true
	}
	rest := copy(db.queue, db.queue[n:])
	clear(db.queue[rest:])
	db.queue = db.queue[:rest]
	db.writeCond.Broadcast()
}
