// SPDX-License-Identifier: MIT

package server

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/thalesdev/sediment/resp"
)

func scanPage(t *testing.T, v resp.Value) (string, []string) {
	t.Helper()
	if v.Kind != resp.KindArray || len(v.Elems) != 2 || v.Elems[0].Kind != resp.KindBulk || v.Elems[1].Kind != resp.KindArray {
		t.Fatalf("not a SCAN reply: %+v", v)
	}
	var keys []string
	for _, e := range v.Elems[1].Elems {
		keys = append(keys, string(e.Bytes))
	}
	return string(v.Elems[0].Bytes), keys
}

func scanAll(t *testing.T, c *client, args ...string) []string {
	t.Helper()
	var all []string
	cursor := "0"
	for range 10000 {
		cmd := append([]string{"SCAN", cursor}, args...)
		next, keys := scanPage(t, c.do(cmd...))
		all = append(all, keys...)
		if next == "0" {
			slices.Sort(all)
			return all
		}
		cursor = next
	}
	t.Fatal("SCAN did not end")
	return nil
}

func keyList(t *testing.T, v resp.Value) []string {
	t.Helper()
	if v.Kind != resp.KindArray {
		t.Fatalf("not an array: %+v", v)
	}
	var out []string
	for _, e := range v.Elems {
		out = append(out, string(e.Bytes))
	}
	slices.Sort(out)
	return out
}

