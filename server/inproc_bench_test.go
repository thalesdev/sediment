// SPDX-License-Identifier: MIT

package server

import (
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/server/command"
)

type loopConn struct {
	chunks  [][]byte
	total   int
	next    int
	pending []byte
	written int64
}

func (c *loopConn) Read(p []byte) (int, error) {
	if len(c.pending) == 0 {
		if c.next >= c.total {
			return 0, io.EOF
		}
		c.pending = c.chunks[c.next%len(c.chunks)]
		c.next++
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *loopConn) Write(p []byte) (int, error)      { c.written += int64(len(p)); return len(p), nil }
func (c *loopConn) Close() error                     { return nil }
func (c *loopConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *loopConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *loopConn) SetDeadline(time.Time) error      { return nil }
func (c *loopConn) SetReadDeadline(time.Time) error  { return nil }
func (c *loopConn) SetWriteDeadline(time.Time) error { return nil }

type nopStore struct{ value []byte }

func (s nopStore) Get([]byte) ([]byte, error) { return s.value, nil }
func (s nopStore) Put(_, _ []byte) error      { return nil }
func (s nopStore) Delete([]byte) error        { return nil }
func (s nopStore) Write(*shale.Batch) error   { return nil }
func (s nopStore) Metrics() shale.Metrics     { return shale.Metrics{} }

var _ command.Store = nopStore{}

func benchKey(i, space int) []byte {
	return fmt.Appendf(nil, "key:%012d", (i*7919)%space)
}

func buildChunks(verb string, depth, chunks, space int) [][]byte {
	out := make([][]byte, chunks)
	n := 0
	for c := range out {
		var buf []byte
		for range depth {
			key := benchKey(n, space)
			n++
			if verb == "SET" {
				buf = fmt.Appendf(buf, "*3\r\n$3\r\nSET\r\n$%d\r\n%s\r\n$100\r\n%s\r\n", len(key), key, benchValue)
			} else {
				buf = fmt.Appendf(buf, "*2\r\n$3\r\nGET\r\n$%d\r\n%s\r\n", len(key), key)
			}
		}
		out[c] = buf
	}
	return out
}

func inproc(b *testing.B, store command.Store, verb string, space int) {
	b.Helper()
	const depth = 16
	s, err := New(Options{Store: store, Log: quietLog()})
	if err != nil {
		b.Fatal(err)
	}
	nc := &loopConn{chunks: buildChunks(verb, depth, 4096, space), total: (b.N + depth - 1) / depth}
	c := s.newConn(nc)
	b.ReportAllocs()
	b.ResetTimer()
	c.loop()
	b.StopTimer()
	if nc.written == 0 {
		b.Fatal("no reply was written")
	}
}

func shaleStore(b *testing.B, keys int, memtable int) command.Store {
	b.Helper()
	db, err := shale.Open(b.TempDir(), shale.Options{MemtableSize: memtable})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	var batch shale.Batch
	for i := range keys {
		batch.Put(fmt.Appendf(nil, "key:%012d", i), benchValue)
		if batch.Len() == 1000 {
			if err := db.Write(&batch); err != nil {
				b.Fatal(err)
			}
			batch.Reset()
		}
	}
	if err := db.WaitIdle(); err != nil {
		b.Fatal(err)
	}
	return db
}

func BenchmarkInprocSetNopStore(b *testing.B) { inproc(b, nopStore{benchValue}, "SET", 100000) }
func BenchmarkInprocGetNopStore(b *testing.B) { inproc(b, nopStore{benchValue}, "GET", 100000) }
func BenchmarkInprocSetShale(b *testing.B) {
	inproc(b, shaleStore(b, 1000, 64<<20), "SET", 3000000)
}
func BenchmarkInprocGetShaleMemtable(b *testing.B) {
	inproc(b, shaleStore(b, 100000, 64<<20), "GET", 100000)
}
func BenchmarkInprocGetShaleTables(b *testing.B) {
	inproc(b, shaleStore(b, 1000000, 1<<20), "GET", 1000000)
}

func inprocConns(b *testing.B, store command.Store, verb string, space, conns int) {
	b.Helper()
	const depth = 16
	s, err := New(Options{Store: store, Log: quietLog()})
	if err != nil {
		b.Fatal(err)
	}
	per := (b.N/conns + depth - 1) / depth
	cs := make([]*conn, conns)
	ncs := make([]*loopConn, conns)
	for i := range cs {
		chunks := buildChunks(verb, depth, 512, space)
		for j := range chunks {
			chunks[j], chunks[(j+i)%len(chunks)] = chunks[(j+i)%len(chunks)], chunks[j]
		}
		ncs[i] = &loopConn{chunks: chunks, total: per}
		cs[i] = s.newConn(ncs[i])
	}
	b.ReportAllocs()
	b.ResetTimer()
	var wg sync.WaitGroup
	for _, c := range cs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.loop()
		}()
	}
	wg.Wait()
	b.StopTimer()
}

func BenchmarkInprocSetShale50Conns(b *testing.B) {
	inprocConns(b, shaleStore(b, 1000, 64<<20), "SET", 3000000, 50)
}

func BenchmarkInprocGetShaleTables50Conns(b *testing.B) {
	inprocConns(b, shaleStore(b, 1000000, 1<<20), "GET", 1000000, 50)
}
