// SPDX-License-Identifier: MIT

package command

import (
	"encoding/binary"
	"errors"
	"math"
	"time"

	"github.com/thalesdev/sediment/internal/shale"
)

// valueType is the type of the value of a key.
type valueType uint8

const (
	typeString valueType = 0
	typeHash   valueType = 1
	typeSet    valueType = 2
)

// name returns the name of the type, as TYPE replies it.
func (t valueType) name() string {
	switch t {
	case typeHash:
		return "hash"
	case typeSet:
		return "set"
	default:
		return "string"
	}
}

const (
	hdrTypeMask    byte = 0x0f
	hdrHasDeadline byte = 0x10
	hdrKnown       byte = hdrTypeMask | hdrHasDeadline

	deadlineLen       = 8
	collectionBodyLen = 16
)

var (
	errBadHeader = errors.New("command: stored value has a bad header")
	errWrongType = errors.New("command: wrong type")
)

// record is the value of a KEY record: its type, its deadline, and its body. The
// body of a string is the string. The body of a hash and of a set is the id of the
// collection and the number of its elements, as two big endian integers.
type record struct {
	typ      valueType
	deadline int64
	body     []byte
}

// encodeRecord returns the stored form of a record. A deadline of zero means that
// the key never expires. Otherwise the deadline is a Unix time in milliseconds.
func encodeRecord(typ valueType, deadline int64, body []byte) []byte {
	hdr := byte(typ)
	n := 1 + len(body)
	if deadline != 0 {
		hdr |= hdrHasDeadline
		n += deadlineLen
	}
	out := make([]byte, 0, n)
	out = append(out, hdr)
	if deadline != 0 {
		out = binary.LittleEndian.AppendUint64(out, uint64(deadline))
	}
	return append(out, body...)
}

// decodeRecord splits a stored value into its parts. It returns an error, and
// never panics, when the header is not valid: an unknown type or flag, a missing or
// not positive deadline, or a body of a collection that is not 16 bytes. The body
// is a slice of raw.
func decodeRecord(raw []byte) (record, error) {
	if len(raw) == 0 || raw[0]&^hdrKnown != 0 {
		return record{}, errBadHeader
	}
	hdr := raw[0]
	r := record{typ: valueType(hdr & hdrTypeMask)}
	if r.typ > typeSet {
		return record{}, errBadHeader
	}
	raw = raw[1:]
	if hdr&hdrHasDeadline != 0 {
		if len(raw) < deadlineLen {
			return record{}, errBadHeader
		}
		r.deadline = int64(binary.LittleEndian.Uint64(raw))
		if r.deadline <= 0 {
			return record{}, errBadHeader
		}
		raw = raw[deadlineLen:]
	}
	if r.typ != typeString && len(raw) != collectionBodyLen {
		return record{}, errBadHeader
	}
	r.body = raw
	return r, nil
}

// collectionBody returns the body of a hash or a set record.
func collectionBody(uid, count uint64) []byte {
	b := binary.BigEndian.AppendUint64(nil, uid)
	return binary.BigEndian.AppendUint64(b, count)
}

// collection returns the id and the number of elements of a hash or a set record.
func (r record) collection() (uid, count uint64) {
	return binary.BigEndian.Uint64(r.body), binary.BigEndian.Uint64(r.body[8:])
}

// ExpiredFilter returns a function for the compaction filter of the engine. It
// reports true for the KEY record of a key whose deadline has passed. It reports
// false for everything that it cannot read, and for each record of another class,
// so that the engine never drops what it does not understand.
func ExpiredFilter(now func() time.Time) func(key, value []byte) bool {
	if now == nil {
		now = time.Now
	}
	return func(key, value []byte) bool {
		if len(key) == 0 || key[0] != classKey {
			return false
		}
		r, err := decodeRecord(value)
		return err == nil && r.deadline != 0 && r.deadline <= now().UnixMilli()
	}
}

// nowMillis returns the time of the server in milliseconds since the Unix epoch.
func (e *Env) nowMillis() int64 {
	if e.Now != nil {
		return e.Now().UnixMilli()
	}
	return time.Now().UnixMilli()
}

// loadRecord returns the record of a user key, of any type. A key whose deadline
// has passed is reported as not found.
func (e *Env) loadRecord(key []byte) (record, error) {
	raw, err := e.Store.Get(keyOf(key))
	if err != nil {
		return record{}, err
	}
	return e.liveRecord(raw)
}

// liveRecord decodes the stored value of a KEY record. A key whose deadline has
// passed is reported as not found.
func (e *Env) liveRecord(raw []byte) (record, error) {
	r, err := decodeRecord(raw)
	if err != nil {
		return record{}, err
	}
	if r.deadline != 0 && r.deadline <= e.nowMillis() {
		return record{}, shale.ErrNotFound
	}
	return r, nil
}

// load returns the value of a string key and its deadline, which is zero for a
// key that does not expire. A key whose deadline has passed is reported as not
// found, and a key of another type gives errWrongType.
func (e *Env) load(key []byte) ([]byte, int64, error) {
	r, err := e.loadRecord(key)
	if err != nil {
		return nil, 0, err
	}
	if r.typ != typeString {
		return nil, 0, errWrongType
	}
	return r.body, r.deadline, nil
}

// save writes the value of a string key with a deadline. Zero means no expiry. A
// collection that the key held is not removed here: the sweeper finds it.
func (e *Env) save(key, value []byte, deadline int64) error {
	return e.Store.Put(keyOf(key), encodeRecord(typeString, deadline, value))
}

// saveRecord writes a record of any type for a user key.
func (e *Env) saveRecord(key []byte, r record) error {
	return e.Store.Put(keyOf(key), encodeRecord(r.typ, r.deadline, r.body))
}

// batchPut adds a write of the value of a string key, with a deadline, to a batch.
func batchPut(b *shale.Batch, key, value []byte, deadline int64) {
	b.Put(keyOf(key), encodeRecord(typeString, deadline, value))
}

// batchDelete adds to b the deletion of a user key that holds the record r: its
// KEY record and, for a collection, its OWNER record and all its elements. The
// elements go with one range delete, so a collection of any size is one atomic
// write.
func batchDelete(b *shale.Batch, key []byte, r record) {
	b.Delete(keyOf(key))
	if r.typ == typeString {
		return
	}
	uid, _ := r.collection()
	slot := slotOf(key)
	b.Delete(ownerKey(slot, uid))
	lower := elementPrefix(slot, uid)
	b.DeleteRange(lower, prefixEnd(lower))
}

// deleteRecord deletes a user key that holds the record r, with its elements.
func (e *Env) deleteRecord(key []byte, r record) error {
	if r.typ == typeString {
		return e.Store.Delete(keyOf(key))
	}
	var b shale.Batch
	batchDelete(&b, key, r)
	return e.Store.Write(&b)
}

// deadlineAt returns the deadline for n units after base, in milliseconds, and
// false if it does not fit in an int64. The unit is the number of milliseconds
// in one unit of n, and n must be positive.
func deadlineAt(base, n, unit int64) (int64, bool) {
	if n > (math.MaxInt64-base)/unit {
		return 0, false
	}
	return base + n*unit, true
}
