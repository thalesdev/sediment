// SPDX-License-Identifier: MIT

package server

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/thalesdev/sediment/resp"
)

func wantQueued(t *testing.T, v resp.Value) {
	t.Helper()
	wantSimple(t, v, "QUEUED")
}

func wantArrayLen(t *testing.T, v resp.Value, n int) []resp.Value {
	t.Helper()
	if v.Kind != resp.KindArray || len(v.Elems) != n {
		t.Fatalf("got kind %d with %d elements, want an array of %d: %+v", v.Kind, len(v.Elems), n, v)
	}
	return v.Elems
}

func TestTransactions(t *testing.T) {
	clock := &fakeClock{}
	clock.ms.Store(1_700_000_000_000)
	_, addr := startServer(t, Options{Now: clock.now, SweepInterval: -1})
	c := dial(t, addr)
	other := dial(t, addr)

	t.Run("Should queue the commands and run them in order at EXEC", func(t *testing.T) {
		wantSimple(t, c.do("MULTI"), "OK")
		wantQueued(t, c.do("SET", "a", "1"))
		wantQueued(t, c.do("INCR", "a"))
		wantQueued(t, c.do("GET", "a"))
		wantNull(t, other.do("GET", "a"))
		got := wantArrayLen(t, c.do("EXEC"), 3)
		wantSimple(t, got[0], "OK")
		wantInt(t, got[1], 2)
		wantBulk(t, got[2], "2")
		wantBulk(t, other.do("GET", "a"), "2")
	})

	t.Run("Should abort at EXEC after an error while queueing", func(t *testing.T) {
		wantSimple(t, c.do("MULTI"), "OK")
		wantQueued(t, c.do("SET", "x", "1"))
		wantError(t, c.do("NOPE", "x"), "ERR unknown command")
		wantQueued(t, c.do("SET", "y", "1"))
		wantError(t, c.do("EXEC"), "EXECABORT")
		wantNull(t, c.do("GET", "x"))
		wantNull(t, c.do("GET", "y"))
		wantSimple(t, c.do("MULTI"), "OK")
		wantQueued(t, c.do("SET", "x", "1"))
		wantError(t, c.do("GET"), "ERR wrong number")
		wantError(t, c.do("EXEC"), "EXECABORT")
		wantNull(t, c.do("GET", "x"))
	})

	t.Run("Should not stop at a command that fails at EXEC", func(t *testing.T) {
		c.do("SET", "s", "text")
		wantSimple(t, c.do("MULTI"), "OK")
		wantQueued(t, c.do("SET", "t", "1"))
		wantQueued(t, c.do("INCR", "s"))
		wantQueued(t, c.do("SET", "u", "2"))
		got := wantArrayLen(t, c.do("EXEC"), 3)
		wantSimple(t, got[0], "OK")
		wantError(t, got[1], "ERR value is not an integer")
		wantSimple(t, got[2], "OK")
		wantBulk(t, c.do("GET", "u"), "2")
	})

	t.Run("Should handle DISCARD and the commands that are out of place", func(t *testing.T) {
		wantError(t, c.do("EXEC"), "ERR EXEC without MULTI")
		wantError(t, c.do("DISCARD"), "ERR DISCARD without MULTI")
		wantSimple(t, c.do("MULTI"), "OK")
		wantQueued(t, c.do("SET", "d", "1"))
		wantSimple(t, c.do("DISCARD"), "OK")
		wantNull(t, c.do("GET", "d"))
		wantSimple(t, c.do("MULTI"), "OK")
		wantError(t, c.do("MULTI"), "ERR MULTI calls can not be nested")
		wantError(t, c.do("WATCH", "k"), "ERR WATCH inside MULTI is not allowed")
		wantQueued(t, c.do("SET", "d", "2"))
		wantArrayLen(t, c.do("EXEC"), 1)
		wantBulk(t, c.do("GET", "d"), "2")
	})

	t.Run("Should refuse AUTH and HELLO between MULTI and EXEC", func(t *testing.T) {
		wantSimple(t, c.do("MULTI"), "OK")
		wantError(t, c.do("HELLO", "3"), "ERR Command not allowed inside a transaction")
		wantError(t, c.do("EXEC"), "EXECABORT")
		wantSimple(t, c.do("MULTI"), "OK")
		wantError(t, c.do("AUTH", "x"), "ERR Command not allowed inside a transaction")
		wantError(t, c.do("EXEC"), "EXECABORT")
	})

	t.Run("Should abort at EXEC when a watched key changed, for each way to write", func(t *testing.T) {
		writes := [][]string{
			{"SET", "w", "new"}, {"MSET", "w", "new", "z", "1"}, {"DEL", "w"}, {"INCR", "w"}, {"APPEND", "w", "x"},
			{"EXPIRE", "w", "100"}, {"GETDEL", "w"}, {"SETNX", "w", "x"}, {"GETSET", "w", "x"}, {"HSET", "w", "f", "1"}, {"SADD", "w", "m"},
		}
		for _, write := range writes {
			other.do("DEL", "w")
			if write[0] != "SETNX" && write[0] != "HSET" && write[0] != "SADD" {
				other.do("SET", "w", "10")
			}
			wantSimple(t, c.do("WATCH", "w"), "OK")
			other.do(write...)
			wantSimple(t, c.do("MULTI"), "OK")
			wantQueued(t, c.do("SET", "mark", write[0]))
			if v := c.do("EXEC"); v.Kind != resp.KindNull {
				t.Fatalf("%s did not abort the transaction: %+v", write[0], v)
			}
			if got := c.do("GET", "mark"); got.Kind == resp.KindBulk && string(got.Bytes) == write[0] {
				t.Fatalf("%s: the transaction ran after the key changed", write[0])
			}
		}
	})

	t.Run("Should see a plain SET from a pipeline as a change", func(t *testing.T) {
		other.do("SET", "pw", "1")
		wantSimple(t, c.do("WATCH", "pw"), "OK")
		other.send("SET", "pw", "2")
		other.send("SET", "pw2", "2")
		other.read()
		other.read()
		wantSimple(t, c.do("MULTI"), "OK")
		wantQueued(t, c.do("SET", "pmark", "x"))
		wantNull(t, c.do("EXEC"))
	})

	t.Run("Should run when the watched keys did not change, and forget them after EXEC", func(t *testing.T) {
		other.do("SET", "k", "1")
		wantSimple(t, c.do("WATCH", "k", "k"), "OK")
		wantSimple(t, c.do("MULTI"), "OK")
		wantQueued(t, c.do("INCR", "k"))
		wantArrayLen(t, c.do("EXEC"), 1)
		other.do("SET", "k", "9")
		wantSimple(t, c.do("MULTI"), "OK")
		wantQueued(t, c.do("INCR", "k"))
		got := wantArrayLen(t, c.do("EXEC"), 1)
		wantInt(t, got[0], 10)
	})

	t.Run("Should not abort after UNWATCH", func(t *testing.T) {
		other.do("SET", "uw", "1")
		wantSimple(t, c.do("WATCH", "uw"), "OK")
		other.do("SET", "uw", "2")
		wantSimple(t, c.do("UNWATCH"), "OK")
		wantSimple(t, c.do("MULTI"), "OK")
		wantQueued(t, c.do("SET", "uw", "3"))
		wantArrayLen(t, c.do("EXEC"), 1)
	})

	t.Run("Should abort when a watched key expires, and not for a key that was already expired", func(t *testing.T) {
		other.do("SET", "ex", "1", "EX", "10")
		wantSimple(t, c.do("WATCH", "ex"), "OK")
		clock.advance(11 * time.Second)
		wantSimple(t, c.do("MULTI"), "OK")
		wantQueued(t, c.do("SET", "emark", "x"))
		wantNull(t, c.do("EXEC"))
		wantSimple(t, c.do("WATCH", "ex"), "OK")
		wantSimple(t, c.do("MULTI"), "OK")
		wantQueued(t, c.do("SET", "emark", "y"))
		wantArrayLen(t, c.do("EXEC"), 1)
	})

	t.Run("Should reply with a null in RESP3 when a watched key changed", func(t *testing.T) {
		d := dial(t, addr)
		d.do("HELLO", "3")
		other.do("SET", "r3", "1")
		wantSimple(t, d.do("WATCH", "r3"), "OK")
		other.do("SET", "r3", "2")
		wantSimple(t, d.do("MULTI"), "OK")
		wantQueued(t, d.do("GET", "r3"))
		wantNull(t, d.do("EXEC"))
		wantSimple(t, d.do("MULTI"), "OK")
		wantQueued(t, d.do("GET", "r3"))
		wantArrayLen(t, d.do("EXEC"), 1)
	})

	t.Run("Should take a transaction that is sent in one pipeline", func(t *testing.T) {
		d := dial(t, addr)
		d.send("SET", "before", "1")
		d.send("MULTI")
		d.send("SET", "p1", "a")
		d.send("SET", "p2", "b")
		d.send("EXEC")
		d.send("SET", "after", "2")
		wantSimple(t, d.read(), "OK")
		wantSimple(t, d.read(), "OK")
		wantQueued(t, d.read())
		wantQueued(t, d.read())
		got := wantArrayLen(t, d.read(), 2)
		wantSimple(t, got[0], "OK")
		wantSimple(t, got[1], "OK")
		wantSimple(t, d.read(), "OK")
		wantBulk(t, d.do("GET", "p2"), "b")
	})

	t.Run("Should run a command of each kind of write inside EXEC without a deadlock", func(t *testing.T) {
		d := dial(t, addr)
		cmds := [][]string{
			{"SET", "k1", "1"}, {"SET", "k1", "2", "NX"}, {"SET", "k1", "3", "XX", "GET"}, {"MSET", "k2", "a", "k3", "b"}, {"DEL", "k2", "k3"},
			{"INCR", "n1"}, {"INCRBY", "n1", "4"}, {"APPEND", "k4", "x"}, {"SETNX", "k5", "x"}, {"GETSET", "k5", "y"}, {"GETDEL", "k5"},
			{"EXPIRE", "k1", "100"}, {"PERSIST", "k1"}, {"HSET", "h1", "a", "1"}, {"HSETNX", "h1", "b", "2"}, {"HINCRBY", "h1", "n", "1"},
			{"HDEL", "h1", "a"}, {"SADD", "s1", "a", "b"}, {"SREM", "s1", "a"}, {"SPOP", "s1"}, {"SADD", "s2", "x"}, {"SUNIONSTORE", "s3", "s2", "s1"},
		}
		d.do("MULTI")
		for _, cmd := range cmds {
			wantQueued(t, d.do(cmd...))
		}
		done := make(chan resp.Value, 1)
		go func() { done <- d.do("EXEC") }()
		select {
		case v := <-done:
			wantArrayLen(t, v, len(cmds))
		case <-time.After(10 * time.Second):
			t.Fatal("EXEC did not finish, a handler took a lock that EXEC holds")
		}
	})

	t.Run("Should not lose an update when many clients use WATCH", func(t *testing.T) {
		c.do("SET", "counter", "0")
		const workers, each = 8, 25
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				w := dial(t, addr)
				for range each {
					for {
						w.do("WATCH", "counter")
						n, _ := strconv.Atoi(string(w.do("GET", "counter").Bytes))
						w.do("MULTI")
						w.do("SET", "counter", strconv.Itoa(n+1))
						if v := w.do("EXEC"); v.Kind == resp.KindArray {
							break
						}
					}
				}
			}()
		}
		wg.Wait()
		wantBulk(t, c.do("GET", "counter"), strconv.Itoa(workers*each))
	})

	t.Run("Should not deadlock when two EXEC lock the same keys in opposite order", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				w := dial(t, addr)
				a, b := "dl-a", "dl-b"
				if i == 1 {
					a, b = b, a
				}
				for range 100 {
					w.do("MULTI")
					w.do("INCR", a)
					w.do("INCR", b)
					w.do("EXEC")
				}
			}()
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatal("the two EXEC deadlocked")
		}
		wantBulk(t, c.do("GET", "dl-a"), "200")
	})
}

