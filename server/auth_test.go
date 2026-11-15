// SPDX-License-Identifier: MIT

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

func aclFile(t *testing.T, text string) (*acl.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.acl")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := mustLoad(t, path)
	return reg, path
}

func mustLoad(t *testing.T, path string) *acl.Registry {
	t.Helper()
	reg, _, err := acl.Load(path, acl.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func usersFile(t *testing.T) string {
	t.Helper()
	app, err := acl.HashPassword([]byte("app-pass"))
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := acl.HashPassword([]byte("admin-pass"))
	reader, _ := acl.HashPassword([]byte("reader-pass"))
	return "user default off\n" +
		"user app on " + app + " ~* +@read +@write +@connection -@dangerous\n" +
		"user reader on " + reader + " ~* +@read +@connection\n" +
		"user admin on " + admin + " ~* +@all\n"
}

func TestAuth(t *testing.T) {
	reg, path := aclFile(t, usersFile(t))
	_, addr := startServer(t, Options{Access: reg, MaxAuthFailures: 3})

	t.Run("Should refuse commands before sign in", func(t *testing.T) {
		c := dial(t, addr)
		wantError(t, c.do("GET", "k"), "NOAUTH")
		wantError(t, c.do("SET", "k", "v"), "NOAUTH")
		wantError(t, c.do("PING"), "NOAUTH")
		wantError(t, c.do("INFO"), "NOAUTH")
		wantError(t, c.do("CONFIG", "GET", "*"), "NOAUTH")
		wantError(t, c.do("HELLO", "3"), "NOAUTH HELLO")
	})

	t.Run("Should sign in with AUTH user password", func(t *testing.T) {
		c := dial(t, addr)
		wantSimple(t, c.do("AUTH", "app", "app-pass"), "OK")
		wantSimple(t, c.do("SET", "k", "v"), "OK")
		wantBulk(t, c.do("GET", "k"), "v")
		if v := c.do("CLIENT", "INFO"); !strings.Contains(string(v.Bytes), "user=app") {
			t.Fatalf("got %q", v.Bytes)
		}
	})

	t.Run("Should sign in with HELLO AUTH", func(t *testing.T) {
		c := dial(t, addr)
		v := c.do("HELLO", "3", "AUTH", "app", "app-pass")
		if v.Kind != resp.KindMap {
			t.Fatalf("got %+v", v)
		}
		wantSimple(t, c.do("SET", "k2", "v"), "OK")
	})

	t.Run("Should refuse a wrong password and an unknown user the same way", func(t *testing.T) {
		for _, args := range [][]string{{"AUTH", "app", "nope"}, {"AUTH", "nobody", "x"}, {"AUTH", "x"}, {"AUTH", "default", "x"}} {
			c := dial(t, addr)
			wantError(t, c.do(args...), "WRONGPASS")
			wantError(t, c.do("GET", "k"), "NOAUTH")
		}
	})

	t.Run("Should close the connection after too many failures", func(t *testing.T) {
		c := dial(t, addr)
		wantError(t, c.do("AUTH", "app", "1"), "WRONGPASS")
		wantError(t, c.do("AUTH", "app", "2"), "WRONGPASS")
		wantError(t, c.do("AUTH", "app", "3"), "WRONGPASS")
		if !c.closed() {
			t.Fatal("the connection is open")
		}
	})

	t.Run("Should refuse a command that the user cannot run", func(t *testing.T) {
		c := dial(t, addr)
		wantSimple(t, c.do("AUTH", "reader", "reader-pass"), "OK")
		wantBulk(t, c.do("GET", "k"), "v")
		wantError(t, c.do("SET", "k", "x"), "NOPERM User reader has no permissions to run the 'set' command")
		wantError(t, c.do("DEL", "k"), "NOPERM")
		wantError(t, c.do("INFO"), "NOPERM")
		wantError(t, c.do("CONFIG", "GET", "*"), "NOPERM")
		wantError(t, c.do("ACL", "LIST"), "NOPERM")
		wantBulk(t, c.do("GET", "k"), "v")
	})

	t.Run("Should deny the dangerous commands to a user without them", func(t *testing.T) {
		c := dial(t, addr)
		wantSimple(t, c.do("AUTH", "app", "app-pass"), "OK")
		wantError(t, c.do("CONFIG", "GET", "*"), "NOPERM")
		wantError(t, c.do("INFO"), "NOPERM")
		wantError(t, c.do("ACL", "LOAD"), "NOPERM")
	})

	t.Run("Should let an admin manage and list users", func(t *testing.T) {
		c := dial(t, addr)
		wantSimple(t, c.do("AUTH", "admin", "admin-pass"), "OK")
		users := c.do("ACL", "USERS")
		if len(users.Elems) != 4 {
			t.Fatalf("got %+v", users)
		}
		list := c.do("ACL", "LIST")
		for _, e := range list.Elems {
			if strings.Contains(string(e.Bytes), "app-pass") {
				t.Fatalf("a password is in %q", e.Bytes)
			}
		}
		if v := c.do("ACL", "CAT"); len(v.Elems) < 5 {
			t.Fatalf("got %+v", v)
		}
		wantSimple(t, c.do("ACL", "LOAD"), "OK")
	})

	t.Run("Should apply a reload to clients that are already in", func(t *testing.T) {
		c := dial(t, addr)
		wantSimple(t, c.do("AUTH", "app", "app-pass"), "OK")
		wantSimple(t, c.do("SET", "x", "1"), "OK")
		app, _ := acl.HashPassword([]byte("app-pass"))
		admin, _ := acl.HashPassword([]byte("admin-pass"))
		text := "user default off\nuser app off " + app + " +@all\nuser admin on " + admin + " ~* +@all\n"
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		a := dial(t, addr)
		wantSimple(t, a.do("AUTH", "admin", "admin-pass"), "OK")
		wantSimple(t, a.do("ACL", "LOAD"), "OK")
		wantError(t, c.do("SET", "x", "2"), "NOPERM")
		wantError(t, dial(t, addr).do("AUTH", "app", "app-pass"), "WRONGPASS")
	})

	t.Run("Should keep the users when a reload fails", func(t *testing.T) {
		a := dial(t, addr)
		wantSimple(t, a.do("AUTH", "admin", "admin-pass"), "OK")
		if err := os.WriteFile(path, []byte("user admin on nopass ~secret:* +@all\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		v := a.do("ACL", "LOAD")
		wantError(t, v, "ERR the ACL file was not loaded")
		if strings.Contains(string(v.Bytes), path) {
			t.Fatalf("the path is in %q", v.Bytes)
		}
		wantSimple(t, a.do("PING"), "PONG")
	})
}

func TestPreAuthLimits(t *testing.T) {
	reg, _ := aclFile(t, usersFile(t))
	_, addr := startServer(t, Options{Access: reg})

	t.Run("Should refuse a large value before sign in", func(t *testing.T) {
		c := dial(t, addr)
		_, _ = c.nc.Write([]byte("*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$4000\r\n"))
		wantError(t, c.read(), "ERR Protocol error: invalid bulk length")
		if !c.closed() {
			t.Fatal("the connection is open")
		}
	})

	t.Run("Should refuse many arguments before sign in", func(t *testing.T) {
		c := dial(t, addr)
		_, _ = c.nc.Write([]byte("*100\r\n"))
		wantError(t, c.read(), "ERR Protocol error: invalid multibulk length")
	})

	t.Run("Should allow large values after sign in", func(t *testing.T) {
		c := dial(t, addr)
		wantSimple(t, c.do("AUTH", "app", "app-pass"), "OK")
		big := strings.Repeat("v", 1<<20)
		wantSimple(t, c.do("SET", "big", big), "OK")
		wantBulk(t, c.do("GET", "big"), big)
	})

	t.Run("Should close a client that does not sign in", func(t *testing.T) {
		_, addr := startServer(t, Options{Access: reg, PreAuthTimeout: 200 * time.Millisecond})
		c := dial(t, addr)
		if !c.closed() {
			t.Fatal("the connection is open")
		}
	})
}

func TestNoAccess(t *testing.T) {
	t.Run("Should let anyone in when there are no users", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("SET", "k", "v"), "OK")
		wantError(t, c.do("ACL", "LIST"), "ERR this server has no ACL file")
		wantBulk(t, c.do("ACL", "WHOAMI"), "default")
	})

	t.Run("Should let anyone in when default is on with no password", func(t *testing.T) {
		reg, _ := aclFile(t, "user default on nopass +@read +@write +@connection\n")
		_, addr := startServer(t, Options{Access: reg})
		c := dial(t, addr)
		wantSimple(t, c.do("SET", "k", "v"), "OK")
		wantError(t, c.do("CONFIG", "GET", "*"), "NOPERM")
	})
}
