// SPDX-License-Identifier: MIT

package server

import (
	"strings"
	"testing"

	"github.com/thalesdev/sediment/resp"
)

func pairs(t *testing.T, v resp.Value) map[string]string {
	t.Helper()
	if (v.Kind != resp.KindMap && v.Kind != resp.KindArray) || len(v.Elems)%2 != 0 {
		t.Fatalf("got kind %d with %d elements", v.Kind, len(v.Elems))
	}
	m := map[string]string{}
	for i := 0; i < len(v.Elems); i += 2 {
		m[string(v.Elems[i].Bytes)] = string(v.Elems[i+1].Bytes)
	}
	return m
}

func TestHello(t *testing.T) {
	_, addr := startServer(t, Options{Version: "9.9.9"})

	t.Run("Should start in RESP2 and answer HELLO as a flat array", func(t *testing.T) {
		c := dial(t, addr)
		v := c.do("HELLO")
		if v.Kind != resp.KindArray {
			t.Fatalf("got kind %d", v.Kind)
		}
		m := pairs(t, v)
		if m["server"] != "sediment" || m["version"] != "9.9.9" {
			t.Fatalf("got %v", m)
		}
	})

	t.Run("Should switch to RESP3 and back", func(t *testing.T) {
		c := dial(t, addr)
		v := c.do("HELLO", "3")
		if v.Kind != resp.KindMap {
			t.Fatalf("got kind %d", v.Kind)
		}
		wantNull(t, c.do("GET", "missing3"))
		raw := make([]byte, 0, 16)
		_, _ = c.nc.Write([]byte("*2\r\n$3\r\nGET\r\n$8\r\nmissing3\r\n"))
		buf := make([]byte, 3)
		n, _ := c.nc.Read(buf)
		raw = append(raw, buf[:n]...)
		if string(raw) != "_\r\n" {
			t.Fatalf("got %q", raw)
		}
		v = c.do("HELLO", "2")
		if v.Kind != resp.KindArray {
			t.Fatalf("got kind %d", v.Kind)
		}
	})

	t.Run("Should refuse an unknown protocol version", func(t *testing.T) {
		c := dial(t, addr)
		wantError(t, c.do("HELLO", "4"), "NOPROTO")
		wantError(t, c.do("HELLO", "x"), "ERR Protocol version")
		wantError(t, c.do("HELLO", "3", "BAD"), "ERR Syntax error in HELLO option 'BAD'")
		wantError(t, c.do("HELLO", "3", "AUTH", "u"), "ERR syntax")
		wantSimple(t, c.do("PING"), "PONG")
	})

	t.Run("Should set the name with HELLO SETNAME", func(t *testing.T) {
		c := dial(t, addr)
		c.do("HELLO", "3", "SETNAME", "app")
		wantBulk(t, c.do("CLIENT", "GETNAME"), "app")
		wantError(t, c.do("HELLO", "3", "SETNAME", "a b"), "ERR Client names")
	})

	t.Run("Should refuse AUTH when there are no users", func(t *testing.T) {
		c := dial(t, addr)
		wantError(t, c.do("AUTH", "secret"), "ERR AUTH called without")
	})
}

