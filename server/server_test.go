// SPDX-License-Identifier: MIT

package server

import (
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thalesdev/sediment/resp"
)

func wantSimple(t *testing.T, v resp.Value, want string) {
	t.Helper()
	if v.Kind != resp.KindSimple || string(v.Bytes) != want {
		t.Fatalf("got kind %d %q, want +%s", v.Kind, v.Bytes, want)
	}
}

func wantBulk(t *testing.T, v resp.Value, want string) {
	t.Helper()
	if v.Kind != resp.KindBulk || string(v.Bytes) != want {
		t.Fatalf("got kind %d %q, want $%s", v.Kind, v.Bytes, want)
	}
}

func wantNull(t *testing.T, v resp.Value) {
	t.Helper()
	if v.Kind != resp.KindNull {
		t.Fatalf("got kind %d %q, want null", v.Kind, v.Bytes)
	}
}

func wantInt(t *testing.T, v resp.Value, want int64) {
	t.Helper()
	if v.Kind != resp.KindInt || v.Int != want {
		t.Fatalf("got kind %d %d, want :%d", v.Kind, v.Int, want)
	}
}

func wantError(t *testing.T, v resp.Value, prefix string) {
	t.Helper()
	if v.Kind != resp.KindError || !strings.HasPrefix(string(v.Bytes), prefix) {
		t.Fatalf("got kind %d %q, want -%s", v.Kind, v.Bytes, prefix)
	}
}

func TestCommands(t *testing.T) {
	_, addr := startServer(t, Options{})
	c := dial(t, addr)

	t.Run("Should answer PING and ECHO", func(t *testing.T) {
		wantSimple(t, c.do("PING"), "PONG")
		wantBulk(t, c.do("ping", "hi"), "hi")
		wantBulk(t, c.do("ECHO", "a\r\nb"), "a\r\nb")
	})

	t.Run("Should SET and GET and DEL", func(t *testing.T) {
		wantNull(t, c.do("GET", "k"))
		wantSimple(t, c.do("SET", "k", "v"), "OK")
		wantBulk(t, c.do("GET", "k"), "v")
		wantInt(t, c.do("DEL", "k"), 1)
		wantInt(t, c.do("DEL", "k"), 0)
		wantNull(t, c.do("GET", "k"))
	})

	t.Run("Should keep binary values and empty ones", func(t *testing.T) {
		wantSimple(t, c.do("SET", "bin", "\x00\xff\r\n"), "OK")
		wantBulk(t, c.do("GET", "bin"), "\x00\xff\r\n")
		wantSimple(t, c.do("SET", "empty", ""), "OK")
		wantBulk(t, c.do("GET", "empty"), "")
	})

	t.Run("Should honor NX XX and GET of SET", func(t *testing.T) {
		wantNull(t, c.do("SET", "n", "1", "XX"))
		wantSimple(t, c.do("SET", "n", "1", "NX"), "OK")
		wantNull(t, c.do("SET", "n", "2", "NX"))
		wantBulk(t, c.do("GET", "n"), "1")
		wantSimple(t, c.do("SET", "n", "3", "XX"), "OK")
		wantBulk(t, c.do("SET", "n", "4", "GET"), "3")
		wantNull(t, c.do("SET", "fresh", "x", "GET"))
		wantBulk(t, c.do("SET", "n", "5", "NX", "GET"), "4")
		wantBulk(t, c.do("GET", "n"), "4")
		wantError(t, c.do("SET", "n", "1", "NX", "XX"), "ERR syntax")
		wantError(t, c.do("SET", "n", "1", "BAD"), "ERR syntax")
	})

	t.Run("Should count EXISTS with repeats", func(t *testing.T) {
		c.do("SET", "e1", "x")
		wantInt(t, c.do("EXISTS", "e1", "e1", "nope"), 2)
	})

	t.Run("Should MSET and MGET", func(t *testing.T) {
		wantSimple(t, c.do("MSET", "m1", "a", "m2", "b", "m1", "c"), "OK")
		v := c.do("MGET", "m1", "missing", "m2")
		if v.Kind != resp.KindArray || len(v.Elems) != 3 {
			t.Fatalf("got %+v", v)
		}
		wantBulk(t, v.Elems[0], "c")
		wantNull(t, v.Elems[1])
		wantBulk(t, v.Elems[2], "b")
		wantError(t, c.do("MSET", "a", "b", "c"), "ERR wrong number")
	})

	t.Run("Should DEL many keys and count each once", func(t *testing.T) {
		c.do("MSET", "d1", "1", "d2", "2")
		wantInt(t, c.do("DEL", "d1", "d2", "d1", "d3"), 2)
	})

	t.Run("Should accept SELECT 0 only", func(t *testing.T) {
		wantSimple(t, c.do("SELECT", "0"), "OK")
		wantError(t, c.do("SELECT", "1"), "ERR DB index")
		wantError(t, c.do("SELECT", "x"), "ERR value is not an integer")
	})

	t.Run("Should reject unknown commands and wrong arity", func(t *testing.T) {
		wantError(t, c.do("NOPE", "a"), "ERR unknown command 'NOPE', with args beginning with: 'a'")
		wantError(t, c.do("GET"), "ERR wrong number of arguments for 'get'")
		wantError(t, c.do("GET", "a", "b"), "ERR wrong number of arguments for 'get'")
		wantSimple(t, c.do("PING"), "PONG")
	})

	t.Run("Should not echo bytes that are not printable", func(t *testing.T) {
		v := c.do("\x01\r\nbad")
		wantError(t, v, "ERR unknown command '??")
		if strings.ContainsAny(string(v.Bytes), "\r\n") {
			t.Fatalf("got %q", v.Bytes)
		}
	})

	t.Run("Should refuse a key above the limit", func(t *testing.T) {
		wantError(t, c.do("GET", strings.Repeat("k", 64<<10+1)), "ERR key is too large")
		wantSimple(t, c.do("SET", strings.Repeat("k", 64<<10), "v"), "OK")
	})

	t.Run("Should close after QUIT", func(t *testing.T) {
		q := dial(t, addr)
		wantSimple(t, q.do("QUIT"), "OK")
		if !q.closed() {
			t.Fatal("the connection is open")
		}
	})
}

