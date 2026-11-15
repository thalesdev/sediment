// SPDX-License-Identifier: MIT

package server

import (
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/thalesdev/sediment/internal/shale"
)

func TestProfileServer(t *testing.T) {
	port := os.Getenv("SEDIMENT_PROFILE_PORT")
	if port == "" {
		t.Skip("set SEDIMENT_PROFILE_PORT to serve for an external load and a profile")
	}
	seconds, _ := strconv.Atoi(os.Getenv("SEDIMENT_PROFILE_SECONDS"))
	if seconds <= 0 {
		seconds = 30
	}

	t.Run("Should serve until the time ends", func(t *testing.T) {
		db, err := shale.Open(t.TempDir(), shale.Options{})
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
