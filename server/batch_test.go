// SPDX-License-Identifier: MIT

package server

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
)

func pipe(t *testing.T, c *client, cmds ...[]string) {
	t.Helper()
	items := make([]resp.Value, 0, len(cmds))
	for _, cmd := range cmds {
		args := make([]resp.Value, len(cmd))
		for i, a := range cmd {
			args[i] = resp.BulkString(a)
		}
		items = append(items, resp.Array(args...))
	}
	for _, it := range items {
		if err := c.w.WriteValue(it); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.w.Flush(); err != nil {
		t.Fatal(err)
	}
}

type failingStore struct {
	*shale.DB
	fail bool
}

func (f *failingStore) Write(b *shale.Batch) error {
	if f.fail {
		return errors.New("disk /var/lib/secret/path is full")
	}
	return f.DB.Write(b)
}

func TestBatchedSet(t *testing.T) {
	_, addr := startServer(t, Options{})

	t.Run("Should answer a mixed pipeline in order", func(t *testing.T) {
		c := dial(t, addr)
		pipe(t, c,
			[]string{"SET", "a", "1"}, []string{"SET", "b", "2"}, []string{"GET", "a"},
			[]string{"SET", "a", "3"}, []string{"GET", "a"}, []string{"SET", "a", "4", "NX"},
			[]string{"SET", "c", "5"}, []string{"PING"}, []string{"GET", "b"}, []string{"GET", "c"},
		)
		wantSimple(t, c.read(), "OK")
		wantSimple(t, c.read(), "OK")
		wantBulk(t, c.read(), "1")
		wantSimple(t, c.read(), "OK")
		wantBulk(t, c.read(), "3")
		wantNull(t, c.read())
		wantSimple(t, c.read(), "OK")
		wantSimple(t, c.read(), "PONG")
		wantBulk(t, c.read(), "2")
		wantBulk(t, c.read(), "5")
	})

	t.Run("Should let the last SET of a key win", func(t *testing.T) {
		c := dial(t, addr)
		pipe(t, c, []string{"SET", "dup", "1"}, []string{"SET", "dup", "2"}, []string{"SET", "dup", "3"}, []string{"GET", "dup"})
		for range 3 {
			wantSimple(t, c.read(), "OK")
		}
		wantBulk(t, c.read(), "3")
	})

	t.Run("Should accept SET in any case", func(t *testing.T) {
		c := dial(t, addr)
		pipe(t, c, []string{"set", "lc", "1"}, []string{"SeT", "mc", "2"}, []string{"SET", "uc", "3"})
		for range 3 {
			wantSimple(t, c.read(), "OK")
		}
		wantBulk(t, c.do("GET", "mc"), "2")
	})

	t.Run("Should give an error to a key that is too large and keep the rest", func(t *testing.T) {
		c := dial(t, addr)
		pipe(t, c, []string{"SET", "ok1", "1"}, []string{"SET", strings.Repeat("k", 64<<10+1), "x"}, []string{"SET", "ok2", "2"}, []string{"GET", "ok1"}, []string{"GET", "ok2"})
		wantSimple(t, c.read(), "OK")
		wantError(t, c.read(), "ERR key is too large")
		wantSimple(t, c.read(), "OK")
		wantBulk(t, c.read(), "1")
		wantBulk(t, c.read(), "2")
	})

	t.Run("Should run more commands than one batch holds", func(t *testing.T) {
		c := dial(t, addr)
		const n = 1000
		cmds := make([][]string, 0, 2*n)
		for i := range n {
			cmds = append(cmds, []string{"SET", fmt.Sprintf("many%d", i), fmt.Sprint(i)})
		}
		for i := range n {
			cmds = append(cmds, []string{"GET", fmt.Sprintf("many%d", i)})
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			pipe(t, c, cmds...)
		}()
		for range n {
			wantSimple(t, c.read(), "OK")
		}
		for i := range n {
			wantBulk(t, c.read(), fmt.Sprint(i))
		}
		<-done
	})

	t.Run("Should run large values in a pipeline", func(t *testing.T) {
		c := dial(t, addr)
		big := strings.Repeat("v", 600<<10)
		pipe(t, c, []string{"SET", "big1", big}, []string{"SET", "big2", big}, []string{"SET", "big3", big}, []string{"GET", "big2"})
		for range 3 {
			wantSimple(t, c.read(), "OK")
		}
		wantBulk(t, c.read(), big)
	})

	t.Run("Should answer the SET commands before the rest of a partial command arrives", func(t *testing.T) {
		c := dial(t, addr)
		_, err := c.nc.Write([]byte("*3\r\n$3\r\nSET\r\n$1\r\np\r\n$1\r\n1\r\n*3\r\n$3\r\nSET\r\n$1\r\nq\r\n$1\r\n2\r\n*3\r\n$3\r\nSET\r\n$1\r\nr"))
		if err != nil {
			t.Fatal(err)
		}
		wantSimple(t, c.read(), "OK")
		wantSimple(t, c.read(), "OK")
		if _, err := c.nc.Write([]byte("\r\n$1\r\n3\r\n")); err != nil {
			t.Fatal(err)
		}
		wantSimple(t, c.read(), "OK")
		wantBulk(t, c.do("GET", "r"), "3")
	})

	t.Run("Should answer the SET commands and then close on a protocol error", func(t *testing.T) {
		c := dial(t, addr)
		_, err := c.nc.Write([]byte("*3\r\n$3\r\nSET\r\n$2\r\npe\r\n$1\r\n1\r\n*3\r\n$3\r\nSET\r\n$2\r\npf\r\n$1\r\n2\r\n*x\r\n"))
		if err != nil {
			t.Fatal(err)
		}
		wantSimple(t, c.read(), "OK")
		wantSimple(t, c.read(), "OK")
		wantError(t, c.read(), "ERR Protocol error")
		if !c.closed() {
			t.Fatal("the connection is open")
		}
		wantBulk(t, dial(t, addr).do("GET", "pf"), "2")
	})

	t.Run("Should report how many commands each batch held", func(t *testing.T) {
		c := dial(t, addr)
		batches, cmds := stat(t, c, "set_batches"), stat(t, c, "set_batched_commands")
		pipe(t, c, []string{"SET", "r1", "1"}, []string{"SET", "r2", "2"}, []string{"SET", "r3", "3"}, []string{"SET", "r4", "4"})
		for range 4 {
			wantSimple(t, c.read(), "OK")
		}
		gotBatches, gotCmds := stat(t, c, "set_batches")-batches, stat(t, c, "set_batched_commands")-cmds
		if gotCmds != 4 || gotBatches < 1 || gotBatches > 4 {
			t.Fatalf("got %d commands in %d batches, want 4 commands", gotCmds, gotBatches)
		}
		if stat(t, c, "read_calls") == 0 || stat(t, c, "read_bytes") == 0 {
			t.Fatal("the reads were not counted")
		}
	})

	t.Run("Should count each command of a batch", func(t *testing.T) {
		c := dial(t, addr)
		before := stat(t, c, "total_commands_processed")
		pipe(t, c, []string{"SET", "s1", "1"}, []string{"SET", "s2", "2"}, []string{"SET", "s3", "3"})
		for range 3 {
			wantSimple(t, c.read(), "OK")
		}
		after := stat(t, c, "total_commands_processed")
		if after-before != 4 {
			t.Fatalf("the counter grew by %d, want 4 (3 SET and the INFO)", after-before)
		}
	})
}

func stat(t *testing.T, c *client, name string) int {
	t.Helper()
	for _, line := range strings.Split(string(c.do("INFO", "stats").Bytes), "\r\n") {
		if v, ok := strings.CutPrefix(line, name+":"); ok {
			var n int
			fmt.Sscan(v, &n)
			return n
		}
	}
	t.Fatalf("no %s", name)
	return 0
}

func TestBatchedSetSecurity(t *testing.T) {
	reg, _ := aclFile(t, usersFile(t))
	_, addr := startServer(t, Options{Access: reg})

	t.Run("Should refuse SET commands before sign in", func(t *testing.T) {
		c := dial(t, addr)
		pipe(t, c, []string{"SET", "k", "1"}, []string{"SET", "k", "2"}, []string{"SET", "k", "3"})
		for range 3 {
			wantError(t, c.read(), "NOAUTH")
		}
	})

	t.Run("Should refuse SET commands to a user that cannot write", func(t *testing.T) {
		c := dial(t, addr)
		wantSimple(t, c.do("AUTH", "reader", "reader-pass"), "OK")
		pipe(t, c, []string{"SET", "k", "1"}, []string{"SET", "k", "2"}, []string{"GET", "k"})
		wantError(t, c.read(), "NOPERM")
		wantError(t, c.read(), "NOPERM")
		wantNull(t, c.read())
	})

	t.Run("Should batch the SET commands of a user that can write", func(t *testing.T) {
		c := dial(t, addr)
		wantSimple(t, c.do("AUTH", "app", "app-pass"), "OK")
		pipe(t, c, []string{"SET", "k", "1"}, []string{"SET", "k", "2"}, []string{"GET", "k"})
		wantSimple(t, c.read(), "OK")
		wantSimple(t, c.read(), "OK")
		wantBulk(t, c.read(), "2")
	})

	t.Run("Should use the limits of a client that has not signed in", func(t *testing.T) {
		c := dial(t, addr)
		_, _ = c.nc.Write([]byte("*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$4000\r\n"))
		wantError(t, c.read(), "ERR Protocol error")
	})
}

func TestBatchedSetFailure(t *testing.T) {
	db, err := shale.Open(t.TempDir(), shale.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &failingStore{DB: db}
	_, addr := startServer(t, Options{Store: store})

	t.Run("Should give the same storage error to every SET and hide the cause", func(t *testing.T) {
		store.fail = true
		c := dial(t, addr)
		pipe(t, c, []string{"SET", "a", "1"}, []string{"SET", "b", "2"}, []string{"SET", "c", "3"})
		for range 3 {
			v := c.read()
			wantError(t, v, "ERR storage error")
			if strings.Contains(string(v.Bytes), "secret") {
				t.Fatalf("the reply leaks the cause: %q", v.Bytes)
			}
		}
		store.fail = false
		wantSimple(t, c.do("SET", "a", "1"), "OK")
		wantBulk(t, c.do("GET", "a"), "1")
	})
}

func TestBatchedSetDoesNotWaitForTheNetwork(t *testing.T) {
	_, addr := startServer(t, Options{ReadTimeout: 300 * time.Millisecond})

	t.Run("Should not hold a reply while it waits for more commands", func(t *testing.T) {
		c := dial(t, addr)
		start := time.Now()
		pipe(t, c, []string{"SET", "w1", "1"}, []string{"SET", "w2", "2"})
		wantSimple(t, c.read(), "OK")
		wantSimple(t, c.read(), "OK")
		if time.Since(start) > 200*time.Millisecond {
			t.Fatalf("the replies took %v", time.Since(start))
		}
	})
}
