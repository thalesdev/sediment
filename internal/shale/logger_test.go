// SPDX-License-Identifier: MIT

package shale

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestLogger(t *testing.T) {
	t.Parallel()
	t.Run("Should log the recovery, each flush and each compaction", func(t *testing.T) {
		var out syncBuffer
		lg := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
		dir := t.TempDir()
		db := openDB(t, dir, Options{MemtableSize: 4 << 10, Logger: lg})
		for i := range 2000 {
			mustPut(t, db, string(keyN(i)), value100(i))
		}
		if err := db.WaitIdle(); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		for _, want := range []string{"recovery finished", "flush started", "flush finished", "compaction finished"} {
			if !strings.Contains(got, want) {
				t.Errorf("the log has no %q:\n%s", want, got)
			}
		}
	})
	t.Run("Should never log a key or a value", func(t *testing.T) {
		var out syncBuffer
		lg := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
		db := openDB(t, t.TempDir(), Options{MemtableSize: 4 << 10, Logger: lg})
		for i := range 500 {
			mustPut(t, db, "secret-key-"+string(keyN(i)), "secret-value-"+value100(i))
		}
		if err := db.WaitIdle(); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if got := out.String(); strings.Contains(got, "secret-") {
			t.Errorf("the log has a key or a value:\n%s", got)
		}
	})
	t.Run("Should work with no logger", func(t *testing.T) {
		db := openDB(t, t.TempDir(), Options{MemtableSize: 4 << 10})
		for i := range 500 {
			mustPut(t, db, string(keyN(i)), value100(i))
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	})
}
