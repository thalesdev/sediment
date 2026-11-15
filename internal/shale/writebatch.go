// SPDX-License-Identifier: MIT

package shale

import (
	"bytes"

	"github.com/thalesdev/sediment/internal/batch"
)

// batchOp is one operation of a Batch.
type batchOp struct {
	kind  batch.Kind
	key   []byte
	value []byte
}

// Batch is a list of Put, Delete and DeleteRange operations that DB.Write applies as one
// unit. The zero value is an empty batch. A Batch keeps the slices that the
// caller gives it, so the caller must not change them until Write returns. It is
// not safe for concurrent use.
type Batch struct {
	ops []batchOp
}

// Put adds an operation that sets the value of key.
func (b *Batch) Put(key, value []byte) {
	b.ops = append(b.ops, batchOp{kind: batch.KindPut, key: key, value: value})
}

// Delete adds an operation that deletes key.
func (b *Batch) Delete(key []byte) {
	b.ops = append(b.ops, batchOp{kind: batch.KindDelete, key: key})
}

// DeleteRange adds an operation that deletes every key from start up to, and not
// including, end. It deletes the keys that exist when the batch is applied, and
// the puts of the same batch that come before it, and not the ones that come
// after it. A range that is empty or inverted deletes nothing, and the batch does
// not keep it.
func (b *Batch) DeleteRange(start, end []byte) {
	if bytes.Compare(start, end) >= 0 {
		return
	}
	b.ops = append(b.ops, batchOp{kind: batch.KindRangeDelete, key: start, value: end})
}

// Len returns the number of operations in the batch.
func (b *Batch) Len() int {
	return len(b.ops)
}

// Reset removes all the operations and keeps the memory of the batch.
func (b *Batch) Reset() {
	clear(b.ops)
	b.ops = b.ops[:0]
}

// Write applies the operations of b in order, as one unit. All of them are in
// one record of the log, so after a crash either all of them or none of them are
// there, and a Get never sees a part of them. A later operation on a key wins
// over an earlier one. An empty batch does nothing. As with Put, if Write returns
// an error the batch can be in the log or not, and the DB refuses all later
// writes.
func (db *DB) Write(b *Batch) error {
	if len(b.ops) == 0 {
		return nil
	}
	w := writerPool.Get().(*writer)
	*w = writer{ops: b.ops}
	return db.submit(w)
}
