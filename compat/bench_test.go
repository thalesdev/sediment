// SPDX-License-Identifier: MIT

package compat

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/thalesdev/sediment/internal/shale"
)

const (
	benchValueBytes = 100
	benchMemtable   = 4 << 20
	benchWriters    = 8
)

type benchEngine interface {
	put(key, value []byte) error
	get(key []byte) error
	settle() error
	tableBytes() uint64
	files() int
	close() error
}

type shaleEngine struct{ db *shale.DB }

func openShaleBench(b *testing.B, sync bool) benchEngine {
	b.Helper()
	db, err := shale.Open(b.TempDir(), shale.Options{Sync: sync, MemtableSize: benchMemtable})
	if err != nil {
		b.Fatal(err)
	}
	return &shaleEngine{db}
}

func (e *shaleEngine) put(key, value []byte) error { return e.db.Put(key, value) }
func (e *shaleEngine) get(key []byte) error        { _, err := e.db.Get(key); return err }
func (e *shaleEngine) settle() error               { return e.db.WaitIdle() }
func (e *shaleEngine) close() error                { return e.db.Close() }
func (e *shaleEngine) files() int                  { return e.db.Metrics().Tables }
func (e *shaleEngine) tableBytes() uint64 {
	m := e.db.Metrics()
	return m.FlushBytes + m.CompactionBytesWritten
}

type countingStorage struct {
	storage.Storage
	tables atomic.Uint64
}

type countingWriter struct {
	storage.Writer
	tables *atomic.Uint64
}

func (s *countingStorage) Create(fd storage.FileDesc) (storage.Writer, error) {
	w, err := s.Storage.Create(fd)
	if err != nil || fd.Type != storage.TypeTable {
		return w, err
	}
	return &countingWriter{w, &s.tables}, nil
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.tables.Add(uint64(n))
	return n, err
}

type levelEngine struct {
	db      *leveldb.DB
	storage *countingStorage
	write   *opt.WriteOptions
}

func openLevelBench(b *testing.B, sync bool) benchEngine {
	b.Helper()
	stor, err := storage.OpenFile(b.TempDir(), false)
	if err != nil {
		b.Fatal(err)
	}
	counting := &countingStorage{Storage: stor}
	db, err := leveldb.Open(counting, &opt.Options{
		Compression: opt.NoCompression,
		WriteBuffer: benchMemtable,
		Filter:      filter.NewBloomFilter(10),
	})
	if err != nil {
		b.Fatal(err)
	}
	return &levelEngine{db: db, storage: counting, write: &opt.WriteOptions{Sync: sync}}
}

func (e *levelEngine) put(key, value []byte) error { return e.db.Put(key, value, e.write) }
func (e *levelEngine) get(key []byte) error        { _, err := e.db.Get(key, nil); return err }
func (e *levelEngine) settle() error               { return e.db.CompactRange(util.Range{}) }
func (e *levelEngine) close() error                { return e.db.Close() }
func (e *levelEngine) tableBytes() uint64          { return e.storage.tables.Load() }

func (e *levelEngine) files() int {
	total := 0
	for level := range 7 {
		value, err := e.db.GetProperty(fmt.Sprintf("leveldb.num-files-at-level%d", level))
		if err != nil {
			continue
		}
		var n int
		fmt.Sscanf(value, "%d", &n)
		total += n
	}
	return total
}

func benchKey(dst []byte, n uint64) []byte {
	return fmt.Appendf(dst[:0], "%016d", n)
}

func benchValue() []byte {
	rng := rand.New(rand.NewPCG(7, 7))
	value := make([]byte, benchValueBytes)
	for i := range value {
		value[i] = byte('a' + rng.IntN(26))
	}
	return value
}

func eachEngine(b *testing.B, sync bool, run func(b *testing.B, e benchEngine)) {
	opens := []struct {
		name string
		open func(*testing.B, bool) benchEngine
	}{{"shale", openShaleBench}, {"goleveldb", openLevelBench}}
	for _, o := range opens {
		b.Run(o.name, func(b *testing.B) {
			if b.N == 1 {
				return
			}
			e := o.open(b, sync)
			defer e.close()
			run(b, e)
		})
	}
}

func prefill(b *testing.B, e benchEngine, keys int) {
	b.Helper()
	value := benchValue()
	key := make([]byte, 0, 16)
	for i := range keys {
		if err := e.put(benchKey(key, uint64(i)), value); err != nil {
			b.Fatal(err)
		}
	}
	if err := e.settle(); err != nil {
		b.Fatal(err)
	}
}

func reportAmplification(b *testing.B, e benchEngine, before uint64, ops int) {
	b.Helper()
	user := float64(ops * (16 + benchValueBytes))
	b.ReportMetric(float64(e.tableBytes()-before)/user, "write-amp")
}

func writeRandom(b *testing.B, e benchEngine, keys, ops int, writers int) {
	b.Helper()
	value := benchValue()
	before := e.tableBytes()
	var wg sync.WaitGroup
	var failed atomic.Bool
	b.ResetTimer()
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(w)+1, 99))
			key := make([]byte, 0, 16)
			for range ops / writers {
				if err := e.put(benchKey(key, rng.Uint64N(uint64(keys))), value); err != nil {
					failed.Store(true)
					return
				}
			}
		}()
	}
	wg.Wait()
	b.StopTimer()
	if failed.Load() {
		b.Fatal("a write failed")
	}
	reportAmplification(b, e, before, ops)
}

func BenchmarkFillRandom(b *testing.B) {
	eachEngine(b, false, func(b *testing.B, e benchEngine) { writeRandom(b, e, b.N, b.N, 1) })
}

func BenchmarkOverwrite(b *testing.B) {
	eachEngine(b, false, func(b *testing.B, e benchEngine) {
		prefill(b, e, b.N)
		writeRandom(b, e, b.N, b.N, 1)
	})
}

func BenchmarkReadRandom(b *testing.B) {
	eachEngine(b, false, func(b *testing.B, e benchEngine) {
		prefill(b, e, b.N)
		rng := rand.New(rand.NewPCG(3, 3))
		key := make([]byte, 0, 16)
		b.ResetTimer()
		for range b.N {
			if err := e.get(benchKey(key, rng.Uint64N(uint64(b.N)))); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		b.ReportMetric(float64(e.files()), "files")
	})
}

func BenchmarkFillSync(b *testing.B) {
	eachEngine(b, true, func(b *testing.B, e benchEngine) { writeRandom(b, e, b.N, b.N, 1) })
}

func BenchmarkFillRandomParallel(b *testing.B) {
	eachEngine(b, false, func(b *testing.B, e benchEngine) { writeRandom(b, e, b.N, b.N, benchWriters) })
}

func BenchmarkFillSyncParallel(b *testing.B) {
	eachEngine(b, true, func(b *testing.B, e benchEngine) { writeRandom(b, e, b.N, b.N, benchWriters) })
}
