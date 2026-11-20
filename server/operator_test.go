// SPDX-License-Identifier: MIT

package server

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClientList(t *testing.T) {
	t.Run("Should list the clients with their name, user and last command", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		a := dial(t, addr)
		b := dial(t, addr)
		a.do("CLIENT", "SETNAME", "alpha")
		b.do("PING")
		got := text(a.do("CLIENT", "LIST"))
		lines := strings.Split(strings.TrimSpace(got), "\n")
		if len(lines) != 2 {
			t.Fatalf("got %q", got)
		}
		if !strings.Contains(lines[0], "name=alpha") || !strings.Contains(lines[0], "cmd=client") || !strings.Contains(lines[1], "cmd=ping") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("Should filter by id", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		a := dial(t, addr)
		dial(t, addr).do("PING")
		id := a.do("CLIENT", "ID").Int
		got := text(a.do("CLIENT", "LIST", "ID", itoaTest(id)))
		if strings.Count(got, "\n") != 1 || !strings.HasPrefix(got, "id="+itoaTest(id)+" ") {
			t.Fatalf("got %q", got)
		}
	})
}

func itoaTest(n int64) string {
	return strconv.FormatInt(n, 10)
}

func TestClientKill(t *testing.T) {
	t.Run("Should close the client with the id and answer the number of closed clients", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		a := dial(t, addr)
		b := dial(t, addr)
		id := b.do("CLIENT", "ID").Int
		if v := a.do("CLIENT", "KILL", "ID", itoaTest(id)); v.Int != 1 {
			t.Fatalf("got %+v", v)
		}
		if !b.closed() {
			t.Fatal("the client is still connected")
		}
		if v := a.do("PING"); text(v) != "PONG" {
			t.Fatalf("got %+v", v)
		}
	})
	t.Run("Should skip the caller by default and close it with SKIPME no", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		a := dial(t, addr)
		id := a.do("CLIENT", "ID").Int
		if v := a.do("CLIENT", "KILL", "ID", itoaTest(id)); v.Int != 0 {
			t.Fatalf("got %+v", v)
		}
		if v := a.do("CLIENT", "KILL", "ID", itoaTest(id), "SKIPME", "no"); v.Int != 1 {
			t.Fatalf("got %+v", v)
		}
		if !a.closed() {
			t.Fatal("the caller is still connected")
		}
	})
	t.Run("Should kill by the old form with the address", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		a := dial(t, addr)
		b := dial(t, addr)
		line := text(b.do("CLIENT", "INFO"))
		peer := strings.Fields(line)[1][len("addr="):]
		wantSimple(t, a.do("CLIENT", "KILL", peer), "OK")
		if !b.closed() {
			t.Fatal("the client is still connected")
		}
		wantError(t, a.do("CLIENT", "KILL", "127.0.0.1:1"), "ERR No such client")
	})
	t.Run("Should refuse a user that has no admin rights", func(t *testing.T) {
		reg, _ := aclFile(t, usersFile(t))
		_, addr := startServer(t, Options{Access: reg})
		c := dial(t, addr)
		wantSimple(t, c.do("AUTH", "app", "app-pass"), "OK")
		wantError(t, c.do("CLIENT", "LIST"), "NOPERM")
		wantError(t, c.do("CLIENT", "KILL", "ID", "1"), "NOPERM")
		wantSimple(t, c.do("AUTH", "admin", "admin-pass"), "OK")
		if got := text(c.do("CLIENT", "LIST")); !strings.Contains(got, "user=admin") {
			t.Fatalf("got %q", got)
		}
	})
}

