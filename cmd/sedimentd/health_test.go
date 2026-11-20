// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/server"
	"github.com/thalesdev/sediment/server/acl"
	"github.com/thalesdev/sediment/server/command"
	"github.com/thalesdev/sediment/server/config"
)

func noEnv(string) string { return "" }

func portOf(l net.Listener) string {
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func openStore(t *testing.T) *shale.DB {
	t.Helper()
	db, err := shale.Open(t.TempDir(), shale.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func healthConfig(bind string, port int) config.Config {
	cfg := config.Defaults()
	cfg.Bind = []string{bind}
	cfg.Port = port
	return cfg
}

func TestHealthcheck(t *testing.T) {
	t.Run("Should succeed when the server answers PONG", func(t *testing.T) {
		srv, err := server.New(server.Options{Store: openStore(t)})
		if err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go srv.Serve(l)
		t.Cleanup(func() { srv.Shutdown(t.Context()) })
		var errOut bytes.Buffer
		if err := healthcheck([]string{"-port", portOf(l)}, &errOut, noEnv); err != nil {
			t.Fatalf("%v, %s", err, errOut.String())
		}
	})
	t.Run("Should fail when nothing listens", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := portOf(l)
		l.Close()
		if err := healthcheck([]string{"-port", port}, &bytes.Buffer{}, noEnv); err == nil {
			t.Fatal("a closed port is healthy")
		}
	})
	t.Run("Should fail while the node is loading", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		loading := server.NewLoading()
		go loading.Serve(l)
		t.Cleanup(loading.Close)
		err = healthcheck([]string{"-port", portOf(l)}, &bytes.Buffer{}, noEnv)
		if err == nil || !bytes.Contains([]byte(err.Error()), []byte("LOADING")) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("Should count a server that wants a sign in as alive, and sign in when it has a password", func(t *testing.T) {
		hash, err := acl.HashPassword([]byte("pw"))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "users.acl")
		if err := os.WriteFile(path, []byte("user default off\nuser probe on "+hash+" ~* +ping +@connection\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		reg, _, err := acl.Load(path, acl.ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var _ command.Access = reg
		srv, err := server.New(server.Options{Store: openStore(t), Access: reg})
		if err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go srv.Serve(l)
		t.Cleanup(func() { srv.Shutdown(t.Context()) })
		if err := healthcheck([]string{"-port", portOf(l)}, &bytes.Buffer{}, noEnv); err != nil {
			t.Fatalf("a server that needs a sign in is not alive: %v", err)
		}
		env := func(k string) string {
			return map[string]string{healthUserEnv: "probe", healthPasswordEnv: "pw"}[k]
		}
		if err := healthcheck([]string{"-port", portOf(l)}, &bytes.Buffer{}, env); err != nil {
			t.Fatal(err)
		}
		bad := func(k string) string {
			return map[string]string{healthUserEnv: "probe", healthPasswordEnv: "nope"}[k]
		}
		if err := healthcheck([]string{"-port", portOf(l)}, &bytes.Buffer{}, bad); err == nil {
			t.Fatal("a wrong password is healthy")
		}
	})
	t.Run("Should dial the loopback address when the server binds to all addresses", func(t *testing.T) {
		cfg := healthConfig("0.0.0.0", 7000)
		if got := healthTarget(cfg); got != "127.0.0.1:7000" {
			t.Fatalf("got %q", got)
		}
		if got := healthTarget(healthConfig("::", 7000)); got != "[::1]:7000" {
			t.Fatalf("got %q", got)
		}
	})
}
