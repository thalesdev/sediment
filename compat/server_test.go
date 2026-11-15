// SPDX-License-Identifier: MIT

package compat

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
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

	t.Run("Should run the counter and string commands with go-redis", func(t *testing.T) {
		ctx := context.Background()
		rdb := redis.NewClient(&redis.Options{Addr: addr})
		defer rdb.Close()
		if n, err := rdb.Incr(ctx, "cnt").Result(); err != nil || n != 1 {
			t.Fatalf("INCR: %d, %v", n, err)
		}
		if n, err := rdb.IncrBy(ctx, "cnt", 9).Result(); err != nil || n != 10 {
			t.Fatalf("INCRBY: %d, %v", n, err)
		}
		if n, err := rdb.Decr(ctx, "cnt").Result(); err != nil || n != 9 {
			t.Fatalf("DECR: %d, %v", n, err)
		}
		if n, err := rdb.Append(ctx, "str", "abc").Result(); err != nil || n != 3 {
			t.Fatalf("APPEND: %d, %v", n, err)
		}
		if n, err := rdb.StrLen(ctx, "str").Result(); err != nil || n != 3 {
			t.Fatalf("STRLEN: %d, %v", n, err)
		}
		if ok, err := rdb.SetNX(ctx, "str", "x", 0).Result(); err != nil || ok {
			t.Fatalf("SETNX: %v, %v", ok, err)
		}
		if got, err := rdb.GetSet(ctx, "str", "new").Result(); err != nil || got != "abc" {
			t.Fatalf("GETSET: %q, %v", got, err)
		}
		if got, err := rdb.GetDel(ctx, "str").Result(); err != nil || got != "new" {
			t.Fatalf("GETDEL: %q, %v", got, err)
		}
	})

	t.Run("Should run the TTL commands with go-redis", func(t *testing.T) {
		ctx := context.Background()
		rdb := redis.NewClient(&redis.Options{Addr: addr})
		defer rdb.Close()
		if err := rdb.Set(ctx, "ttl", "v", 30*time.Second).Err(); err != nil {
			t.Fatal(err)
		}
		if d, err := rdb.TTL(ctx, "ttl").Result(); err != nil || d <= 0 || d > 30*time.Second {
			t.Fatalf("TTL: %v, %v", d, err)
		}
		if ok, err := rdb.Persist(ctx, "ttl").Result(); err != nil || !ok {
			t.Fatalf("PERSIST: %v, %v", ok, err)
		}
		if d, err := rdb.TTL(ctx, "ttl").Result(); err != nil || d != -1 {
			t.Fatalf("TTL after PERSIST: %v, %v", d, err)
		}
		if ok, err := rdb.Expire(ctx, "ttl", time.Hour).Result(); err != nil || !ok {
			t.Fatalf("EXPIRE: %v, %v", ok, err)
		}
		if err := rdb.Set(ctx, "short", "v", 50*time.Millisecond).Err(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(120 * time.Millisecond)
		if _, err := rdb.Get(ctx, "short").Result(); !errors.Is(err, redis.Nil) {
			t.Fatalf("GET after expiry: %v", err)
		}
	})

	t.Run("Should run the keyspace commands with go-redis", func(t *testing.T) {
		ctx := context.Background()
		rdb := redis.NewClient(&redis.Options{Addr: addr})
		defer rdb.Close()
		for i := range 25 {
			if err := rdb.Set(ctx, fmt.Sprintf("ks:%02d", i), i, 0).Err(); err != nil {
				t.Fatal(err)
			}
		}
		if err := rdb.Set(ctx, "ks:gone", "v", 40*time.Millisecond).Err(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		it := rdb.Scan(ctx, 0, "ks:*", 4).Iterator()
		n := 0
		for it.Next(ctx) {
			n++
		}
		if err := it.Err(); err != nil || n != 25 {
			t.Fatalf("SCAN visited %d keys: %v", n, err)
		}
		if keys, err := rdb.Keys(ctx, "ks:0?").Result(); err != nil || len(keys) != 10 {
			t.Fatalf("KEYS: %v, %v", keys, err)
		}
		if n, err := rdb.DBSize(ctx).Result(); err != nil || n < 25 {
			t.Fatalf("DBSIZE: %d, %v", n, err)
		}
		if got, err := rdb.Type(ctx, "ks:00").Result(); err != nil || got != "string" {
			t.Fatalf("TYPE: %q, %v", got, err)
		}
		if got, err := rdb.Type(ctx, "ks:gone").Result(); err != nil || got != "none" {
			t.Fatalf("TYPE of an expired key: %q, %v", got, err)
		}
		if key, err := rdb.RandomKey(ctx).Result(); err != nil || key == "" {
			t.Fatalf("RANDOMKEY: %q, %v", key, err)
		}
		it = rdb.ScanType(ctx, 0, "ks:*", 10, "string").Iterator()
		n = 0
		for it.Next(ctx) {
			n++
		}
		if err := it.Err(); err != nil || n != 25 {
			t.Fatalf("SCAN TYPE visited %d keys: %v", n, err)
		}
	})

	for _, proto := range []int{2, 3} {
		t.Run(fmt.Sprintf("Should run the hash commands with go-redis on RESP%d", proto), func(t *testing.T) {
			ctx := context.Background()
			rdb := redis.NewClient(&redis.Options{Addr: addr, Protocol: proto})
			defer rdb.Close()
			key := fmt.Sprintf("hash%d", proto)
			if n, err := rdb.HSet(ctx, key, "a", "1", "b", "2", "c", "3").Result(); err != nil || n != 3 {
				t.Fatalf("HSET: %d, %v", n, err)
			}
			if got, err := rdb.HGet(ctx, key, "b").Result(); err != nil || got != "2" {
				t.Fatalf("HGET: %q, %v", got, err)
			}
			all, err := rdb.HGetAll(ctx, key).Result()
			if err != nil || len(all) != 3 || all["c"] != "3" {
				t.Fatalf("HGETALL: %v, %v", all, err)
			}
			if n, err := rdb.HLen(ctx, key).Result(); err != nil || n != 3 {
				t.Fatalf("HLEN: %d, %v", n, err)
			}
			if ok, err := rdb.HExists(ctx, key, "a").Result(); err != nil || !ok {
				t.Fatalf("HEXISTS: %v, %v", ok, err)
			}
			if n, err := rdb.HIncrBy(ctx, key, "n", 5).Result(); err != nil || n != 5 {
				t.Fatalf("HINCRBY: %d, %v", n, err)
			}
			if f, err := rdb.HIncrByFloat(ctx, key, "f", 1.5).Result(); err != nil || f != 1.5 {
				t.Fatalf("HINCRBYFLOAT: %v, %v", f, err)
			}
			vals, err := rdb.HMGet(ctx, key, "a", "zz").Result()
			if err != nil || len(vals) != 2 || vals[0] != "1" || vals[1] != nil {
				t.Fatalf("HMGET: %v, %v", vals, err)
			}
			it := rdb.HScan(ctx, key, 0, "*", 2).Iterator()
			seen := 0
			for it.Next(ctx) {
				seen++
			}
			if err := it.Err(); err != nil || seen != 10 {
				t.Fatalf("HSCAN gave %d elements: %v", seen, err)
			}
			if n, err := rdb.HDel(ctx, key, "a", "b", "zz").Result(); err != nil || n != 2 {
				t.Fatalf("HDEL: %d, %v", n, err)
			}
			if got, err := rdb.Type(ctx, key).Result(); err != nil || got != "hash" {
				t.Fatalf("TYPE: %q, %v", got, err)
			}
			if err := rdb.Get(ctx, key).Err(); err == nil || !strings.Contains(err.Error(), "WRONGTYPE") {
				t.Fatalf("GET of a hash: %v", err)
			}
		})
	}

	for _, proto := range []int{2, 3} {
		t.Run(fmt.Sprintf("Should run the set commands with go-redis on RESP%d", proto), func(t *testing.T) {
			ctx := context.Background()
			rdb := redis.NewClient(&redis.Options{Addr: addr, Protocol: proto})
			defer rdb.Close()
			a, b := fmt.Sprintf("seta%d", proto), fmt.Sprintf("setb%d", proto)
			if n, err := rdb.SAdd(ctx, a, "1", "2", "3", "4").Result(); err != nil || n != 4 {
				t.Fatalf("SADD: %d, %v", n, err)
			}
			if n, err := rdb.SAdd(ctx, b, "3", "4", "5").Result(); err != nil || n != 3 {
				t.Fatalf("SADD: %d, %v", n, err)
			}
			if got, err := rdb.SMembers(ctx, a).Result(); err != nil || len(got) != 4 {
				t.Fatalf("SMEMBERS: %v, %v", got, err)
			}
			if ok, err := rdb.SIsMember(ctx, a, "2").Result(); err != nil || !ok {
				t.Fatalf("SISMEMBER: %v, %v", ok, err)
			}
			if got, err := rdb.SInter(ctx, a, b).Result(); err != nil || len(got) != 2 {
				t.Fatalf("SINTER: %v, %v", got, err)
			}
			if got, err := rdb.SUnion(ctx, a, b).Result(); err != nil || len(got) != 5 {
				t.Fatalf("SUNION: %v, %v", got, err)
			}
			if got, err := rdb.SDiff(ctx, a, b).Result(); err != nil || len(got) != 2 {
				t.Fatalf("SDIFF: %v, %v", got, err)
			}
			if n, err := rdb.SUnionStore(ctx, a+"u", a, b).Result(); err != nil || n != 5 {
				t.Fatalf("SUNIONSTORE: %d, %v", n, err)
			}
			if n, err := rdb.SCard(ctx, a).Result(); err != nil || n != 4 {
				t.Fatalf("SCARD: %d, %v", n, err)
			}
			if n, err := rdb.SRem(ctx, a, "1", "9").Result(); err != nil || n != 1 {
				t.Fatalf("SREM: %d, %v", n, err)
			}
			if got, err := rdb.SPop(ctx, a).Result(); err != nil || got == "" {
				t.Fatalf("SPOP: %q, %v", got, err)
			}
			if got, err := rdb.SRandMemberN(ctx, b, 2).Result(); err != nil || len(got) != 2 {
				t.Fatalf("SRANDMEMBER: %v, %v", got, err)
			}
			it := rdb.SScan(ctx, b, 0, "*", 2).Iterator()
			seen := 0
			for it.Next(ctx) {
				seen++
			}
			if err := it.Err(); err != nil || seen != 3 {
				t.Fatalf("SSCAN saw %d members: %v", seen, err)
			}
			if got, err := rdb.Type(ctx, b).Result(); err != nil || got != "set" {
				t.Fatalf("TYPE: %q, %v", got, err)
			}
		})
	}

	for _, proto := range []int{2, 3} {
		t.Run(fmt.Sprintf("Should run transactions with go-redis on RESP%d", proto), func(t *testing.T) {
			ctx := context.Background()
			rdb := redis.NewClient(&redis.Options{Addr: addr, Protocol: proto})
			defer rdb.Close()
			key := fmt.Sprintf("txkey%d", proto)
			cmds, err := rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
				p.Set(ctx, key, "10", 0)
				p.Incr(ctx, key)
				p.Get(ctx, key)
				return nil
			})
			if err != nil || len(cmds) != 3 {
				t.Fatalf("TxPipelined: %v, %d commands", err, len(cmds))
			}
			if got := cmds[2].(*redis.StringCmd).Val(); got != "11" {
				t.Fatalf("GET inside the transaction: %q", got)
			}
			err = rdb.Watch(ctx, func(tx *redis.Tx) error {
				n, err := tx.Get(ctx, key).Int()
				if err != nil {
					return err
				}
				if err := rdb.Set(ctx, key, "99", 0).Err(); err != nil {
					return err
				}
				_, err = tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
					p.Set(ctx, key, n+1, 0)
					return nil
				})
				return err
			}, key)
			if !errors.Is(err, redis.TxFailedErr) {
				t.Fatalf("a conflicting write should fail the transaction, got %v", err)
			}
			err = rdb.Watch(ctx, func(tx *redis.Tx) error {
				n, err := tx.Get(ctx, key).Int()
				if err != nil {
					return err
				}
				_, err = tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
					p.Set(ctx, key, n+1, 0)
					return nil
				})
				return err
			}, key)
			if err != nil {
				t.Fatalf("Watch without a conflict: %v", err)
			}
			if got, err := rdb.Get(ctx, key).Result(); err != nil || got != "100" {
				t.Fatalf("GET after the transactions: %q, %v", got, err)
			}
		})
	}

	t.Run("Should run the remaining string commands with go-redis", func(t *testing.T) {
		ctx := context.Background()
		rdb := redis.NewClient(&redis.Options{Addr: addr})
		defer rdb.Close()
		if err := rdb.SetEx(ctx, "more:ex", "v", 30*time.Second).Err(); err != nil {
			t.Fatal(err)
		}
		if ok, err := rdb.MSetNX(ctx, "more:a", "1", "more:b", "2").Result(); err != nil || !ok {
			t.Fatalf("MSETNX: %v, %v", ok, err)
		}
		if ok, err := rdb.MSetNX(ctx, "more:c", "3", "more:a", "9").Result(); err != nil || ok {
			t.Fatalf("MSETNX over an existing key: %v, %v", ok, err)
		}
		if got, err := rdb.GetEx(ctx, "more:ex", time.Minute).Result(); err != nil || got != "v" {
			t.Fatalf("GETEX: %q, %v", got, err)
		}
		if got, err := rdb.GetRange(ctx, "more:ex", 0, 0).Result(); err != nil || got != "v" {
			t.Fatalf("GETRANGE: %q, %v", got, err)
		}
		if n, err := rdb.SetRange(ctx, "more:ex", 3, "x").Result(); err != nil || n != 4 {
			t.Fatalf("SETRANGE: %d, %v", n, err)
		}
		if f, err := rdb.IncrByFloat(ctx, "more:f", 2.5).Result(); err != nil || f != 2.5 {
			t.Fatalf("INCRBYFLOAT: %v, %v", f, err)
		}
		if err := rdb.Rename(ctx, "more:a", "more:z").Err(); err != nil {
			t.Fatalf("RENAME: %v", err)
		}
		if ok, err := rdb.RenameNX(ctx, "more:z", "more:b").Result(); err != nil || ok {
			t.Fatalf("RENAMENX: %v, %v", ok, err)
		}
		if n, err := rdb.Unlink(ctx, "more:z", "more:b").Result(); err != nil || n != 2 {
			t.Fatalf("UNLINK: %d, %v", n, err)
		}
	})

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
