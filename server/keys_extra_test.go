// SPDX-License-Identifier: MIT

package server

import (
	"strings"
	"testing"
)

func TestCopy(t *testing.T) {
	t.Run("Should copy a string with its expiry, and not replace without REPLACE", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("SETEX", "a", "100", "v"), "OK")
		wantInt(t, c.do("COPY", "a", "b"), 1)
		wantBulk(t, c.do("GET", "b"), "v")
		if v := c.do("TTL", "b"); v.Int < 90 {
			t.Fatalf("got %+v", v)
		}
		wantSimple(t, c.do("SET", "c", "other"), "OK")
		wantInt(t, c.do("COPY", "a", "c"), 0)
		wantBulk(t, c.do("GET", "c"), "other")
		wantInt(t, c.do("COPY", "a", "c", "REPLACE"), 1)
		wantBulk(t, c.do("GET", "c"), "v")
		wantInt(t, c.do("COPY", "nope", "x"), 0)
		wantSimple(t, c.do("SET", "a", "changed"), "OK")
		wantBulk(t, c.do("GET", "b"), "v")
	})
	t.Run("Should copy a hash and a set to independent collections", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantInt(t, c.do("HSET", "h", "f1", "v1", "f2", "v2"), 2)
		wantInt(t, c.do("SADD", "s", "x", "y", "z"), 3)
		wantInt(t, c.do("COPY", "h", "h2"), 1)
		wantInt(t, c.do("COPY", "s", "s2"), 1)
		wantInt(t, c.do("HLEN", "h2"), 2)
		wantBulk(t, c.do("HGET", "h2", "f2"), "v2")
		wantInt(t, c.do("SCARD", "s2"), 3)
		wantInt(t, c.do("HSET", "h", "f3", "v3"), 1)
		wantInt(t, c.do("SREM", "s", "x"), 1)
		wantInt(t, c.do("HLEN", "h2"), 2)
		wantInt(t, c.do("SISMEMBER", "s2", "x"), 1)
		wantInt(t, c.do("DEL", "h"), 1)
		wantInt(t, c.do("HLEN", "h2"), 2)
		wantInt(t, c.do("COPY", "s", "h2", "REPLACE"), 1)
		wantError(t, c.do("HLEN", "h2"), "WRONGTYPE")
		wantInt(t, c.do("SCARD", "h2"), 2)
	})
	t.Run("Should refuse the same key, another database and a bad option", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("SET", "a", "v"), "OK")
		wantError(t, c.do("COPY", "a", "a"), "ERR source and destination objects are the same")
		wantError(t, c.do("COPY", "a", "b", "DB", "1"), "ERR DB index is out of range")
		wantInt(t, c.do("COPY", "a", "b", "DB", "0"), 1)
		wantError(t, c.do("COPY", "a", "c", "DB", "x"), "ERR value is not an integer")
		wantError(t, c.do("COPY", "a", "c", "WAT"), "ERR syntax error")
	})
}