func TestKeyspace(t *testing.T) {
	clock := &fakeClock{}
	clock.ms.Store(1_700_000_000_000)
	_, addr := startServer(t, Options{Now: clock.now})
	c := dial(t, addr)
	var users []string
	for i := range 57 {
		k := fmt.Sprintf("user:%03d", i)
		c.do("SET", k, "v")
		users = append(users, k)
	}
	c.do("SET", "other:1", "v")
	c.do("SET", "other:2", "v")

	t.Run("Should visit every key with SCAN", func(t *testing.T) {
		got := scanAll(t, c, "COUNT", "5")
		want := append(slices.Clone(users), "other:1", "other:2")
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("got %d keys, want %d", len(got), len(want))
		}
	})

	t.Run("Should honor MATCH and a plain prefix in SCAN", func(t *testing.T) {
		if got := scanAll(t, c, "MATCH", "user:*", "COUNT", "7"); !slices.Equal(got, users) {
			t.Fatalf("got %d keys, want %d", len(got), len(users))
		}
		if got := scanAll(t, c, "MATCH", "other:?"); !slices.Equal(got, []string{"other:1", "other:2"}) {
			t.Fatalf("got %v", got)
		}
		if got := scanAll(t, c, "MATCH", "*:00[12]"); !slices.Equal(got, []string{"user:001", "user:002"}) {
			t.Fatalf("got %v", got)
		}
		if got := scanAll(t, c, "MATCH", "nothing*"); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("Should honor TYPE in SCAN", func(t *testing.T) {
		if got := scanAll(t, c, "TYPE", "string", "MATCH", "other:*"); len(got) != 2 {
			t.Fatalf("got %v", got)
		}
		if got := scanAll(t, c, "TYPE", "list"); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("Should return a page of COUNT keys with a cursor", func(t *testing.T) {
		next, keys := scanPage(t, c.do("SCAN", "0", "COUNT", "3"))
		if next == "0" || len(keys) != 3 {
			t.Fatalf("cursor %q keys %v", next, keys)
		}
	})

	t.Run("Should refuse a cursor that is not valid", func(t *testing.T) {
		wantError(t, c.do("SCAN", "abc"), "ERR invalid cursor")
		wantError(t, c.do("SCAN", "-1"), "ERR invalid cursor")
		wantError(t, c.do("SCAN", "999999"), "ERR invalid cursor")
		wantError(t, c.do("SCAN", "0", "COUNT"), "ERR syntax")
		wantError(t, c.do("SCAN", "0", "COUNT", "0"), "ERR syntax")
		wantError(t, c.do("SCAN", "0", "COUNT", "x"), "ERR value is not an integer")
		wantError(t, c.do("SCAN", "0", "BAD", "1"), "ERR syntax")
	})

	t.Run("Should keep only a few cursors for each connection", func(t *testing.T) {
		d := dial(t, addr)
		first, _ := scanPage(t, d.do("SCAN", "0", "COUNT", "1"))
		for range 40 {
			scanPage(t, d.do("SCAN", "0", "COUNT", "1"))
		}
		wantError(t, d.do("SCAN", first), "ERR invalid cursor")
	})

	t.Run("Should not share cursors between connections", func(t *testing.T) {
		next, _ := scanPage(t, c.do("SCAN", "0", "COUNT", "1"))
		d := dial(t, addr)
		other, _ := scanPage(t, d.do("SCAN", "0", "COUNT", "1"))
		if next != other {
			wantError(t, d.do("SCAN", next), "ERR invalid cursor")
		}
	})

	t.Run("Should finish a SCAN while keys are written and deleted between pages", func(t *testing.T) {
		cursor := "0"
		seen := map[string]bool{}
		step := 0
		for {
			next, keys := scanPage(t, c.do("SCAN", cursor, "MATCH", "user:*", "COUNT", "4"))
			for _, k := range keys {
				seen[k] = true
			}
			if next == "0" {
				break
			}
			cursor = next
			step++
			c.do("SET", fmt.Sprintf("zzz:%d", step), "v")
			c.do("DEL", fmt.Sprintf("other:%d", step))
		}
		for _, k := range users {
			if !seen[k] {
				t.Fatalf("%s was not visited", k)
			}
		}
	})

	t.Run("Should list keys with KEYS", func(t *testing.T) {
		if got := keyList(t, c.do("KEYS", "user:*")); !slices.Equal(got, users) {
			t.Fatalf("got %d keys", len(got))
		}
		if got := keyList(t, c.do("KEYS", "user:00?")); len(got) != 10 {
			t.Fatalf("got %v", got)
		}
		if got := keyList(t, c.do("KEYS", "nothing")); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
		if got := keyList(t, c.do("KEYS", "*")); len(got) < len(users) {
			t.Fatalf("got %d", len(got))
		}
	})

	t.Run("Should count with DBSIZE and report TYPE", func(t *testing.T) {
		n := len(keyList(t, c.do("KEYS", "*")))
		wantInt(t, c.do("DBSIZE"), int64(n))
		wantSimple(t, c.do("TYPE", "user:001"), "string")
		wantSimple(t, c.do("TYPE", "missing"), "none")
	})

	t.Run("Should return a live key with RANDOMKEY", func(t *testing.T) {
		for range 20 {
			v := c.do("RANDOMKEY")
			if v.Kind != resp.KindBulk {
				t.Fatalf("got %+v", v)
			}
			wantInt(t, c.do("EXISTS", string(v.Bytes)), 1)
		}
	})

	t.Run("Should hide expired keys", func(t *testing.T) {
		c.do("SET", "tmp:1", "v", "EX", "10")
		c.do("SET", "tmp:2", "v")
		if got := scanAll(t, c, "MATCH", "tmp:*"); len(got) != 2 {
			t.Fatalf("got %v", got)
		}
		before := len(keyList(t, c.do("KEYS", "*")))
		clock.advance(11 * time.Second)
		if got := scanAll(t, c, "MATCH", "tmp:*"); !slices.Equal(got, []string{"tmp:2"}) {
			t.Fatalf("got %v", got)
		}
		if got := keyList(t, c.do("KEYS", "tmp:*")); !slices.Equal(got, []string{"tmp:2"}) {
			t.Fatalf("got %v", got)
		}
		wantInt(t, c.do("DBSIZE"), int64(before-1))
		wantSimple(t, c.do("TYPE", "tmp:1"), "none")
	})

	t.Run("Should check the number of arguments", func(t *testing.T) {
		wantError(t, c.do("KEYS"), "ERR wrong number")
		wantError(t, c.do("DBSIZE", "x"), "ERR wrong number")
		wantError(t, c.do("TYPE"), "ERR wrong number")
		wantError(t, c.do("SCAN"), "ERR wrong number")
	})
}

func TestKeyspaceEmpty(t *testing.T) {
	_, addr := startServer(t, Options{})
	c := dial(t, addr)

	t.Run("Should answer on an empty database", func(t *testing.T) {
		next, keys := scanPage(t, c.do("SCAN", "0"))
		if next != "0" || len(keys) != 0 {
			t.Fatalf("cursor %q keys %v", next, keys)
		}
		wantInt(t, c.do("DBSIZE"), 0)
		wantNull(t, c.do("RANDOMKEY"))
		if got := keyList(t, c.do("KEYS", "*")); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})
}

func TestKeysReplyLimit(t *testing.T) {
	_, addr := startServer(t, Options{MaxReply: 100})
	c := dial(t, addr)

	t.Run("Should refuse a KEYS reply that is too large and cut a SCAN page", func(t *testing.T) {
		for i := range 30 {
			c.do("SET", fmt.Sprintf("a-long-key-name-%03d", i), "v")
		}
		wantError(t, c.do("KEYS", "*"), "ERR reply is too large")
		got := scanAll(t, c, "COUNT", "1000")
		if len(got) != 30 {
			t.Fatalf("SCAN visited %d keys, want 30", len(got))
		}
	})
}
