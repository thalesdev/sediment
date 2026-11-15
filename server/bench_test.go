// SPDX-License-Identifier: MIT

package server

import (
	"fmt"
	"net"
	"testing"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
)

const benchKeys = 100000

var benchValue = make([]byte, 100)

func benchServer(b *testing.B) string {
	b.Helper()
	db, err := shale.Open(b.TempDir(), shale.Options{})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	var batch shale.Batch
	for i := range benchKeys {
		batch.Put(fmt.Appendf(nil, "key:%012d", i), benchValue)
		if batch.Len() == 1000 {
			if err := db.Write(&batch); err != nil {
				b.Fatal(err)
			}
			batch.Reset()
		}
	}
	s, err := New(Options{Store: db, Log: quietLog()})
	if err != nil {
		b.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	go func() { _ = s.Serve(l) }()
	b.Cleanup(func() {
		ctx, cancel := contextWithTimeout(5e9)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return l.Addr().String()
}

type benchClient struct {
	nc net.Conn
	r  *resp.Reader
	w  *resp.Writer
}

func newBenchClient(b *testing.B, addr string) *benchClient {
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { nc.Close() })
	return &benchClient{nc: nc, r: resp.NewReader(nc, resp.Limits{}), w: resp.NewWriter(nc)}
}

func (c *benchClient) send(b *testing.B, args ...[]byte) {
	items := make([]resp.Value, len(args))
	for i, a := range args {
		items[i] = resp.Bulk(a)
	}
	if err := c.w.WriteValue(resp.Array(items...)); err != nil {
		b.Fatal(err)
	}
}

func (c *benchClient) flushRead(b *testing.B, n int) {
	if err := c.w.Flush(); err != nil {
		b.Fatal(err)
	}
	for range n {
		if _, err := c.r.ReadValue(); err != nil {
			b.Fatal(err)
		}
	}
}

func runBench(b *testing.B, depth int, cmd string, withValue bool) {
	addr := benchServer(b)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		c := newBenchClient(b, addr)
		i := 0
		pending := 0
		for pb.Next() {
			key := fmt.Appendf(nil, "key:%012d", (i*7919)%benchKeys)
			i++
			if withValue {
				c.send(b, []byte(cmd), key, benchValue)
			} else {
				c.send(b, []byte(cmd), key)
			}
			pending++
			if pending == depth {
				c.flushRead(b, pending)
				pending = 0
			}
		}
		if pending > 0 {
			c.flushRead(b, pending)
		}
	})
}

func BenchmarkGet(b *testing.B)           { runBench(b, 1, "GET", false) }
func BenchmarkGetPipeline16(b *testing.B) { runBench(b, 16, "GET", false) }
func BenchmarkSet(b *testing.B)           { runBench(b, 1, "SET", true) }
func BenchmarkSetPipeline16(b *testing.B) { runBench(b, 16, "SET", true) }