func TestPipelining(t *testing.T) {
	_, addr := startServer(t, Options{})
	c := dial(t, addr)

	t.Run("Should answer a large pipeline in order", func(t *testing.T) {
		const n = 5000
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range n {
				c.send("SET", fmt.Sprintf("p%d", i), fmt.Sprintf("v%d", i))
			}
		}()
		for range n {
			wantSimple(t, c.read(), "OK")
		}
		wg.Wait()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range n {
				c.send("GET", fmt.Sprintf("p%d", i))
			}
		}()
		for i := range n {
			wantBulk(t, c.read(), fmt.Sprintf("v%d", i))
		}
		wg.Wait()
	})

	t.Run("Should answer commands that arrive in one write", func(t *testing.T) {
		_, err := c.nc.Write([]byte("*1\r\n$4\r\nPING\r\nPING\r\n*2\r\n$4\r\nECHO\r\n$2\r\nhi\r\n"))
		if err != nil {
			t.Fatal(err)
		}
		wantSimple(t, c.read(), "PONG")
		wantSimple(t, c.read(), "PONG")
		wantBulk(t, c.read(), "hi")
	})

	t.Run("Should flush the replies when a command arrives in pieces", func(t *testing.T) {
		c.send("PING")
		_, _ = c.nc.Write([]byte("*2\r\n$4\r\nECHO\r\n$"))
		wantSimple(t, c.read(), "PONG")
		_, _ = c.nc.Write([]byte("1\r\nx\r\n"))
		wantBulk(t, c.read(), "x")
	})
}

func TestConcurrentClients(t *testing.T) {
	_, addr := startServer(t, Options{})
	t.Run("Should serve many clients at the same time", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := range 32 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c := dial(t, addr)
				for j := range 100 {
					k, v := fmt.Sprintf("c%d-%d", i, j), fmt.Sprint(j)
					if r := c.do("SET", k, v); r.Kind != resp.KindSimple {
						t.Errorf("set: %+v", r)
						return
					}
					if r := c.do("GET", k); string(r.Bytes) != v {
						t.Errorf("get: %+v", r)
						return
					}
				}
			}()
		}
		wg.Wait()
	})

	t.Run("Should make SET NX atomic", func(t *testing.T) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		won := 0
		for range 32 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c := dial(t, addr)
				if c.do("SET", "race", "x", "NX").Kind == resp.KindSimple {
					mu.Lock()
					won++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if won != 1 {
			t.Fatalf("%d clients won", won)
		}
	})
}

