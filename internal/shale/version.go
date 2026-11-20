// SPDX-License-Identifier: MIT

package shale

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thalesdev/sediment/internal/ikey"
	"github.com/thalesdev/sediment/internal/manifest"
	"github.com/thalesdev/sediment/internal/skiplist"
	"github.com/thalesdev/sediment/internal/sstable"
)

// memtable is a skip list of internal keys with the data that the flush needs.
type memtable struct {
	list *skiplist.List

	// wal is the number of the log file that holds the writes of the memtable.
	wal uint64

	// maxSeq is the largest sequence number in the memtable. The DB sets it when
	// it freezes the memtable.
	maxSeq uint64

	tombMu    sync.Mutex
	tombs     []rangeTomb
	tombCount atomic.Int64
	tombBytes atomic.Int64
	tombIdx   atomic.Pointer[tombIndex]
}

// addRange adds a range delete with sequence number seq. It copies start and end.
// The writer of the memtable calls it, and so does recovery.
func (m *memtable) addRange(start, end []byte, seq uint64) {
	t := rangeTomb{start: bytes.Clone(start), end: bytes.Clone(end), seq: seq}
	m.tombMu.Lock()
	m.tombs = append(m.tombs, t)
	m.tombMu.Unlock()
	m.tombBytes.Add(int64(len(start) + len(end) + tombOverhead))
	m.tombCount.Add(1)
}

// cover returns the index of the range deletes of the memtable, or nil if it
// has none. It builds the index again when range deletes came after the last one.
// A reader that gets it sees every range delete that was added before the call.
func (m *memtable) cover() *tombIndex {
	n := int(m.tombCount.Load())
	if n == 0 {
		return nil
	}
	if idx := m.tombIdx.Load(); idx != nil && idx.count >= n {
		return idx
	}
	m.tombMu.Lock()
	defer m.tombMu.Unlock()
	if idx := m.tombIdx.Load(); idx != nil && idx.count == len(m.tombs) {
		return idx
	}
	idx := newTombIndex(m.tombs)
	m.tombIdx.Store(idx)
	return idx
}

// rangeTombs returns a copy of the list of the range deletes of the memtable.
func (m *memtable) rangeTombs() []rangeTomb {
	m.tombMu.Lock()
	defer m.tombMu.Unlock()
	return slices.Clone(m.tombs)
}

// approximateSize returns the size that the freeze of the memtable depends on: the
// size of its list and of its range deletes.
func (m *memtable) approximateSize() int {
	return m.list.ApproximateSize() + int(m.tombBytes.Load())
}

// empty tells if the memtable has no entry and no range delete.
func (m *memtable) empty() bool {
	if m.tombCount.Load() != 0 {
		return false
	}
	it := m.list.NewIterator()
	defer it.Close()
	it.SeekToFirst()
	return !it.Valid()
}

// newMemtable returns an empty memtable for the log file with number wal.
func newMemtable(wal uint64) *memtable {
	return &memtable{
		list: skiplist.NewList(ikey.Compare, uint64(time.Now().UnixNano())),
		wal:  wal,
	}
}

// table is a table file that is open for reads. It counts the read states that
// hold it. When the count is 0 the table closes its file, and it removes the
// file too if the table is obsolete.
type table struct {
	meta     manifest.TableMeta
	path     string
	file     *os.File
	reader   *sstable.Reader
	refs     atomic.Int64
	obsolete atomic.Bool
	cache    *sstable.BlockCache
	lg       *slog.Logger

	tombs   []rangeTomb
	tombIdx *tombIndex
}

// unref drops one reference. The last reference closes the file, and removes it
// if the table is obsolete. A failure to remove the file is not an error: the
// next Open removes a file that the MANIFEST does not name.
func (t *table) unref() {
	if t.refs.Add(-1) != 0 {
		return
	}
	t.file.Close()
	if t.obsolete.Load() {
		if err := os.Remove(t.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.lg.Warn("an obsolete table file was not removed, the next Open removes it", "table", t.meta.Number, "err", err)
		}
		t.cache.DropTable(t.meta.Number)
	}
}

// readState is what a read sees: the memtable, the frozen memtable if there is
// one, and the tables from the newest to the oldest by sequence number. A
// readState does not change after the DB publishes it. It counts the readers
// that use it, and the DB holds one reference while the state is the current one.
type readState struct {
	mem    *memtable
	imm    *memtable
	tables []*table
	refs   atomic.Int64
}