func TestClient(t *testing.T) {
	_, addr := startServer(t, Options{})
	c := dial(t, addr)

	t.Run("Should give each client an id", func(t *testing.T) {
		a := c.do("CLIENT", "ID")
		b := dial(t, addr).do("CLIENT", "ID")
		if a.Kind != resp.KindInt || b.Kind != resp.KindInt || a.Int == b.Int {
			t.Fatalf("got %d and %d", a.Int, b.Int)
		}
	})

	t.Run("Should set and get the name", func(t *testing.T) {
		wantNull(t, c.do("CLIENT", "GETNAME"))
		wantSimple(t, c.do("CLIENT", "SETNAME", "worker-1"), "OK")
		wantBulk(t, c.do("CLIENT", "GETNAME"), "worker-1")
		wantError(t, c.do("CLIENT", "SETNAME", "bad name"), "ERR Client names")
	})

	t.Run("Should accept SETINFO and show it in INFO", func(t *testing.T) {
		wantSimple(t, c.do("CLIENT", "SETINFO", "LIB-NAME", "go-redis"), "OK")
		wantSimple(t, c.do("CLIENT", "SETINFO", "LIB-VER", "9.0"), "OK")
		wantError(t, c.do("CLIENT", "SETINFO", "LIB-NAME", "a b"), "ERR lib-name")
		wantError(t, c.do("CLIENT", "SETINFO", "OTHER", "x"), "ERR Unrecognized option")
		v := c.do("CLIENT", "INFO")
		if !strings.Contains(string(v.Bytes), "lib-name=go-redis") || !strings.Contains(string(v.Bytes), "lib-ver=9.0") {
			t.Fatalf("got %q", v.Bytes)
		}
	})

	t.Run("Should refuse an unknown subcommand", func(t *testing.T) {
		wantError(t, c.do("CLIENT", "PAUSE"), "ERR unknown subcommand 'PAUSE'")
	})
}

func TestMeta(t *testing.T) {
	_, addr := startServer(t, Options{Settings: map[string]string{"port": "6380"}})
	c := dial(t, addr)

	t.Run("Should answer the COMMAND family", func(t *testing.T) {
		count := c.do("COMMAND", "COUNT")
		if count.Kind != resp.KindInt || count.Int < 10 {
			t.Fatalf("got %+v", count)
		}
		all := c.do("COMMAND")
		if all.Kind != resp.KindArray || int64(len(all.Elems)) != count.Int {
			t.Fatalf("got %d entries", len(all.Elems))
		}
		info := c.do("COMMAND", "INFO", "get", "nope")
		if len(info.Elems) != 2 || string(info.Elems[0].Elems[0].Bytes) != "get" || info.Elems[1].Kind != resp.KindNull {
			t.Fatalf("got %+v", info)
		}
		if v := c.do("COMMAND", "DOCS"); v.Kind != resp.KindArray || len(v.Elems) != 0 {
			t.Fatalf("got %+v", v)
		}
		if v := c.do("COMMAND", "LIST"); len(v.Elems) != int(count.Int) {
			t.Fatalf("got %d", len(v.Elems))
		}
	})

	t.Run("Should report INFO sections", func(t *testing.T) {
		c.do("SET", "info-key", "v")
		all := string(c.do("INFO").Bytes)
		for _, want := range []string{"# Server", "# Clients", "# Stats", "# Shale", "# Runtime", "total_commands_processed:", "connected_clients:", "gomaxprocs:", "sched_latency_p99_ms:", "write_stalls_flush:", "flush_ms:", "compaction_ms:"} {
			if !strings.Contains(all, want) {
				t.Fatalf("INFO has no %q:\n%s", want, all)
			}
		}
		one := string(c.do("INFO", "clients").Bytes)
		if !strings.Contains(one, "# Clients") || strings.Contains(one, "# Stats") {
			t.Fatalf("got %q", one)
		}
	})

	t.Run("Should answer CONFIG GET and refuse CONFIG SET", func(t *testing.T) {
		m := pairs(t, c.do("CONFIG", "GET", "save", "appendonly", "port"))
		if m["save"] != "" || m["appendonly"] != "no" || m["port"] != "6380" {
			t.Fatalf("got %v", m)
		}
		if _, ok := pairs(t, c.do("CONFIG", "GET", "*"))["maxclients"]; !ok {
			t.Fatal("no maxclients")
		}
		if v := c.do("CONFIG", "GET", "nothing-like-this"); len(v.Elems) != 0 {
			t.Fatalf("got %+v", v)
		}
		wantError(t, c.do("CONFIG", "SET", "dir", "/tmp"), "ERR unknown subcommand or not supported")
		wantError(t, c.do("CONFIG", "GET"), "ERR wrong number")
	})
}