func TestProtocolErrors(t *testing.T) {
	_, addr := startServer(t, Options{})

	t.Run("Should reply and close on a protocol error", func(t *testing.T) {
		for _, in := range []string{"*x\r\n", "*1\r\n:1\r\n", "*1\r\n$99999999999\r\n", "*2000\r\n", "*1\r\n$3\r\nabcde\r\n"} {
			c := dial(t, addr)
			_, _ = c.nc.Write([]byte(in))
			wantError(t, c.read(), "ERR Protocol error")
			if !c.closed() {
				t.Fatalf("%q: the connection is open", in)
			}
		}
	})

	t.Run("Should close without a reply on an HTTP request", func(t *testing.T) {
		c := dial(t, addr)
		_, _ = c.nc.Write([]byte("POST / HTTP/1.1\r\nHost: x\r\n\r\n"))
		_ = c.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
		b, err := io.ReadAll(c.nc)
		if err != nil || len(b) != 0 {
			t.Fatalf("got %q, %v", b, err)
		}
	})

	t.Run("Should accept inline commands", func(t *testing.T) {
		c := dial(t, addr)
		_, _ = c.nc.Write([]byte("PING\r\n"))
		wantSimple(t, c.read(), "PONG")
	})

	t.Run("Should survive a client that sends garbage", func(t *testing.T) {
		c := dial(t, addr)
		_, _ = c.nc.Write([]byte("\x00\x01\x02\xff\xfe"))
		_ = c.nc.Close()
		d := dial(t, addr)
		wantSimple(t, d.do("PING"), "PONG")
	})
}

func TestLimits(t *testing.T) {
	t.Run("Should close a client that is too slow to send a command", func(t *testing.T) {
		_, addr := startServer(t, Options{ReadTimeout: 200 * time.Millisecond})
		c := dial(t, addr)
		_, _ = c.nc.Write([]byte("*1\r\n$4\r\nPI"))
		if !c.closed() {
			t.Fatal("the connection is open")
		}
		wantSimple(t, dial(t, addr).do("PING"), "PONG")
	})

	t.Run("Should close an idle client", func(t *testing.T) {
		_, addr := startServer(t, Options{IdleTimeout: 200 * time.Millisecond})
		c := dial(t, addr)
		if !c.closed() {
			t.Fatal("the connection is open")
		}
	})

	t.Run("Should refuse clients above the limit", func(t *testing.T) {
		_, addr := startServer(t, Options{MaxClients: 2})
		a, b := dial(t, addr), dial(t, addr)
		wantSimple(t, a.do("PING"), "PONG")
		wantSimple(t, b.do("PING"), "PONG")
		c := dial(t, addr)
		wantError(t, c.read(), "ERR max number of clients")
		a.nc.Close()
		deadline := time.Now().Add(5 * time.Second)
		for {
			d := dial(t, addr)
			d.send("PING")
			if v := d.read(); v.Kind == resp.KindSimple {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the slot was not freed")
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	t.Run("Should refuse a reply above the limit", func(t *testing.T) {
		_, addr := startServer(t, Options{MaxReply: 100})
		c := dial(t, addr)
		wantSimple(t, c.do("MSET", "a", strings.Repeat("x", 60), "b", strings.Repeat("y", 60)), "OK")
		wantError(t, c.do("MGET", "a", "b"), "ERR reply is too large")
		wantBulk(t, c.do("GET", "a"), strings.Repeat("x", 60))
	})

	t.Run("Should close a client that does not read", func(t *testing.T) {
		_, addr := startServer(t, Options{WriteTimeout: 200 * time.Millisecond})
		c := dial(t, addr)
		wantSimple(t, c.do("SET", "big", strings.Repeat("z", 1<<20)), "OK")
		if tc, ok := c.nc.(*net.TCPConn); ok {
			_ = tc.SetReadBuffer(4096)
		}
		for range 200 {
			c.send("GET", "big")
		}
		time.Sleep(time.Second)
		d := dial(t, addr)
		wantSimple(t, d.do("PING"), "PONG")
	})
}

func TestShutdown(t *testing.T) {
	t.Run("Should stop the server and close the clients", func(t *testing.T) {
		s, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("PING"), "PONG")
		ctx, cancel := contextWithTimeout(5 * time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		if !c.closed() {
			t.Fatal("the connection is open")
		}
		if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			t.Fatal("the listener is open")
		}
	})
}
