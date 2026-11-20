// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestLogger(t *testing.T) {
	t.Run("Should write JSON when the format is json", func(t *testing.T) {
		var out bytes.Buffer
		level := new(slog.LevelVar)
		newLogger(&out, level, "json").Info("hello", "k", "v")
		var m map[string]any
		if err := json.Unmarshal(out.Bytes(), &m); err != nil || m["msg"] != "hello" || m["k"] != "v" {
			t.Fatalf("got %q, %v", out.String(), err)
		}
	})
	t.Run("Should write text when the format is text", func(t *testing.T) {
		var out bytes.Buffer
		newLogger(&out, new(slog.LevelVar), "text").Info("hello")
		if !bytes.Contains(out.Bytes(), []byte("msg=hello")) {
			t.Fatalf("got %q", out.String())
		}
	})
	t.Run("Should change the level when the file changes and the reload runs", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sediment.conf")
		if err := os.WriteFile(path, []byte("loglevel info\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		level := new(slog.LevelVar)
		lg := newLogger(&out, level, "text")
		lg.Debug("hidden")
		if out.Len() != 0 {
			t.Fatalf("debug line at info level: %q", out.String())
		}
		if err := os.WriteFile(path, []byte("loglevel debug\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := reloadLevel(path, level); err != nil {
			t.Fatal(err)
		}
		lg.Debug("shown")
		if !bytes.Contains(out.Bytes(), []byte("msg=shown")) {
			t.Fatalf("got %q", out.String())
		}
	})
	t.Run("Should keep the level when the file does not parse", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sediment.conf")
		if err := os.WriteFile(path, []byte("loglevel loud\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		level := new(slog.LevelVar)
		level.Set(slog.LevelWarn)
		if err := reloadLevel(path, level); err == nil {
			t.Fatal("a bad file was accepted")
		}
		if level.Level() != slog.LevelWarn {
			t.Fatalf("the level is %v", level.Level())
		}
	})
}
