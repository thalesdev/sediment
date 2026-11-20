// SPDX-License-Identifier: MIT

package server

import (
	"testing"
)

func TestSetCardinality(t *testing.T) {
	setup := func(t *testing.T) *client {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantInt(t, c.do("SADD", "a", "1", "2", "3", "4"), 4)
		wantInt(t, c.do("SADD", "b", "3", "4", "5"), 3)
		wantInt(t, c.do("SADD", "c", "4", "9"), 2)
		return c
	}
	t.Run("Should count the intersection, the union and the difference", func(t *testing.T) {
		c := setup(t)
		wantInt(t, c.do("SINTERCARD", "2", "a", "b"), 2)
		wantInt(t, c.do("SINTERCARD", "3", "a", "b", "c"), 1)
		wantInt(t, c.do("SINTERCARD", "2", "a", "nope"), 0)
		wantInt(t, c.do("SUNIONCARD", "3", "a", "b", "c"), 6)
		wantInt(t, c.do("SUNIONCARD", "2", "a", "b", "APPROX"), 5)
		wantInt(t, c.do("SDIFFCARD", "2", "a", "b"), 2)
		wantInt(t, c.do("SDIFFCARD", "3", "a", "b", "c"), 2)
		wantInt(t, c.do("SDIFFCARD", "1", "nope"), 0)
	})
	t.Run("Should stop at LIMIT", func(t *testing.T) {
		c := setup(t)
		wantInt(t, c.do("SINTERCARD", "2", "a", "b", "LIMIT", "1"), 1)
		wantInt(t, c.do("SINTERCARD", "2", "a", "b", "LIMIT", "0"), 2)
		wantInt(t, c.do("SUNIONCARD", "3", "a", "b", "c", "LIMIT", "3"), 3)
		wantInt(t, c.do("SDIFFCARD", "2", "a", "b", "LIMIT", "1"), 1)
	})
	t.Run("Should refuse a bad count, extra arguments, a bad LIMIT and a wrong type", func(t *testing.T) {
		c := setup(t)
		for _, name := range []string{"SINTERCARD", "SUNIONCARD", "SDIFFCARD"} {
			wantError(t, c.do(name), "ERR wrong number of arguments")
			wantError(t, c.do(name, "1"), "ERR wrong number of arguments")
			wantError(t, c.do(name, "0", "a"), "ERR numkeys")
			wantError(t, c.do(name, "x", "a"), "ERR numkeys")
			wantError(t, c.do(name, "2", "a"), "ERR Number of keys")
			wantError(t, c.do(name, "1", "a", "b"), "ERR syntax error")
			wantError(t, c.do(name, "1", "a", "LIMIT"), "ERR syntax error")
			wantError(t, c.do(name, "1", "a", "LIMIT", "-1"), "ERR LIMIT")
			wantError(t, c.do(name, "1", "a", "LIMIT", "x"), "ERR LIMIT")
			wantSimple(t, c.do("SET", "str", "v"), "OK")
			wantError(t, c.do(name, "2", "a", "str"), "WRONGTYPE")
		}
		wantError(t, c.do("SINTERCARD", "1", "a", "APPROX"), "ERR syntax error")
	})
}

func TestSmove(t *testing.T) {
	t.Run("Should move a member in one write and remove a source that becomes empty", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantInt(t, c.do("SADD", "src", "a", "b"), 2)
		wantInt(t, c.do("SMOVE", "src", "dst", "a"), 1)
		wantInt(t, c.do("SISMEMBER", "dst", "a"), 1)
		wantInt(t, c.do("SCARD", "src"), 1)
		wantInt(t, c.do("SMOVE", "src", "dst", "a"), 0)
		wantInt(t, c.do("SMOVE", "src", "dst", "b"), 1)
		wantInt(t, c.do("EXISTS", "src"), 0)
		wantInt(t, c.do("SCARD", "dst"), 2)
	})
	t.Run("Should not duplicate a member that the destination has, and do nothing for the same key", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantInt(t, c.do("SADD", "src", "a", "b"), 2)
		wantInt(t, c.do("SADD", "dst", "a"), 1)
		wantInt(t, c.do("SMOVE", "src", "dst", "a"), 1)
		wantInt(t, c.do("SCARD", "dst"), 1)
		wantInt(t, c.do("SCARD", "src"), 1)
		wantInt(t, c.do("SMOVE", "src", "src", "b"), 1)
		wantInt(t, c.do("SMOVE", "src", "src", "zz"), 0)
		wantInt(t, c.do("SCARD", "src"), 1)
	})
	t.Run("Should refuse a key of another type and move nothing", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantInt(t, c.do("SADD", "src", "a"), 1)
		wantSimple(t, c.do("SET", "str", "v"), "OK")
		wantError(t, c.do("SMOVE", "src", "str", "a"), "WRONGTYPE")
		wantError(t, c.do("SMOVE", "str", "src", "a"), "WRONGTYPE")
		wantInt(t, c.do("SISMEMBER", "src", "a"), 1)
	})
}

func TestVariableKeysAndACL(t *testing.T) {
	t.Run("Should check every key of SINTERCARD and MSETEX against the patterns of the user", func(t *testing.T) {
		addr, _ := aclServerWith(t, "user default off\nuser app on nopass ~app:* +@all\n")
		c := as(t, addr, "app", "x")
		wantInt(t, c.do("SADD", "app:a", "1"), 1)
		wantInt(t, c.do("SINTERCARD", "1", "app:a"), 1)
		wantError(t, c.do("SINTERCARD", "2", "app:a", "other"), "NOPERM")
		wantError(t, c.do("SUNIONCARD", "2", "app:a", "other"), "NOPERM")
		wantInt(t, c.do("MSETEX", "2", "app:1", "v", "app:2", "w", "EX", "100"), 1)
		wantError(t, c.do("MSETEX", "2", "app:1", "v", "other", "w", "EX", "100"), "NOPERM")
		wantError(t, c.do("SMOVE", "app:a", "other", "1"), "NOPERM")
	})
}

func TestSrandmemberOverflow(t *testing.T) {
	t.Run("Should answer an error and keep the connection for the smallest count", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantInt(t, c.do("SADD", "s", "a"), 1)
		wantError(t, c.do("SRANDMEMBER", "s", "-9223372036854775808"), "ERR value is out of range")
		wantInt(t, c.do("SCARD", "s"), 1)
		wantArrayLen(t, c.do("SRANDMEMBER", "nokey", "100"), 0)
		wantArrayLen(t, c.do("SRANDMEMBER", "s", "0"), 0)
	})
}
