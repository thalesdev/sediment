// SPDX-License-Identifier: MIT

package server

import (
	"net"
	"testing"
	"time"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
)

type client struct {
	t  *testing.T
	nc net.Conn
	r  *resp.Reader
	w  *resp.Writer
}

func startServer(t *testing.T, o Options) (*Server, string) {
	t.Helper()
	return startServerOn(t, o, func(l net.Listener) net.Listener { return l })
}

func startOn(t *testing.T, o Options, wrap func(net.Listener) net.Listener) string {
	t.Helper()
	_, addr := startServerOn(t, o, wrap)
	return addr
}

func startServerOn(t *testing.T, o Options, wrap func(net.Listener) net.Listener) (*Server, string) {
	t.Helper()
	if o.Store == nil {
		db, err := shale.Open(t.TempDir(), shale.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		o.Store = db
	}
	if o.AuthDelay == 0 {
		o.AuthDelay = time.Millisecond
		o.AuthDelayMax = 5 * time.Millisecond
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	done := make(chan error, 1)
	go func() { done <- s.Serve(wrap(l)) }()
	t.Cleanup(func() {
		ctx, cancel := contextWithTimeout(5 * time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		if err := <-done; err != ErrServerClosed {
			t.Errorf("serve: %v", err)
		}
	})
	return s, addr
}

func dial(t *testing.T, addr string) *client {
	t.Helper()
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	return newClient(t, nc)
}

func newClient(t *testing.T, nc net.Conn) *client {
	return &client{t: t, nc: nc, r: resp.NewReader(nc, resp.Limits{}), w: resp.NewWriter(nc)}
}

func (c *client) send(args ...string) {
	c.t.Helper()
	items := make([]resp.Value, len(args))
	for i, a := range args {
		items[i] = resp.BulkString(a)
	}
	if err := c.w.WriteValue(resp.Array(items...)); err != nil {
		c.t.Fatal(err)
	}
	if err := c.w.Flush(); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) read() resp.Value {
	c.t.Helper()
	_ = c.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	v, err := c.r.ReadValue()
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return v
}

func (c *client) do(args ...string) resp.Value {
	c.t.Helper()
	c.send(args...)
	return c.read()
}

func (c *client) closed() bool {
	_ = c.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := c.r.ReadValue()
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return false
	}
	return err != nil
}

func text(v resp.Value) string {
	switch v.Kind {
	case resp.KindNull:
		return "<nil>"
	case resp.KindInt:
		return "int:" + string(rune('0'+v.Int))
	}
	return string(v.Bytes)
}

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	return listenLocalOn(t, "127.0.0.1:0")
}

func listenLocalOn(t *testing.T, addr string) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
