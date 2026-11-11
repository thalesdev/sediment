// SPDX-License-Identifier: MIT

package shale

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/thalesdev/sediment/internal/ikey"
	"github.com/thalesdev/sediment/internal/manifest"
	"github.com/thalesdev/sediment/internal/skiplist"
	"github.com/thalesdev/sediment/internal/sstable"
)

// memtable is a skip list of internal keys with the data that the flush needs.
type memtable struct {
	list *skiplist.LockedList

	// wal is the number of the log file that holds the writes of the memtable.
	wal uint64

	// maxSeq is the largest sequence number in the memtable. The DB sets it when
	// it freezes the memtable.
	maxSeq uint64
}

// newMemtable returns an empty memtable for the log file with number wal.
func newMemtable(wal uint64) *memtable {
	return &memtable{
		list: skiplist.NewLockedList(ikey.Compare, uint64(time.Now().UnixNano())),
		wal:  wal,
	}
}

// table is a table file that is open for reads.
type table struct {
	meta   manifest.TableMeta
	file   *os.File
	reader *sstable.Reader
}

// readState is what a read sees: the memtable, the frozen memtable if there is
// one, and the tables from the newest to the oldest. A readState does not change
// after the DB publishes it.
type readState struct {
	mem    *memtable
	imm    *memtable
	tables []*table
}

// tableOptions returns the options of the tables of the DB.
func tableOptions() sstable.Options {
	return sstable.Options{Compare: ikey.Compare, FilterKey: ikey.FilterKey}
}

// openTable opens the table file of meta in dir. It returns an error if the size
// of the file is not the size of the MANIFEST or if the table does not open.
func openTable(dir string, meta manifest.TableMeta) (*table, error) {
	name := tableName(meta.Number)
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("shale: opening table %s: %w", name, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("shale: table %s: %w", name, err)
	}
	if uint64(info.Size()) != meta.Size {
		f.Close()
		return nil, fmt.Errorf("shale: table %s has %d bytes, but the MANIFEST says %d: %w", name, info.Size(), meta.Size, manifest.ErrCorrupt)
	}
	reader, err := sstable.Open(f, info.Size(), tableOptions())
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("shale: table %s: %w", name, err)
	}
	return &table{meta: meta, file: f, reader: reader}, nil
}

// lookupMemtable finds the newest version of user that the lookup key sees in m.
func lookupMemtable(m *memtable, user, lookup []byte) (value []byte, deleted, found bool, err error) {
	it := m.list.NewIterator()
	defer it.Close()
	it.Seek(lookup)
	if !it.Valid() {
		return nil, false, false, nil
	}
	return decideEntry(it.Key(), it.Value(), user)
}

// lookup finds the newest version of user that the lookup key sees in the
// table. It does not read the table if user is out of the range of its keys.
func (t *table) lookup(user, lookup []byte) (value []byte, deleted, found bool, err error) {
	if bytes.Compare(user, ikey.UserKey(t.meta.Smallest)) < 0 || bytes.Compare(user, ikey.UserKey(t.meta.Largest)) > 0 {
		return nil, false, false, nil
	}
	key, value, ok, err := t.reader.Find(lookup)
	if err != nil || !ok {
		return nil, false, false, err
	}
	return decideEntry(key, value, user)
}

// decideEntry tells if the entry with key and value is a version of user. If it
// is, it tells if the version is a delete.
func decideEntry(key, value, user []byte) (v []byte, deleted, found bool, err error) {
	entryUser, _, kind, err := ikey.Parse(key)
	if err != nil {
		return nil, false, false, err
	}
	if !bytes.Equal(entryUser, user) {
		return nil, false, false, nil
	}
	return value, kind == ikey.KindDelete, true, nil
}