func TestKeysExtra(t *testing.T) {
	t.Run("Should TOUCH like EXISTS and answer TIME as seconds and microseconds", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("MSET", "a", "1", "b", "2"), "OK")
		wantInt(t, c.do("TOUCH", "a", "b", "c", "a"), 3)
		v := c.do("TIME")
		if len(v.Elems) != 2 || len(text(v.Elems[0])) != 10 {
			t.Fatalf("got %+v", v)
		}
		if us := text(v.Elems[1]); len(us) == 0 || len(us) > 6 {
			t.Fatalf("microseconds %q", us)
		}
	})
	t.Run("Should answer OBJECT ENCODING, REFCOUNT and HELP, and say that it does not track FREQ and IDLETIME", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("SET", "n", "123"), "OK")
		wantSimple(t, c.do("SET", "s", "hello"), "OK")
		wantSimple(t, c.do("SET", "l", strings.Repeat("x", 100)), "OK")
		wantBulk(t, c.do("OBJECT", "ENCODING", "n"), "int")
		wantBulk(t, c.do("OBJECT", "ENCODING", "s"), "embstr")
		wantBulk(t, c.do("OBJECT", "ENCODING", "l"), "raw")
		wantInt(t, c.do("OBJECT", "REFCOUNT", "s"), 1)
		wantNull(t, c.do("OBJECT", "ENCODING", "nope"))
		wantError(t, c.do("OBJECT", "FREQ", "s"), "ERR An LFU maxmemory policy")
		wantError(t, c.do("OBJECT", "IDLETIME", "s"), "ERR the idle time")
		wantError(t, c.do("OBJECT", "WAT", "s"), "ERR unknown subcommand")
		wantInt(t, c.do("HSET", "h", "a", "1"), 1)
		wantBulk(t, c.do("OBJECT", "ENCODING", "h"), "hashtable")
		if v := c.do("OBJECT", "HELP"); len(v.Elems) < 3 || !strings.Contains(text(v.Elems[0]), "OBJECT <subcommand>") {
			t.Fatalf("got %+v", v)
		}
	})
	t.Run("Should RESET the connection and ask for a sign in again", func(t *testing.T) {
		reg, _ := aclFile(t, usersFile(t))
		_, addr := startServer(t, Options{Access: reg})
		c := dial(t, addr)
		wantSimple(t, c.do("AUTH", "admin", "admin-pass"), "OK")
		wantSimple(t, c.do("CLIENT", "SETNAME", "mine"), "OK")
		wantSimple(t, c.do("MULTI"), "OK")
		wantSimple(t, c.do("RESET"), "RESET")
		wantError(t, c.do("GET", "k"), "NOAUTH")
		wantSimple(t, c.do("AUTH", "admin", "admin-pass"), "OK")
		wantError(t, c.do("EXEC"), "ERR EXEC without MULTI")
		wantNull(t, c.do("CLIENT", "GETNAME"))
	})
	t.Run("Should RESET an open server to the default state", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("CLIENT", "SETNAME", "mine"), "OK")
		c.do("HELLO", "3")
		wantSimple(t, c.do("RESET"), "RESET")
		wantNull(t, c.do("CLIENT", "GETNAME"))
		wantSimple(t, c.do("SET", "k", "v"), "OK")
	})
}

func TestCommandKeys(t *testing.T) {
	t.Run("Should give the keys of a command and their flags", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantStrs(t, c.do("COMMAND", "GETKEYS", "get", "key"), "key")
		wantStrs(t, c.do("COMMAND", "GETKEYS", "mset", "k1", "v1", "k2", "v2"), "k1", "k2")
		wantStrs(t, c.do("COMMAND", "GETKEYS", "lcs", "key1", "key2"), "key1", "key2")
		wantStrs(t, c.do("COMMAND", "GETKEYS", "sintercard", "2", "a", "b", "LIMIT", "1"), "a", "b")
		wantStrs(t, c.do("COMMAND", "GETKEYS", "msetex", "2", "k1", "v1", "k2", "v2", "EX", "10"), "k1", "k2")
		v := c.do("COMMAND", "GETKEYSANDFLAGS", "set", "k1", "v1")
		if len(v.Elems) != 1 || text(v.Elems[0].Elems[0]) != "k1" || len(v.Elems[0].Elems[1].Elems) != 2 || text(v.Elems[0].Elems[1].Elems[0]) != "OW" {
			t.Fatalf("got %+v", v)
		}
		v = c.do("COMMAND", "GETKEYSANDFLAGS", "delex", "k1")
		if text(v.Elems[0].Elems[1].Elems[0]) != "RM" {
			t.Fatalf("got %+v", v)
		}
	})
	t.Run("Should refuse an unknown command, wrong arguments and a command with no keys", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantError(t, c.do("COMMAND", "GETKEYS", "nosuch", "k"), "ERR Invalid command specified")
		wantError(t, c.do("COMMAND", "GETKEYS", "get"), "ERR Invalid arguments specified")
		wantError(t, c.do("COMMAND", "GETKEYS", "ping"), "ERR The command has no key arguments")
		wantError(t, c.do("COMMAND", "GETKEYS"), "ERR wrong number of arguments")
		wantError(t, c.do("COMMAND", "GETKEYS", "msetex", "x", "k", "v"), "ERR Invalid arguments")
	})
	t.Run("Should mark the commands with a variable number of keys as movablekeys", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		info := c.do("COMMAND", "INFO", "sintercard", "get")
		if !strings.Contains(text(info.Elems[0].Elems[2].Elems[0])+text(info.Elems[0].Elems[2].Elems[len(info.Elems[0].Elems[2].Elems)-1]), "movablekeys") {
			t.Fatalf("got %+v", info.Elems[0].Elems[2])
		}
		for _, f := range info.Elems[1].Elems[2].Elems {
			if text(f) == "movablekeys" {
				t.Fatal("GET is marked movablekeys")
			}
		}
	})
}
