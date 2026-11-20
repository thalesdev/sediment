// SPDX-License-Identifier: MIT

package server

import (
	"errors"
	"strings"
	"testing"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/server/command"
)

type sickStore struct {
	command.Store
	err error
}

func (s sickStore) Err() error { return s.err }

func TestHealth(t *testing.T) {
	open := func(t *testing.T) *shale.DB {
		t.Helper()
		db, err := shale.Open(t.TempDir(), shale.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	t.Run("Should answer PONG and show a healthy storage", func(t *testing.T) {
		_, addr := startServer(t, Options{Store: open(t)})
		c := dial(t, addr)
		wantSimple(t, c.do("PING"), "PONG")
		if got := text(c.do("INFO", "server")); !strings.Contains(got, "loading:0") || !strings.Contains(got, "storage:ok") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("Should answer an error to PING when the storage stopped", func(t *testing.T) {
		_, addr := startServer(t, Options{Store: sickStore{open(t), errors.New("disk full")}})
		c := dial(t, addr)
		v := c.do("PING")
		wantError(t, v, "MISCONF")
		if strings.Contains(string(v.Bytes), "disk full") {
			t.Fatalf("the cause is in the reply: %q", v.Bytes)
		}
		if got := text(c.do("INFO", "server")); !strings.Contains(got, "storage:failed") {
			t.Fatalf("got %q", got)
		}
		wantNull(t, c.do("GET", "k"))
	})
	t.Run("Should show loading and refuse commands while the node loads", func(t *testing.T) {
		l := listenLocal(t)
		loading := NewLoading()
		go loading.Serve(l)
		t.Cleanup(loading.Close)
		c := dial(t, l.Addr().String())
		wantError(t, c.do("PING"), "LOADING")
		wantError(t, c.do("GET", "k"), "LOADING")
		if got := text(c.do("INFO")); !strings.Contains(got, "loading:1") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("Should close the connections and free the listener on Close", func(t *testing.T) {
		l := listenLocal(t)
		addr := l.Addr().String()
		loading := NewLoading()
		go loading.Serve(l)
		c := dial(t, addr)
		wantError(t, c.do("PING"), "LOADING")
		loading.Close()
		if !c.closed() {
			t.Fatal("the connection is still open")
		}
		again := listenLocalOn(t, addr)
		again.Close()
	})
}
