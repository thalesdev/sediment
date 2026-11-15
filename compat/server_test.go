// SPDX-License-Identifier: MIT

package compat

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/server"
	"github.com/thalesdev/sediment/server/acl"
)

func startSedimentd(t *testing.T, o server.Options) string {
	t.Helper()
	db, err := shale.Open(t.TempDir(), shale.Options{})
	if err != nil {
		t.Fatal(err)
	}
	o.Store = db
	o.AuthDelay = time.Millisecond
	o.AuthDelayMax = 5 * time.Millisecond
	s, err := server.New(o)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
		_ = db.Close()
	})
	return l.Addr().String()
}

func TestGoRedisClient(t *testing.T) {
	addr := startSedimentd(t, server.Options{})
	for _, proto := range []int{2, 3} {
		t.Run(fmt.Sprintf("Should work with go-redis on RESP%d", proto), func(t *testing.T) {
			ctx := context.Background()
			rdb := redis.NewClient(&redis.Options{Addr: addr, Protocol: proto})
			defer rdb.Close()

			if err := rdb.Ping(ctx).Err(); err != nil {
				t.Fatal(err)
			}
			if err := rdb.Set(ctx, "k", "v", 0).Err(); err != nil {
				t.Fatal(err)
			}
			if got, err := rdb.Get(ctx, "k").Result(); err != nil || got != "v" {
				t.Fatalf("got %q, %v", got, err)
			}
			if _, err := rdb.Get(ctx, "missing").Result(); !errors.Is(err, redis.Nil) {
				t.Fatalf("got %v", err)
			}
			if _, err := rdb.SetArgs(ctx, "k", "x", redis.SetArgs{Mode: "NX"}).Result(); !errors.Is(err, redis.Nil) {
				t.Fatalf("SET NX on an existing key: %v", err)
			}
			if got, err := rdb.SetArgs(ctx, "fresh"+fmt.Sprint(proto), "x", redis.SetArgs{Mode: "NX"}).Result(); err != nil || got != "OK" {
				t.Fatalf("SET NX on a new key: %q, %v", got, err)
			}
			if err := rdb.MSet(ctx, "a", "1", "b", "2").Err(); err != nil {
				t.Fatal(err)
			}
			vals, err := rdb.MGet(ctx, "a", "nope", "b").Result()
			if err != nil || len(vals) != 3 || vals[0] != "1" || vals[1] != nil || vals[2] != "2" {
				t.Fatalf("got %v, %v", vals, err)
			}
			if n, err := rdb.Exists(ctx, "a", "b", "nope").Result(); err != nil || n != 2 {
				t.Fatalf("got %d, %v", n, err)
			}
			if n, err := rdb.Del(ctx, "a", "b", "nope").Result(); err != nil || n != 2 {
				t.Fatalf("got %d, %v", n, err)
			}
			if got, err := rdb.SetArgs(ctx, "k", "w", redis.SetArgs{Get: true}).Result(); err != nil || got != "v" {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}

	t.Run("Should pipeline many commands with go-redis", func(t *testing.T) {
		ctx := context.Background()
		rdb := redis.NewClient(&redis.Options{Addr: addr})
		defer rdb.Close()
		pipe := rdb.Pipeline()
		for i := range 2000 {
			pipe.Set(ctx, fmt.Sprintf("p%d", i), i, 0)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			t.Fatal(err)
		}
		gets := make([]*redis.StringCmd, 2000)
		pipe = rdb.Pipeline()
		for i := range gets {
			gets[i] = pipe.Get(ctx, fmt.Sprintf("p%d", i))
		}
		if _, err := pipe.Exec(ctx); err != nil {
			t.Fatal(err)
		}
		for i, c := range gets {
			if c.Val() != fmt.Sprint(i) {
				t.Fatalf("p%d: got %q", i, c.Val())
			}
		}
	})

	t.Run("Should refuse a command that the server does not have", func(t *testing.T) {
		rdb := redis.NewClient(&redis.Options{Addr: addr})
		defer rdb.Close()
		if err := rdb.Do(context.Background(), "EVAL", "return 1", "0").Err(); err == nil {
			t.Fatal("EVAL worked")
		}
		if err := rdb.Do(context.Background(), "REPLICAOF", "evil.example", "6379").Err(); err == nil {
			t.Fatal("REPLICAOF worked")
		}
		if err := rdb.Do(context.Background(), "CONFIG", "SET", "dir", "/tmp").Err(); err == nil {
			t.Fatal("CONFIG SET worked")
		}
	})
}

func TestGoRedisAuth(t *testing.T) {
	hash, err := acl.HashPassword([]byte("s3cret"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "users.acl")
	text := "user default off\nuser app on " + hash + " ~* +@read +@write +@connection\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, _, err := acl.Load(path, acl.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	addr := startSedimentd(t, server.Options{Access: reg})
	ctx := context.Background()

	t.Run("Should sign in with go-redis credentials", func(t *testing.T) {
		for _, proto := range []int{2, 3} {
			rdb := redis.NewClient(&redis.Options{Addr: addr, Username: "app", Password: "s3cret", Protocol: proto})
			if err := rdb.Set(ctx, "k", "v", 0).Err(); err != nil {
				t.Fatalf("RESP%d: %v", proto, err)
			}
			rdb.Close()
		}
	})

	t.Run("Should refuse a client with no credentials or a wrong password", func(t *testing.T) {
		for _, o := range []*redis.Options{
			{Addr: addr},
			{Addr: addr, Username: "app", Password: "wrong"},
			{Addr: addr, Username: "default", Password: "s3cret"},
		} {
			rdb := redis.NewClient(o)
			if err := rdb.Get(ctx, "k").Err(); err == nil {
				t.Fatalf("%+v: a client got in", o.Username)
			}
			rdb.Close()
		}
	})
}
