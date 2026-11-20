// SPDX-License-Identifier: MIT

package command

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/thalesdev/sediment/internal/shale"
)

// sweepBatch is how many dead collections one write of the sweeper removes.
const sweepBatch = 512

// Sweep removes the elements of the collections that no key holds any more. A
// plain SET over a hash, and the expiry of a hash, leave its elements behind, and
// this is what takes them away. It reads the OWNER records, one for each
// collection, and for each one it looks at the KEY record as of the same snapshot.
// A collection whose KEY record is gone, expired or has another id is dead, and an
// id is never used twice, so a dead collection never lives again and no lock is
// needed. It removes the OWNER record and the elements with one range delete for
// each collection. It returns the number of collections that it removed.
func (e *Env) Sweep() (int, error) {
	snap, err := e.Store.Snapshot()
	if err != nil {
		return 0, err
	}
	defer snap.Release()
	it, err := snap.NewIterator(classBounds(classOwner))
	if err != nil {
		return 0, err
	}
	defer it.Close()
	now := e.nowMillis()
	var b shale.Batch
	pending, swept := 0, 0
	flush := func() error {
		if pending == 0 {
			return nil
		}
		err := e.Store.Write(&b)
		b.Reset()
		pending = 0
		return err
	}
	for it.SeekToFirst(); it.Valid(); it.Next() {
		k := it.Key()
		if len(k) != keyHeaderLen+8 || k[0] != classOwner {
			return swept, fmt.Errorf("an OWNER record has a key that is not valid")
		}
		slot := binary.BigEndian.Uint16(k[1:])
		uid := binary.BigEndian.Uint64(k[keyHeaderLen:])
		alive, err := e.holds(snap, it.Value(), uid, now)
		if err != nil {
			return swept, err
		}
		if alive {
			continue
		}
		b.Delete(bytes.Clone(k))
		lower := elementPrefix(slot, uid)
		b.DeleteRange(lower, prefixEnd(lower))
		pending++
		swept++
		if pending >= sweepBatch {
			if err := flush(); err != nil {
				return swept, err
			}
		}
	}
	if err := it.Err(); err != nil {
		return swept, err
	}
	return swept, flush()
}

// holds tells if the user key holds, as of the snapshot, the collection with the
// id, and that collection has not expired.
func (e *Env) holds(snap *shale.Snapshot, user []byte, uid uint64, now int64) (bool, error) {
	raw, err := snap.Get(keyOf(user))
	if isNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	rec, err := decodeRecord(raw)
	if err != nil {
		return false, err
	}
	if rec.typ == typeString || (rec.deadline != 0 && rec.deadline <= now) {
		return false, nil
	}
	have, _ := rec.collection()
	return have == uid, nil
}

// RunSweeper calls Sweep when it starts and then every interval, until stop is
// closed. An error is logged and the next pass tries again.
func (e *Env) RunSweeper(stop <-chan struct{}, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		default:
		}
		if n, err := e.Sweep(); err != nil {
			if e.Log != nil {
				e.Log.Error("sweep failed", "err", err)
			}
		} else if n > 0 && e.Log != nil {
			e.Log.Debug("swept dead collections", "collections", n)
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}
