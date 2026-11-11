// SPDX-License-Identifier: MIT

package wal

import (
	"bytes"
	"fmt"
	"io"
	"testing"
)

type nullDst struct{}

func (nullDst) Write(p []byte) (int, error) { return len(p), nil }
func (nullDst) Sync() error                 { return nil }

func BenchmarkAddRecord(b *testing.B) {
	for _, size := range []int{100, 1 << 10, 40 << 10} {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			data := payloadOf(size, 1)
			w := NewWriter(nullDst{})
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := w.AddRecord(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkReaderNext(b *testing.B) {
	for _, size := range []int{100, 1 << 10, 40 << 10} {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			var buf bytes.Buffer
			w := NewWriter(struct {
				io.Writer
				nullSync
			}{&buf, nullSync{}})
			data := payloadOf(size, 1)
			count := max(1, (4<<20)/size)
			for range count {
				if err := w.AddRecord(data); err != nil {
					b.Fatal(err)
				}
			}
			log := buf.Bytes()
			r := NewReader(bytes.NewReader(log))
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				rec, err := r.Next()
				if err == io.EOF {
					r = NewReader(bytes.NewReader(log))
					continue
				}
				if err != nil || len(rec) != size {
					b.Fatalf("Next: len=%d err=%v", len(rec), err)
				}
			}
		})
	}
}

type nullSync struct{}

func (nullSync) Sync() error { return nil }
