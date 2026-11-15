// SPDX-License-Identifier: MIT

package shale

import (
	"sync"

	"github.com/thalesdev/sediment/internal/batch"
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

// writer is a Put, a Delete or a Batch that waits in the queue of the DB. A
// writer with ops is a batch, and the other fields are not used.
type writer struct {
	kind  batch.Kind
	key   []byte
	value []byte
	ops   []batchOp
	done  bool
	err   error
}

// size returns the bytes that the write counts in a group.
func (w *writer) size() int {
	if w.ops == nil {
		return len(w.key) + len(w.value) + writeOverhead
	}
	n := 0
	for _, op := range w.ops {
		n += len(op.key) + len(op.value) + writeOverhead
	}
	return n
}

// count returns the number of operations of the write, which is the number of
// sequence numbers that it uses.
func (w *writer) count() int {
	if w.ops == nil {
		return 1
	}
	return len(w.ops)
}

var writerPool = sync.Pool{New: func() any { return new(writer) }}

// write does a Put or a Delete. The write goes into a queue. The first write in
// the queue is the leader. It takes the writes that wait behind it, up to a limit
// of size, as one group. It writes the group to the log as one record, flushes
// the log once if Options.Sync is true, adds the group to the memtable, and makes
// the last sequence number visible to Get. Then it wakes the writes of the group
// with the result. A write that is not the leader waits until the leader of its
// group wakes it, or until it becomes the leader.
func (db *DB) write(kind batch.Kind, key, value []byte) error {
	w := writerPool.Get().(*writer)
	*w = writer{kind: kind, key: key, value: value}
	return db.submit(w)
}

// submit queues w, waits for its group to be committed, and returns the result.
// It puts w back in the pool.
func (db *DB) submit(w *writer) error {
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
	ops := 0
	for _, g := range group {
		ops += g.count()
	}
	first := db.lastSeq.Load() + 1
	last := first + uint64(ops) - 1
	if last > ikey.MaxSeq {
		db.finish(len(group), errSeqExhausted)
		return errSeqExhausted
	}
	db.batch.Reset(first)
	add := func(kind batch.Kind, key, value []byte) {
		switch kind {
		case batch.KindPut:
			db.batch.Put(key, value)
		case batch.KindRangeDelete:
			db.batch.DeleteRange(key, value)
		default:
			db.batch.Delete(key)
		}
	}
	for _, g := range group {
		if g.ops != nil {
			for _, op := range g.ops {
				add(op.kind, op.key, op.value)
			}
		} else {
			add(g.kind, g.key, g.value)
		}
	}
	mem := db.state.Load().mem
	db.mu.Unlock()

	err := db.log.AddRecord(db.batch.Bytes())
	if err == nil && db.opts.Sync {
		err = db.log.Sync()
	}
	part := "write-ahead log"
	if err == nil {
		if err = db.insertGroup(mem, group, first); err != nil {
			part = "memtable"
		} else {
			db.lastSeq.Store(last)
		}
	}

	db.mu.Lock()
	db.groups.Add(1)
	db.grouped.Add(uint64(len(group)))
	if err != nil {
		err = db.fail(part, err)
	}
	db.finish(len(group), err)
	return err
}

// insertGroup adds the operations of the writes of a group to the memtable, with
// sequence numbers that start at first. The record of the group is already in the
// log. It returns the error of the memtable if the memtable has no room, which a
// limit on its size makes impossible in practice, and then the DB fails and the
// next Open reads the record from the log.
func (db *DB) insertGroup(mem *memtable, group []*writer, first uint64) error {
	seq := first
	insert := func(kind batch.Kind, key, value []byte) error {
		defer func() { seq++ }()
		if kind == batch.KindRangeDelete {
			mem.addRange(key, value, seq)
			return nil
		}
		db.keyBuf = ikey.Append(db.keyBuf[:0], key, seq, ikey.Kind(kind))
		return mem.list.Insert(db.keyBuf, value)
	}
	for _, g := range group {
		if g.ops != nil {
			for _, op := range g.ops {
				if err := insert(op.kind, op.key, op.value); err != nil {
					return err
				}
			}
			continue
		}
		if err := insert(g.kind, g.key, g.value); err != nil {
			return err
		}
	}
	return nil
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