func TestSlowLog(t *testing.T) {
	t.Run("Should keep the commands that took the threshold, with no argument", func(t *testing.T) {
		_, addr := startServer(t, Options{SlowLogThreshold: time.Nanosecond})
		c := dial(t, addr)
		c.do("SET", "secret-key", "secret-value")
		c.do("GET", "secret-key")
		if v := c.do("SLOWLOG", "LEN"); v.Int < 2 {
			t.Fatalf("got %+v", v)
		}
		v := c.do("SLOWLOG", "GET", "5")
		if len(v.Elems) == 0 || len(v.Elems[0].Elems) != 6 {
			t.Fatalf("got %+v", v)
		}
		var all strings.Builder
		for _, e := range v.Elems {
			for _, f := range e.Elems[3].Elems {
				all.Write(f.Bytes)
			}
		}
		if strings.Contains(all.String(), "secret") {
			t.Fatalf("an argument is in the log: %q", all.String())
		}
	})
	t.Run("Should reset the log", func(t *testing.T) {
		_, addr := startServer(t, Options{SlowLogThreshold: time.Nanosecond})
		c := dial(t, addr)
		c.do("PING")
		wantSimple(t, c.do("SLOWLOG", "RESET"), "OK")
		if v := c.do("SLOWLOG", "LEN"); v.Int != 1 {
			t.Fatalf("got %+v", v)
		}
	})
	t.Run("Should keep only the newest entries", func(t *testing.T) {
		_, addr := startServer(t, Options{SlowLogThreshold: time.Nanosecond, SlowLogMaxLen: 3})
		c := dial(t, addr)
		for range 10 {
			c.do("PING")
		}
		if v := c.do("SLOWLOG", "LEN"); v.Int != 3 {
			t.Fatalf("got %+v", v)
		}
	})
	t.Run("Should keep nothing when the threshold is negative", func(t *testing.T) {
		_, addr := startServer(t, Options{SlowLogThreshold: -1})
		c := dial(t, addr)
		c.do("PING")
		if v := c.do("SLOWLOG", "LEN"); v.Int != 0 {
			t.Fatalf("got %+v", v)
		}
	})
}

func TestAuthLimit(t *testing.T) {
	t.Run("Should refuse an address after too many failures, with the right password too", func(t *testing.T) {
		reg, _ := aclFile(t, usersFile(t))
		_, addr := startServer(t, Options{Access: reg, AuthFailLimit: 3, MaxAuthFailures: 100})
		for range 3 {
			c := dial(t, addr)
			wantError(t, c.do("AUTH", "app", "wrong"), "WRONGPASS")
		}
		c := dial(t, addr)
		wantError(t, c.do("AUTH", "app", "app-pass"), "ERR too many failed sign ins")
		if !c.closed() {
			t.Fatal("the connection is open")
		}
	})
	t.Run("Should have no limit when it is off", func(t *testing.T) {
		reg, _ := aclFile(t, usersFile(t))
		_, addr := startServer(t, Options{Access: reg, AuthFailLimit: -1, MaxAuthFailures: 100})
		for range 30 {
			wantError(t, dial(t, addr).do("AUTH", "app", "wrong"), "WRONGPASS")
		}
		wantSimple(t, dial(t, addr).do("AUTH", "app", "app-pass"), "OK")
	})
}

func TestAuthLimitWindow(t *testing.T) {
	t.Run("Should count by host and forget after the window", func(t *testing.T) {
		now := time.Unix(1000, 0)
		l := newAuthLimit(2, time.Minute, func() time.Time { return now })
		l.Failed("10.0.0.1:1000")
		l.Failed("10.0.0.1:2000")
		if !l.Blocked("10.0.0.1:3000") {
			t.Fatal("the host is not blocked")
		}
		if l.Blocked("10.0.0.2:1000") {
			t.Fatal("another host is blocked")
		}
		now = now.Add(time.Minute)
		if l.Blocked("10.0.0.1:3000") {
			t.Fatal("the host is still blocked after the window")
		}
	})
	t.Run("Should be nil with no limit", func(t *testing.T) {
		if newAuthLimit(0, time.Minute, nil) != nil {
			t.Fatal("got a limit")
		}
	})
}
