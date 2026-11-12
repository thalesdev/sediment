// SPDX-License-Identifier: MIT

package shale

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/thalesdev/sediment/internal/batch"
	"github.com/thalesdev/sediment/internal/ikey"
	"github.com/thalesdev/sediment/internal/wal"
)

func queued(db *DB) int {
	db.mu.Lock()
	defer db.mu.Unlock()
	return len(db.queue)
}

func waitQueued(t testing.TB, db *DB, want int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for queued(db) < want {
		if time.Now().After(deadline) {
			t.Fatalf("only %d writes in the queue, want %d", queued(db), want)
		}
		time.Sleep(time.Millisecond)
	}
}

type blockedLog struct {
	fake    *fakeLog
	reached chan struct{}
	release chan struct{}
}

func newBlockedLog(failAt int, err error) *blockedLog {
	b := &blockedLog{fake: &fakeLog{}, reached: make(chan struct{}, 1), release: make(chan struct{})}
	b.fake.hook = func(call int) error {
		if call == 1 {
			b.reached <- struct{}{}
			<-b.release
		}
		if call == failAt {
			return err
		}
		return nil
	}
	return b
}

func startPuts(db *DB, from, n int, errs []error) *sync.WaitGroup {
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = db.Put(keyN(from+i), []byte(value100(from+i)))
		}()
	}
	return &wg
}

func decodeRecords(t *testing.T, data []byte) []batch.View {
	t.Helper()
	r := wal.NewReader(bytes.NewReader(data))
	var out []batch.View
	for {
		record, err := r.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		view, err := batch.Parse(bytes.Clone(record))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, view)
	}
}

