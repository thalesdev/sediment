// SPDX-License-Identifier: MIT

package sstable

import (
	"bytes"
	"fmt"
	"testing"
)

const benchEntries = 20000

func benchTable(b *testing.B) (*Reader, []kv) {
	b.Helper()
	entries := numbered(benchEntries, 100)
	dst := &fakeDst{}
	w := NewWriter(dst, Options{})
	for _, e := range entries {
		if err := w.Add(e.key, e.value); err != nil {
			b.Fatal(err)
		}
	}
	if err := w.Finish(); err != nil {
		b.Fatal(err)
	}
	data := dst.buf.Bytes()
	r, err := Open(bytes.NewReader(data), int64(len(data)), Options{})
	if err != nil {
		b.Fatal(err)
	}
	return r, entries
}

func BenchmarkWriterAdd(b *testing.B) {
	entries := numbered(benchEntries, 100)
	var w *Writer
	b.SetBytes(int64(len(entries[0].key) + len(entries[0].value)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if i%benchEntries == 0 {
			w = NewWriter(&fakeDst{}, Options{})
		}
		e := entries[i%benchEntries]
		if err := w.Add(e.key, e.value); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBlockIter(b *testing.B) {
	entries := numbered(256, 100)
	block := buildBlock(entries, 16)
	b.Run("Next", func(b *testing.B) {
		it, err := newBlockIter(block, bytes.Compare)
		if err != nil {
			b.Fatal(err)
		}
		it.SeekToFirst()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if !it.Valid() {
				it.SeekToFirst()
			}
			it.Next()
		}
	})
	b.Run("Seek", func(b *testing.B) {
		it, err := newBlockIter(block, bytes.Compare)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := range b.N {
			it.Seek(entries[i%len(entries)].key)
		}
	})
}

func BenchmarkReaderGet(b *testing.B) {
	r, entries := benchTable(b)
	b.Run("hit", func(b *testing.B) {
		b.ReportAllocs()
		for i := range b.N {
			if _, ok, err := r.Get(entries[i%benchEntries].key); !ok || err != nil {
				b.Fatalf("Get: %v %v", ok, err)
			}
		}
	})
	b.Run("miss", func(b *testing.B) {
		misses := make([][]byte, 1024)
		for i := range misses {
			misses[i] = fmt.Appendf(nil, "key-%08d-x", i*17)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := range b.N {
			if _, ok, err := r.Get(misses[i%len(misses)]); ok || err != nil {
				b.Fatalf("Get: %v %v", ok, err)
			}
		}
	})
}

func BenchmarkIterator(b *testing.B) {
	r, entries := benchTable(b)
	b.Run("Next", func(b *testing.B) {
		it := r.NewIterator()
		it.SeekToFirst()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if !it.Valid() {
				it.SeekToFirst()
			}
			it.Next()
		}
	})
	b.Run("Seek", func(b *testing.B) {
		it := r.NewIterator()
		b.ReportAllocs()
		b.ResetTimer()
		for i := range b.N {
			it.Seek(entries[(i*7919)%benchEntries].key)
		}
	})
}