// newReadState returns a state with one reference, which is the one of the DB.
func newReadState(mem, imm *memtable, tables []*table) *readState {
	s := &readState{mem: mem, imm: imm, tables: tables}
	s.refs.Store(1)
	return s
}

// tryAcquire adds a reference if the state still has one. It returns false if
// the state already gave up its tables, and then the caller must load the state
// again.
func (s *readState) tryAcquire() bool {
	for {
		n := s.refs.Load()
		if n <= 0 {
			return false
		}
		if s.refs.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

// release drops one reference. When the last one goes, the state drops its
// references on the tables.
func (s *readState) release() {
	if s.refs.Add(-1) != 0 {
		return
	}
	for _, t := range s.tables {
		t.unref()
	}
}

// tableOptions returns the options of the tables of the DB.
func tableOptions() sstable.Options {
	return sstable.Options{Compare: ikey.Compare, FilterKey: ikey.FilterKey}
}

// readerOptions returns the options of the reader of the table with the file number
// number, which keeps its blocks in cache. A nil cache keeps nothing.
func readerOptions(cache *sstable.BlockCache, number uint64) sstable.Options {
	o := tableOptions()
	o.Cache, o.CacheID = cache, number
	return o
}

// openTable opens the table file of meta in dir. It returns an error if the size
// of the file is not the size of the MANIFEST or if the table does not open.
func openTable(dir string, meta manifest.TableMeta, cache *sstable.BlockCache, lg *slog.Logger) (*table, error) {
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
	reader, err := sstable.Open(f, info.Size(), readerOptions(cache, meta.Number))
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("shale: table %s: %w", name, err)
	}
	tombs, err := loadTombs(reader)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("shale: table %s: %w", name, err)
	}
	return &table{meta: meta, path: filepath.Join(dir, name), file: f, reader: reader, cache: cache, lg: lg, tombs: tombs, tombIdx: newTombIndex(tombs)}, nil
}

// lookupMemtable finds the newest version of user that the lookup key sees in m.
func lookupMemtable(m *memtable, user, lookup []byte) (value []byte, seq uint64, deleted, found bool, err error) {
	it := m.list.NewIterator()
	defer it.Close()
	it.Seek(lookup)
	if !it.Valid() {
		return nil, 0, false, false, nil
	}
	return decideEntry(it.Key(), it.Value(), user)
}

// lookup finds the newest version of user that the lookup key sees in the
// table. It does not read the table if user is out of the range of its keys.
func (t *table) lookup(user, lookup []byte) (value []byte, seq uint64, deleted, found bool, err error) {
	if bytes.Compare(user, ikey.UserKey(t.meta.Smallest)) < 0 || bytes.Compare(user, ikey.UserKey(t.meta.Largest)) > 0 {
		return nil, 0, false, false, nil
	}
	key, value, ok, err := t.reader.Find(lookup)
	if err != nil || !ok {
		return nil, 0, false, false, err
	}
	return decideEntry(key, value, user)
}

// decideEntry tells if the entry with key and value is a version of user. If it
// is, it tells its sequence number and if the version is a delete.
func decideEntry(key, value, user []byte) (v []byte, seq uint64, deleted, found bool, err error) {
	entryUser, seq, kind, err := ikey.Parse(key)
	if err != nil {
		return nil, 0, false, false, err
	}
	if !bytes.Equal(entryUser, user) {
		return nil, 0, false, false, nil
	}
	return value, seq, kind == ikey.KindDelete, true, nil
}

// loadTombs reads the range deletes of a table, and checks each one: a key that is
// an internal key with the kind delete, and an end that is above the start. It
// returns an error that wraps manifest.ErrCorrupt otherwise.
func loadTombs(r *sstable.Reader) ([]rangeTomb, error) {
	dels := r.RangeDels()
	if len(dels) == 0 {
		return nil, nil
	}
	tombs := make([]rangeTomb, 0, len(dels))
	for _, d := range dels {
		start, seq, kind, err := ikey.Parse(d.Key)
		if err != nil {
			return nil, fmt.Errorf("range delete: %w: %w", manifest.ErrCorrupt, err)
		}
		if kind != ikey.KindDelete || bytes.Compare(start, d.End) >= 0 {
			return nil, fmt.Errorf("range delete of %q to %q is not valid: %w", start, d.End, manifest.ErrCorrupt)
		}
		tombs = append(tombs, rangeTomb{start: start, end: d.End, seq: seq})
	}
	return tombs, nil
}
