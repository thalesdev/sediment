// SPDX-License-Identifier: MIT

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/server/command"
)

type blockingStore struct {
	*shale.DB
	release chan struct{}
	started chan struct{}
}

func (s *blockingStore) Checkpoint(dir string) (shale.CheckpointInfo, error) {
	s.started <- struct{}{}
	<-s.release
	return s.DB.Checkpoint(dir)
}

func infoField(t *testing.T, c *client, section, field string) string {
	t.Helper()
	text := string(c.do("INFO", section).Bytes)
	for _, line := range strings.Split(text, "\r\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && k == field {
			return v
		}
	}
	t.Fatalf("INFO %s has no %s", section, field)
	return ""
}

func waitSaved(t *testing.T, c *client) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for infoField(t, c, "persistence", "bgsave_in_progress") != "0" || infoField(t, c, "persistence", "lastsave") == "0" {
		if time.Now().After(deadline) {
			t.Fatal("the backup did not end")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBackup(t *testing.T) {
	clock := &fakeClock{}
	clock.ms.Store(1_700_000_000_000)

	t.Run("Should refuse BGSAVE when no backup directory is set", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantError(t, c.do("BGSAVE"), "ERR no backup-dir is set")
		wantInt(t, c.do("LASTSAVE"), 0)
		if got := infoField(t, c, "persistence", "backup_dir_set"); got != "0" {
			t.Fatalf("backup_dir_set = %s", got)
		}
	})

	t.Run("Should make a backup that another server can open and serve", func(t *testing.T) {
		dir := t.TempDir()
		source, err := shale.Open(t.TempDir(), shale.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		if err := command.EnsureFormat(source); err != nil {
			t.Fatal(err)
		}
		_, addr := startServer(t, Options{Now: clock.now, Store: source, BackupDir: dir, SweepInterval: -1})
		c := dial(t, addr)
		c.do("SET", "str", "v")
		c.do("SETEX", "ttl", "100", "t")
		c.do("HSET", "hash", "a", "1", "b", "2")
		c.do("SADD", "set", "x", "y", "z")
		c.do("DEL", "gone")
		wantSimple(t, c.do("BGSAVE"), "Background saving started")
		waitSaved(t, c)
		wantInt(t, c.do("LASTSAVE"), 1_700_000_000)
		if got := infoField(t, c, "persistence", "last_bgsave_status"); got != "ok" {
			t.Fatalf("last_bgsave_status = %s", got)
		}
		name := infoField(t, c, "persistence", "last_bgsave_name")
		if name != "backup-1700000000" {
			t.Fatalf("name %q", name)
		}
		c.do("SET", "after", "x")
		c.do("HSET", "hash", "c", "3")

		backup, err := shale.Open(filepath.Join(dir, name), shale.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer backup.Close()
		if err := command.EnsureFormat(backup); err != nil {
			t.Fatalf("the backup does not have the format record: %v", err)
		}
		_, addr2 := startServer(t, Options{Now: clock.now, Store: backup, SweepInterval: -1})
		d := dial(t, addr2)
		wantBulk(t, d.do("GET", "str"), "v")
		wantInt(t, d.do("TTL", "ttl"), 100)
		wantStrs(t, d.do("HGETALL", "hash"), "a", "1", "b", "2")
		wantStrs(t, d.do("SMEMBERS", "set"), "x", "y", "z")
		wantNull(t, d.do("GET", "after"))
		wantInt(t, d.do("DBSIZE"), 4)
	})

	t.Run("Should name a second backup of the same second with a suffix", func(t *testing.T) {
		dir := t.TempDir()
		_, addr := startServer(t, Options{Now: clock.now, BackupDir: dir, SweepInterval: -1})
		c := dial(t, addr)
		c.do("SET", "k", "v")
		for range 2 {
			wantSimple(t, c.do("BGSAVE"), "Background saving started")
			waitSaved(t, c)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if len(names) != 2 || names[0] != "backup-1700000000" || names[1] != "backup-1700000000-1" {
			t.Fatalf("directories %v", names)
		}
	})

	t.Run("Should refuse a second BGSAVE while one runs", func(t *testing.T) {
		db, err := shale.Open(t.TempDir(), shale.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		store := &blockingStore{DB: db, release: make(chan struct{}), started: make(chan struct{}, 1)}
		_, addr := startServer(t, Options{Now: clock.now, Store: store, BackupDir: t.TempDir(), SweepInterval: -1})
		c := dial(t, addr)
		wantSimple(t, c.do("BGSAVE"), "Background saving started")
		<-store.started
		wantError(t, c.do("BGSAVE"), "ERR Background save already in progress")
		if got := infoField(t, c, "persistence", "bgsave_in_progress"); got != "1" {
			t.Fatalf("bgsave_in_progress = %s", got)
		}
		close(store.release)
		waitSaved(t, c)
		wantSimple(t, c.do("BGSAVE"), "Background saving started")
		waitSaved(t, c)
	})

	t.Run("Should report a backup that failed", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "backups")
		if err := os.WriteFile(dir, []byte("a file, so the directory cannot be made"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, addr := startServer(t, Options{Now: clock.now, BackupDir: dir, SweepInterval: -1})
		c := dial(t, addr)
		c.do("SET", "k", "v")
		wantSimple(t, c.do("BGSAVE"), "Background saving started")
		deadline := time.Now().Add(20 * time.Second)
		for infoField(t, c, "persistence", "last_bgsave_status") != "err" {
			if time.Now().After(deadline) {
				t.Fatal("the failure was not reported")
			}
			time.Sleep(5 * time.Millisecond)
		}
		wantInt(t, c.do("LASTSAVE"), 0)
	})
}
