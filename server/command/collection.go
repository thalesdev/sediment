// SPDX-License-Identifier: MIT

package command

import (
	"errors"

	"github.com/thalesdev/sediment/internal/shale"
)

// subField is the sub byte of the elements of a hash and of a set.
const subField byte = 0

var errBadElement = errors.New("command: stored element is not valid")

// elementValue returns the stored value of an element with a payload.
func elementValue(payload []byte) []byte {
	out := make([]byte, 1+len(payload))
	copy(out[1:], payload)
	return out
}

// decodeElement returns the payload of a stored element. It returns an error, and
// never panics, if the flags are not the ones that this version writes.
func decodeElement(raw []byte) ([]byte, error) {
	if len(raw) == 0 || raw[0] != 0 {
		return nil, errBadElement
	}
	return raw[1:], nil
}

// fieldOf returns the field of the engine key of an element, and an error if the
// key is not the one of an element.
func fieldOf(k []byte) ([]byte, error) {
	const header = keyHeaderLen + 8 + 1
	if len(k) < header || k[0] != classElement || k[keyHeaderLen+8] != subField {
		return nil, errBadElement
	}
	return k[header:], nil
}

// coll is a hash or a set that a command works on. It holds the state that the
// command read under the lock of the key, and it builds the writes of the
// command, which are all in one batch.
type coll struct {
	env   *Env
	key   []byte
	slot  uint16
	typ   valueType
	rec   record
	found bool
	uid   uint64
	count uint64
}

// openColl reads the KEY record of key, which must be a collection of the type, or
// absent. A key of another type gives errWrongType. The caller must hold the lock
// of the key if it is going to write.
func (e *Env) openColl(key []byte, typ valueType) (*coll, error) {
	c := &coll{env: e, key: key, slot: slotOf(key), typ: typ}
	rec, err := e.loadRecord(key)
	switch {
	case err == nil:
		if rec.typ != typ {
			return nil, errWrongType
		}
		c.rec, c.found = rec, true
		c.uid, c.count = rec.collection()
	case !isNotFound(err):
		return nil, err
	}
	return c, nil
}

// elem returns the engine key of a field of the collection. It is only for a
// collection that exists, or after ensureUID.
func (c *coll) elem(field []byte) []byte {
	return elementKey(c.slot, c.uid, subField, field)
}

// get reads the payload of a field.
func (c *coll) get(field []byte) ([]byte, bool, error) {
	if !c.found {
		return nil, false, nil
	}
	raw, err := c.env.Store.Get(c.elem(field))
	if isNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	payload, err := decodeElement(raw)
	return payload, err == nil, err
}

// ensureUID gives a new collection an id.
func (c *coll) ensureUID() error {
	if c.found || c.uid != 0 {
		return nil
	}
	uid, err := c.env.allocUID()
	c.uid = uid
	return err
}

// put adds the write of a field to b.
func (c *coll) put(b *shale.Batch, field, payload []byte) {
	b.Put(c.elem(field), elementValue(payload))
}

// del adds the delete of a field to b.
func (c *coll) del(b *shale.Batch, field []byte) {
	b.Delete(c.elem(field))
}

// finish adds to b the write of the KEY record with the new number of elements,
// and the OWNER record of a collection that is new. If the number is zero the
// collection goes away, as in Redis: its KEY and OWNER records and all its
// elements are deleted. It writes b.
func (c *coll) finish(b *shale.Batch, count uint64) error {
	switch {
	case count == 0 && !c.found:
		return nil
	case count == 0:
		var gone shale.Batch
		batchDelete(&gone, c.key, c.rec)
		return c.env.Store.Write(&gone)
	}
	b.Put(keyOf(c.key), encodeRecord(c.typ, c.rec.deadline, collectionBody(c.uid, count)))
	if !c.found {
		b.Put(ownerKey(c.slot, c.uid), c.key)
	}
	return c.env.Store.Write(b)
}

// collAt reads a collection as of a snapshot. A key that is absent or expired
// gives a collection that is not found, and a key of another type gives
// errWrongType.
func (e *Env) collAt(snap *shale.Snapshot, key []byte, typ valueType) (*coll, error) {
	c := &coll{env: e, key: key, slot: slotOf(key), typ: typ}
	raw, err := snap.Get(keyOf(key))
	if isNotFound(err) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	rec, err := e.liveRecord(raw)
	switch {
	case isNotFound(err):
		return c, nil
	case err != nil:
		return nil, err
	case rec.typ != typ:
		return nil, errWrongType
	}
	c.rec, c.found = rec, true
	c.uid, c.count = rec.collection()
	return c, nil
}

// readColl takes a snapshot and reads a collection as of it. The caller must
// release the snapshot, which it gets back, unless err is not nil.
func (e *Env) readColl(key []byte, typ valueType) (*shale.Snapshot, *coll, error) {
	snap, err := e.Store.Snapshot()
	if err != nil {
		return nil, nil, err
	}
	c, err := e.collAt(snap, key, typ)
	if err != nil {
		snap.Release()
		return nil, nil, err
	}
	return snap, c, nil
}

// hasIn tells if a field is in the collection as of the snapshot.
func (c *coll) hasIn(snap *shale.Snapshot, field []byte) (bool, error) {
	if !c.found {
		return false, nil
	}
	_, err := snap.Get(c.elem(field))
	if isNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// each calls fn for each field of the collection in the snapshot, with its
// payload. The slices are valid until fn returns. If fn returns false, each stops.
func (c *coll) each(snap *shale.Snapshot, fn func(field, payload []byte) bool) error {
	if !c.found {
		return nil
	}
	it, err := snap.NewIterator(elementBounds(c.slot, c.uid))
	if err != nil {
		return err
	}
	defer it.Close()
	for it.SeekToFirst(); it.Valid(); it.Next() {
		field, err := fieldOf(it.Key())
		if err != nil {
			return err
		}
		payload, err := decodeElement(it.Value())
		if err != nil {
			return err
		}
		if !fn(field, payload) {
			return nil
		}
	}
	return it.Err()
}
