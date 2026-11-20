// SPDX-License-Identifier: MIT

package server

import (
	"testing"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/server/command"
)

func TestFlush(t *testing.T) {
	fill := func(c *client) {
		wantSimple(t, c.do("SET", "s", "v"), "OK")
		wantInt(t, c.do("HSET", "h", "a", "1", "b", "2"), 2)
		wantInt(t, c.do("SADD", "z", "x", "y"), 2)
		wantInt(t, c.do("DBSIZE"), 3)
	}
	t.Run("Should delete every key of every type, for FLUSHALL and for FLUSHDB", func(t *testing.T) {
		for _, name := range []string{"FLUSHALL", "FLUSHDB"} {
			_, addr := startServer(t, Options{})
			c := dial(t, addr)
			fill(c)
			wantSimple(t, c.do(name), "OK")
			wantInt(t, c.do("DBSIZE"), 0)
			wantNull(t, c.do("GET", "s"))
			wantInt(t, c.do("EXISTS", "h", "z"), 0)
			wantArrayLen(t, c.do("KEYS", "*"), 0)
		}
	})
	t.Run("Should accept ASYNC and SYNC and refuse another option", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("FLUSHALL", "ASYNC"), "OK")
		wantSimple(t, c.do("FLUSHDB", "sync"), "OK")
		wantError(t, c.do("FLUSHALL", "NOW"), "ERR syntax error")
		wantError(t, c.do("FLUSHALL", "ASYNC", "SYNC"), "ERR syntax error")
	})
	t.Run("Should leave no element behind for a key that is made again", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantInt(t, c.do("HSET", "h", "old", "1"), 1)
		wantSimple(t, c.do("FLUSHALL"), "OK")
		wantInt(t, c.do("HSET", "h", "new", "2"), 1)
		wantStrs(t, c.do("HGETALL", "h"), "new", "2")
		wantInt(t, c.do("SADD", "z", "m"), 1)
		wantStrs(t, c.do("SMEMBERS", "z"), "m")
	})
	t.Run("Should keep the format record, so the data directory still opens", func(t *testing.T) {
		db, err := shale.Open(t.TempDir(), shale.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err := command.EnsureFormat(db); err != nil {
			t.Fatal(err)
		}
		_, addr := startServer(t, Options{Store: db})
		c := dial(t, addr)
		fill(c)
		wantSimple(t, c.do("FLUSHALL"), "OK")
		if err := command.EnsureFormat(db); err != nil {
			t.Fatalf("the format record is gone: %v", err)
		}
	})
	t.Run("Should make a WATCH of any key fail", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		a, b := dial(t, addr), dial(t, addr)
		wantSimple(t, a.do("SET", "k", "1"), "OK")
		wantSimple(t, a.do("WATCH", "k", "other"), "OK")
		wantSimple(t, b.do("FLUSHALL"), "OK")
		wantSimple(t, a.do("MULTI"), "OK")
		wantQueued(t, a.do("SET", "k", "2"))
		wantNull(t, a.do("EXEC"))
		wantNull(t, a.do("GET", "k"))
	})
	t.Run("Should be refused inside a transaction", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("MULTI"), "OK")
		wantError(t, c.do("FLUSHALL"), "ERR Command not allowed inside a transaction")
		wantError(t, c.do("EXEC"), "EXECABORT")
	})
	t.Run("Should need the right to write every key", func(t *testing.T) {
		admin, _ := aclServerWith(t,
			"user default off\n"+
				"user wide on nopass ~* +@all\n"+
				"user narrow on nopass ~app:* +@all\n"+
				"user readall on nopass %R~* +@all\n")
		wantSimple(t, as(t, admin, "narrow", "x").do("SET", "app:1", "v"), "OK")
		wantError(t, as(t, admin, "narrow", "x").do("FLUSHALL"), "NOPERM")
		wantError(t, as(t, admin, "readall", "x").do("FLUSHDB"), "NOPERM")
		wantSimple(t, as(t, admin, "wide", "x").do("FLUSHALL"), "OK")
		wantNull(t, as(t, admin, "wide", "x").do("GET", "app:1"))
	})
}