func TestTransactionLimits(t *testing.T) {
	t.Run("Should refuse a command when the queue has too many", func(t *testing.T) {
		_, addr := startServer(t, Options{MaxTxCommands: 3, SweepInterval: -1})
		c := dial(t, addr)
		c.do("MULTI")
		for range 3 {
			wantQueued(t, c.do("SET", "a", "1"))
		}
		wantError(t, c.do("SET", "a", "1"), "ERR transaction queue is full")
		wantError(t, c.do("EXEC"), "EXECABORT")
	})

	t.Run("Should refuse a command when the queue has too many bytes", func(t *testing.T) {
		_, addr := startServer(t, Options{MaxTxBytes: 40, SweepInterval: -1})
		c := dial(t, addr)
		c.do("MULTI")
		wantQueued(t, c.do("SET", "a", "0123456789"))
		wantError(t, c.do("SET", "b", "0123456789012345678901234567890123456789"), "ERR transaction queue is full")
		wantError(t, c.do("EXEC"), "EXECABORT")
	})

	t.Run("Should refuse a WATCH of too many keys", func(t *testing.T) {
		_, addr := startServer(t, Options{MaxWatch: 2, SweepInterval: -1})
		c := dial(t, addr)
		wantSimple(t, c.do("WATCH", "a", "b"), "OK")
		wantError(t, c.do("WATCH", "c"), "ERR too many watched keys")
		wantSimple(t, c.do("WATCH", "a"), "OK")
	})

	t.Run("Should refuse a reply of EXEC that is too large", func(t *testing.T) {
		_, addr := startServer(t, Options{MaxReply: 100, SweepInterval: -1})
		c := dial(t, addr)
		c.do("SET", "big", string(make([]byte, 80)))
		c.do("MULTI")
		wantQueued(t, c.do("GET", "big"))
		wantQueued(t, c.do("GET", "big"))
		wantError(t, c.do("EXEC"), "ERR reply is too large")
	})
}

