// SPDX-License-Identifier: MIT

package server

import (
	"crypto/tls"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thalesdev/sediment/server/acl"
	"github.com/thalesdev/sediment/server/command"
)

func aclServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	admin, _ := acl.HashPassword([]byte("admin-pass"))
	text := "user default off\n" +
		"user admin on " + admin + " ~* +@all\n" +
		"user app on nopass ~app:* +@read +@write +@connection +@transaction -@dangerous\n" +
		"user ro on nopass %R~pub:* +@read +@connection\n"
	addr, path := aclServerWith(t, text)
	return nil, addr, path
}

func aclServerWith(t *testing.T, text string) (string, string) {
	t.Helper()
	_, path := aclFile(t, text)
	known := command.NewRegistry()
	reg, _, err := acl.Load(path, acl.ParseOptions{
		KnownCommand: func(n string) bool { return known.Lookup([]byte(n)) != nil },
		KnownSub: func(n, sub string) bool {
			spec := known.Lookup([]byte(n))
			return spec != nil && slices.Contains(spec.Subs, sub)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, addr := startServer(t, Options{Access: reg, AuthFailLimit: -1})
	return addr, path
}

func as(t *testing.T, addr, user, pass string) *client {
	t.Helper()
	c := dial(t, addr)
	wantSimple(t, c.do("AUTH", user, pass), "OK")
	return c
}

func TestKeyPatternsInServer(t *testing.T) {
	t.Run("Should refuse a key outside the patterns and name no key in the error", func(t *testing.T) {
		_, addr, _ := aclServer(t)
		c := as(t, addr, "app", "x")
		wantSimple(t, c.do("SET", "app:1", "v"), "OK")
		v := c.do("SET", "secret-key", "v")
		wantError(t, v, "NOPERM No permissions to access a key")
		if strings.Contains(string(v.Bytes), "secret-key") {
			t.Fatalf("the key is in %q", v.Bytes)
		}
		wantError(t, c.do("MGET", "app:1", "other"), "NOPERM")
		wantError(t, c.do("MSET", "app:1", "a", "other", "b"), "NOPERM")
		wantError(t, c.do("RENAME", "app:1", "other"), "NOPERM")
	})
	t.Run("Should split read and write", func(t *testing.T) {
		_, addr, _ := aclServer(t)
		adm := as(t, addr, "admin", "admin-pass")
		wantSimple(t, adm.do("SET", "pub:1", "v"), "OK")
		c := as(t, addr, "ro", "x")
		wantBulk(t, c.do("GET", "pub:1"), "v")
		wantError(t, c.do("SET", "pub:1", "x"), "NOPERM")
		wantError(t, c.do("GET", "app:1"), "NOPERM")
	})
	t.Run("Should refuse a command that lists keys to a user that cannot read them all", func(t *testing.T) {
		_, addr, _ := aclServer(t)
		c := as(t, addr, "ro", "x")
		wantError(t, c.do("KEYS", "pub:*"), "NOPERM")
		wantError(t, c.do("SCAN", "0"), "NOPERM")
		wantError(t, c.do("RANDOMKEY"), "NOPERM")
		adm := as(t, addr, "admin", "admin-pass")
		wantSimple(t, adm.do("SET", "k", "v"), "OK")
		if v := adm.do("KEYS", "*"); len(v.Elems) != 1 {
			t.Fatalf("got %+v", v)
		}
	})
	t.Run("Should check the keys of a command inside EXEC", func(t *testing.T) {
		_, addr, _ := aclServer(t)
		c := as(t, addr, "app", "x")
		wantSimple(t, c.do("MULTI"), "OK")
		wantQueued(t, c.do("SET", "app:1", "a"))
		wantError(t, c.do("SET", "other", "b"), "NOPERM No permissions to access a key")
		wantError(t, c.do("EXEC"), "EXECABORT")
		wantNull(t, c.do("GET", "app:1"))
	})
	t.Run("Should check the keys of each SET of a pipeline", func(t *testing.T) {
		_, addr, _ := aclServer(t)
		c := as(t, addr, "app", "x")
		c.send("SET", "app:1", "a")
		c.send("SET", "other", "b")
		c.send("SET", "app:2", "c")
		wantSimple(t, c.read(), "OK")
		wantError(t, c.read(), "NOPERM")
		wantSimple(t, c.read(), "OK")
		wantBulk(t, c.do("GET", "app:2"), "c")
	})
}

func TestACLSubcommands(t *testing.T) {
	t.Run("Should let a user run one subcommand with a rule for it", func(t *testing.T) {
		_, addr, _ := aclServer(t)
		adm := as(t, addr, "admin", "admin-pass")
		wantSimple(t, adm.do("ACL", "SETUSER", "op", "on", "nopass", "~*", "+@connection", "+client|list"), "OK")
		c := as(t, addr, "op", "x")
		if got := text(c.do("CLIENT", "LIST")); !strings.Contains(got, "id=") {
			t.Fatalf("got %q", got)
		}
		wantError(t, c.do("CLIENT", "KILL", "ID", "1"), "NOPERM")
	})
	t.Run("Should refuse a rule for a subcommand that does not exist", func(t *testing.T) {
		_, addr, _ := aclServer(t)
		adm := as(t, addr, "admin", "admin-pass")
		wantError(t, adm.do("ACL", "SETUSER", "op", "+client|nope"), "ERR Error in ACL SETUSER")
	})
}

func TestACLSetUser(t *testing.T) {
	t.Run("Should make, show and remove a user", func(t *testing.T) {
		_, addr, _ := aclServer(t)
		adm := as(t, addr, "admin", "admin-pass")
		wantSimple(t, adm.do("ACL", "SETUSER", "bob", "on", ">pw", "~k:*", "%R~r:*", "+get", "(~z:* +set)"), "OK")
		info := adm.do("ACL", "GETUSER", "bob")
		var flat []string
		for _, e := range info.Elems {
			flat = append(flat, string(e.Bytes))
		}
		joined := strings.Join(flat, "|")
		if !strings.Contains(joined, "~k:* %R~r:*") || !strings.Contains(joined, "+get") {
			t.Fatalf("got %q", joined)
		}
		bob := as(t, addr, "bob", "pw")
		wantNull(t, bob.do("GET", "k:1"))
		wantError(t, bob.do("GET", "x"), "NOPERM")
		if v := adm.do("ACL", "DELUSER", "bob", "nobody"); v.Int != 1 {
			t.Fatalf("got %+v", v)
		}
		if !bob.closed() {
			t.Fatal("the connection of a removed user stayed open")
		}
		wantError(t, adm.do("ACL", "DELUSER", "default"), "ERR")
		wantNull(t, adm.do("ACL", "GETUSER", "bob"))
	})
	t.Run("Should refuse a bad rule and leave the user as it was", func(t *testing.T) {
		_, addr, _ := aclServer(t)
		adm := as(t, addr, "admin", "admin-pass")
		wantError(t, adm.do("ACL", "SETUSER", "app", "-@all", "nonsense"), "ERR Error in ACL SETUSER")
		c := as(t, addr, "app", "x")
		wantSimple(t, c.do("SET", "app:1", "v"), "OK")
	})
	t.Run("Should refuse ACL SETUSER to a user that is not an admin", func(t *testing.T) {
		_, addr, _ := aclServer(t)
		c := as(t, addr, "app", "x")
		wantError(t, c.do("ACL", "SETUSER", "x", "on"), "NOPERM")
	})
	t.Run("Should write the users to the file with ACL SAVE", func(t *testing.T) {
		_, addr, path := aclServer(t)
		adm := as(t, addr, "admin", "admin-pass")
		wantSimple(t, adm.do("ACL", "SETUSER", "carol", "on", "nopass", "~c:*", "+get"), "OK")
		wantSimple(t, adm.do("ACL", "SAVE"), "OK")
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), "user carol on nopass ~c:* +get") {
			t.Fatalf("got %q, %v", data, err)
		}
		if strings.Contains(string(data), "admin-pass") {
			t.Fatal("a password is in the file")
		}
		wantSimple(t, adm.do("ACL", "LOAD"), "OK")
		as(t, addr, "carol", "x")
	})
}

func TestACLLog(t *testing.T) {
	t.Run("Should log a denied command, a denied key and a failed sign in, with no key", func(t *testing.T) {
		_, addr, _ := aclServer(t)
		c := as(t, addr, "app", "x")
		c.do("CONFIG", "GET", "*")
		c.do("GET", "secret-key")
		wantError(t, dial(t, addr).do("AUTH", "admin", "wrong"), "WRONGPASS")
		adm := as(t, addr, "admin", "admin-pass")
		log := adm.do("ACL", "LOG")
		if len(log.Elems) != 3 {
			t.Fatalf("got %d entries: %+v", len(log.Elems), log)
		}
		var all strings.Builder
		reasons := map[string]bool{}
		for _, e := range log.Elems {
			for i := 0; i+1 < len(e.Elems); i += 2 {
				k, v := string(e.Elems[i].Bytes), string(e.Elems[i+1].Bytes)
				all.WriteString(v + " ")
				if k == "reason" {
					reasons[v] = true
				}
			}
		}
		if !reasons["command"] || !reasons["key"] || !reasons["auth"] {
			t.Fatalf("got reasons %v", reasons)
		}
		if strings.Contains(all.String(), "secret-key") {
			t.Fatal("a key is in the log")
		}
		wantSimple(t, adm.do("ACL", "LOG", "RESET"), "OK")
		if log := adm.do("ACL", "LOG"); len(log.Elems) != 0 {
			t.Fatalf("got %+v", log)
		}
	})
}

func TestCertUser(t *testing.T) {
	p := newPKI(t)
	setup := func(t *testing.T, certUser bool, line string) string {
		t.Helper()
		reg, _ := aclFile(t, "user default off\n"+line+"\n")
		cfg := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{p.server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: p.pool}
		return startOn(t, Options{Access: reg, CertUser: certUser}, func(l net.Listener) net.Listener { return TLSListener(l, cfg) })
	}
	connect := func(t *testing.T, addr string) *client {
		t.Helper()
		nc, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: p.pool, ServerName: "localhost", Certificates: []tls.Certificate{p.client}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { nc.Close() })
		return newClient(t, nc)
	}
	t.Run("Should sign in the user named like the common name of the certificate", func(t *testing.T) {
		addr := setup(t, true, "user client on certonly ~* +@all")
		c := connect(t, addr)
		wantBulk(t, c.do("ACL", "WHOAMI"), "client")
		wantSimple(t, c.do("SET", "k", "v"), "OK")
	})
	t.Run("Should not sign in a user that has no certonly rule", func(t *testing.T) {
		addr := setup(t, true, "user client on nopass ~* +@all")
		c := connect(t, addr)
		wantError(t, c.do("GET", "k"), "NOAUTH")
	})
	t.Run("Should do nothing when the setting is off", func(t *testing.T) {
		addr := setup(t, false, "user client on certonly ~* +@all")
		c := connect(t, addr)
		wantError(t, c.do("GET", "k"), "NOAUTH")
	})
	t.Run("Should not let a certonly user sign in with a password", func(t *testing.T) {
		addr := setup(t, false, "user client on certonly ~* +@all")
		c := connect(t, addr)
		wantError(t, c.do("AUTH", "client", ""), "WRONGPASS")
		time.Sleep(time.Millisecond)
	})
}
