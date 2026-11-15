// SPDX-License-Identifier: MIT

//go:build unix

package server

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/thalesdev/sediment/internal/shale"
)

func profileWindows(dir string) {
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		var f *os.File
		n := 0
		for s := range sigs {
			switch s {
			case syscall.SIGUSR1:
				n++
				var err error
				if f, err = os.Create(filepath.Join(dir, fmt.Sprintf("cpu%d.prof", n))); err == nil {
					_ = pprof.StartCPUProfile(f)
				}
			case syscall.SIGUSR2:
				pprof.StopCPUProfile()
				if f != nil {
					_ = f.Close()
				}
			}
		}
	}()
}

func TestProfileServer(t *testing.T) {
	port := os.Getenv("SEDIMENT_PROFILE_PORT")
	if port == "" {
		t.Skip("set SEDIMENT_PROFILE_PORT to serve for an external load and a profile")
	}
	seconds, _ := strconv.Atoi(os.Getenv("SEDIMENT_PROFILE_SECONDS"))
	if seconds <= 0 {
		seconds = 30
	}
	if dir := os.Getenv("SEDIMENT_PROFILE_DIR"); dir != "" {
		profileWindows(dir)
	}

	t.Run("Should serve until the time ends", func(t *testing.T) {
		db, err := shale.Open(t.TempDir(), shale.Options{MemtableSize: 64 << 20})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		s, err := New(Options{Store: db, Log: quietLog()})
		if err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = s.Serve(l) }()
		time.Sleep(time.Duration(seconds) * time.Second)
		ctx, cancel := contextWithTimeout(10 * time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
	})
}