func TestGroupCommit(t *testing.T) {
	t.Parallel()

	t.Run("Should write the writes that wait together as one record and one flush", func(t *testing.T) {
		db := openDB(t, t.TempDir(), Options{Sync: true})
		defer db.Close()
		bl := newBlockedLog(0, nil)
		swapLog(db, bl.fake)

		const followers = 7
		first := make([]error, 1)
		wgFirst := startPuts(db, 0, 1, first)
		<-bl.reached
		errs := make([]error, followers)
		wg := startPuts(db, 1, followers, errs)
		waitQueued(t, db, followers+1)
		close(bl.release)
		wg.Wait()
		wgFirst.Wait()

		for i, err := range append(first, errs...) {
			if err != nil {
				t.Fatalf("write %d: %v", i, err)
			}
		}
		if bl.fake.writes != 2 || bl.fake.syncs != 2 {
			t.Fatalf("%d calls to Write and %d syncs, want 2 and 2", bl.fake.writes, bl.fake.syncs)
		}
		if g, n := db.groups.Load(), db.grouped.Load(); g != 2 || n != followers+1 {
			t.Fatalf("%d groups with %d writes", g, n)
		}
		records := decodeRecords(t, bl.fake.data)
		if len(records) != 2 || records[0].Count() != 1 || records[1].Count() != followers {
			t.Fatalf("records are %d, want one of 1 write and one of %d writes", len(records), followers)
		}
		if records[0].Seq() != 1 || records[1].Seq() != 2 {
			t.Fatalf("first sequence numbers are %d and %d", records[0].Seq(), records[1].Seq())
		}
		seen := 0
		records[1].ForEach(func(seq uint64, kind ikey.Kind, key, value []byte) {
			if seq != uint64(2+seen) || kind != ikey.KindPut {
				t.Errorf("write %d has seq %d and kind %d", seen, seq, kind)
			}
			seen++
		})
		for i := range followers + 1 {
			mustGet(t, db, string(keyN(i)), value100(i))
		}
		if got := db.lastSeq.Load(); got != followers+1 {
			t.Fatalf("last sequence number %d, want %d", got, followers+1)
		}
	})

	t.Run("Should give the error of the log to every write of the group and refuse the later ones", func(t *testing.T) {
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		boom := errors.New("disk full")
		bl := newBlockedLog(2, boom)
		swapLog(db, bl.fake)

		first := make([]error, 1)
		wgFirst := startPuts(db, 0, 1, first)
		<-bl.reached
		const followers = 5
		errs := make([]error, followers)
		wg := startPuts(db, 1, followers, errs)
		waitQueued(t, db, followers+1)
		close(bl.release)
		wg.Wait()
		wgFirst.Wait()

		if first[0] != nil {
			t.Fatalf("the write of the first group: %v", first[0])
		}
		for i, err := range errs {
			if !errors.Is(err, boom) {
				t.Fatalf("write %d of the failed group: %v", i, err)
			}
		}
		if err := db.Put([]byte("later"), []byte("x")); !errors.Is(err, boom) {
			t.Fatalf("later Put = %v", err)
		}
		mustGet(t, db, string(keyN(0)), value100(0))
		for i := 1; i <= followers; i++ {
			mustNotFind(t, db, string(keyN(i)))
		}
	})

	t.Run("Should return ErrClosed to the writes that wait when the DB closes and keep the ones that returned nil", func(t *testing.T) {
		dir := t.TempDir()
		db := openDB(t, dir, Options{})
		bl := newBlockedLog(0, nil)
		swapLog(db, bl.fake)

		first := make([]error, 1)
		wgFirst := startPuts(db, 0, 1, first)
		<-bl.reached
		const followers = 6
		errs := make([]error, followers)
		wg := startPuts(db, 1, followers, errs)
		waitQueued(t, db, followers+1)
		closed := make(chan error, 1)
		go func() { closed <- db.Close() }()
		time.Sleep(50 * time.Millisecond)
		select {
		case <-closed:
			t.Fatal("Close returned while a group was being written")
		default:
		}
		close(bl.release)
		wg.Wait()
		wgFirst.Wait()
		select {
		case err := <-closed:
			if err != nil {
				t.Fatalf("Close = %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("Close did not return")
		}
		if first[0] != nil {
			t.Fatalf("the write that was being written: %v", first[0])
		}
		for i, err := range errs {
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("write %d that waited: %v", i, err)
			}
		}
	})

	t.Run("Should limit the size of a group", func(t *testing.T) {
		db := &DB{}
		mk := func(size int) *writer { return &writer{key: []byte("k"), value: make([]byte, size)} }
		cases := []struct {
			name  string
			sizes []int
			want  int
		}{
			{"small writes stop at the growth of a small first write", repeatSize(100, 5000), (100 + writeOverhead + 1 + smallGroupGrowth) / (100 + writeOverhead + 1)},
			{"a large first write uses the limit of the group", []int{500 << 10, 300 << 10, 300 << 10, 300 << 10}, 2},
			{"a group has at least one write", []int{2 << 20, 10}, 1},
		}
		for _, tc := range cases {
			db.queue = nil
			for _, s := range tc.sizes {
				db.queue = append(db.queue, mk(s))
			}
			group := db.pick()
			total := 0
			for _, g := range group {
				total += g.size()
			}
			if len(group) != tc.want {
				t.Fatalf("%s: group of %d writes, want %d", tc.name, len(group), tc.want)
			}
			if len(group) > 1 && total > maxGroupBytes {
				t.Fatalf("%s: group of %d bytes", tc.name, total)
			}
		}
	})

	t.Run("Should flush less than once for each write when many writers wait on the flush", func(t *testing.T) {
		if testing.Short() {
			t.Skip("the test needs real flushes")
		}
		db := openDB(t, t.TempDir(), Options{Sync: true})
		defer db.Close()
		const writers, each = 8, 40
		var wg sync.WaitGroup
		for w := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range each {
					if err := db.Put([]byte(fmt.Sprintf("w%d-%04d", w, i)), []byte("v")); err != nil {
						t.Errorf("Put: %v", err)
						return
					}
				}
			}()
		}
		wg.Wait()
		if g, n := db.groups.Load(), db.grouped.Load(); n != writers*each || g >= n {
			t.Fatalf("%d groups for %d writes, the writes did not share the flush", g, n)
		}
		for w := range writers {
			for i := range each {
				mustGet(t, db, fmt.Sprintf("w%d-%04d", w, i), "v")
			}
		}
	})
}

func repeatSize(size, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = size
	}
	return out
}
