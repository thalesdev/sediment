// SPDX-License-Identifier: MIT

package skiplist

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

func benchList(b *testing.B, n int) *LockedList {
	b.Helper()
	l := NewLockedList(bytes.Compare, 42)
	for _, i := range rand.New(rand.NewPCG(7, 7)).Perm(n) {
		l.Insert(key(i), val(i))
	}
	return l
}

func BenchmarkInsert(b *testing.B) {
	const chunk = 1 << 16
	keys := make([][]byte, chunk)
	for i, j := range rand.New(rand.NewPCG(1, 1)).Perm(chunk) {
		keys[i] = key(j)
	}
	v := val(0)
	l := NewLockedList(bytes.Compare, 42)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if i%chunk == 0 {
			l = NewLockedList(bytes.Compare, 42)
		}
		l.Insert(keys[i%chunk], v)
	}
}

func BenchmarkSeek(b *testing.B) {
	const n = 100000
	l := benchList(b, n)
	targets := make([][]byte, 1024)
	for i := range targets {
		targets[i] = key(rand.IntN(n))
	}
	it := l.NewIterator()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		it.Seek(targets[i%len(targets)])
	}
}

func BenchmarkIteratorNext(b *testing.B) {
	l := benchList(b, 100000)
	it := l.NewIterator()
	it.SeekToFirst()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !it.Valid() {
			it.SeekToFirst()
		}
		it.Next()
	}
}