func TestTransactionCommands(t *testing.T) {
	t.Run("Should list the transaction commands", func(t *testing.T) {
		_, addr := startServer(t, Options{SweepInterval: -1})
		c := dial(t, addr)
		got := keyList(t, c.do("COMMAND", "LIST"))
		for _, name := range []string{"multi", "exec", "discard", "watch", "unwatch"} {
			if !contains(got, name) {
				t.Fatalf("COMMAND LIST has no %s", name)
			}
		}
		_ = fmt.Sprint
	})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestTransactionsWithUsers(t *testing.T) {
	t.Run("Should abort at EXEC when a command is not allowed while queueing", func(t *testing.T) {
		reg, _ := aclFile(t, "user default off\nuser t on nopass ~* +@all -@hash\n")
		_, addr := startServer(t, Options{Access: reg, SweepInterval: -1})
		c := dial(t, addr)
		wantSimple(t, c.do("AUTH", "t", "x"), "OK")
		wantSimple(t, c.do("MULTI"), "OK")
		wantQueued(t, c.do("SET", "a", "1"))
		wantError(t, c.do("HSET", "h", "f", "1"), "NOPERM")
		wantError(t, c.do("EXEC"), "EXECABORT")
		wantNull(t, c.do("GET", "a"))
	})

	t.Run("Should check the permissions again at EXEC", func(t *testing.T) {
		reg, path := aclFile(t, "user default off\nuser t on nopass ~* +@all\n")
		_, addr := startServer(t, Options{Access: reg, SweepInterval: -1})
		c := dial(t, addr)
		wantSimple(t, c.do("AUTH", "t", "x"), "OK")
		wantSimple(t, c.do("MULTI"), "OK")
		wantQueued(t, c.do("SET", "a", "1"))
		wantQueued(t, c.do("GET", "a"))
		if err := os.WriteFile(path, []byte("user default off\nuser t on nopass ~* +@read +@connection +@transaction\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := reg.Reload(); err != nil {
			t.Fatal(err)
		}
		got := wantArrayLen(t, c.do("EXEC"), 2)
		wantError(t, got[0], "NOPERM")
		wantNull(t, got[1])
	})

	t.Run("Should not let a client that has not signed in start a transaction", func(t *testing.T) {
		reg, _ := aclFile(t, "user default off\nuser t on nopass ~* +@all\n")
		_, addr := startServer(t, Options{Access: reg, SweepInterval: -1})
		c := dial(t, addr)
		wantError(t, c.do("MULTI"), "NOAUTH")
		wantError(t, c.do("WATCH", "k"), "NOAUTH")
	})
}
