// SPDX-License-Identifier: MIT

package shale

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thalesdev/sediment/internal/batch"
)

const walHeaderSize = 7

func TestPutGetDelete(t *testing.T) {
	t.Run("Should store and return a value, and replace it", func(t *testing.T) {
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		mustNotFind(t, db, "a")
		mustPut(t, db, "a", "1")
		mustGet(t, db, "a", "1")
		mustPut(t, db, "a", "2")
		mustGet(t, db, "a", "2")
		mustPut(t, db, "b", "x")
		mustGet(t, db, "a", "2")
		mustGet(t, db, "b", "x")
	})

	t.Run("Should return ErrNotFound for a key that was deleted and store it again", func(t *testing.T) {
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		mustPut(t, db, "a", "1")
		mustDelete(t, db, "a")
		mustNotFind(t, db, "a")
		mustPut(t, db, "a", "3")
		mustGet(t, db, "a", "3")
		mustDelete(t, db, "a")
		mustDelete(t, db, "a")
		mustNotFind(t, db, "a")
	})

	t.Run("Should accept the delete of a key that does not exist", func(t *testing.T) {
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		mustDelete(t, db, "never")
		mustNotFind(t, db, "never")
	})

	t.Run("Should accept an empty key and an empty value", func(t *testing.T) {
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		mustPut(t, db, "", "empty key")
		mustPut(t, db, "k", "")
		mustGet(t, db, "", "empty key")
		got, err := db.Get([]byte("k"))
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("Get = %v, %v, want an empty value that is not nil", got, err)
		}
		if err := db.Put(nil, nil); err != nil {
			t.Fatal(err)
		}
		mustGet(t, db, "", "")
		mustDelete(t, db, "")
		mustNotFind(t, db, "")
	})

	t.Run("Should not mix a key with a key that has it as a prefix", func(t *testing.T) {
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		mustPut(t, db, "a", "1")
		mustPut(t, db, "ab", "2")
		mustPut(t, db, "a\x00", "3")
		mustDelete(t, db, "ab")
		mustGet(t, db, "a", "1")
		mustGet(t, db, "a\x00", "3")
		mustNotFind(t, db, "ab")
		mustNotFind(t, db, "abc")
	})

	t.Run("Should let the caller reuse the slices of Put and change the result of Get", func(t *testing.T) {
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()
		key, value := []byte("key"), []byte("value")
		if err := db.Put(key, value); err != nil {
			t.Fatal(err)
		}
		copy(key, "xxx")
		copy(value, "yyyyy")
		mustGet(t, db, "key", "value")
		got, _ := db.Get([]byte("key"))
		copy(got, "zzzzz")
		mustGet(t, db, "key", "value")
		if err := db.Delete(key); err != nil {
			t.Fatal(err)
		}
		mustGet(t, db, "key", "value")
	})
}

func TestReopen(t *testing.T) {
	t.Run("Should return after Open what was written before Close, also the deletes", func(t *testing.T) {
		dir := t.TempDir()
		db := openDB(t, dir, Options{})
		mustPut(t, db, "a", "1")
		mustPut(t, db, "b", "2")
		mustPut(t, db, "a", "3")
		mustDelete(t, db, "b")
		mustPut(t, db, "", "empty")
		closeDB(t, db)

		db = openDB(t, dir, Options{})
		defer db.Close()
		mustGet(t, db, "a", "3")
		mustNotFind(t, db, "b")
		mustGet(t, db, "", "empty")
		rec := db.Recovery()
		if rec.Logs != 1 || rec.Batches != 5 || rec.LastSeq != 5 || rec.DroppedBytes != 0 {
			t.Fatalf("Recovery = %+v", rec)
		}
	})

	t.Run("Should keep the sequence number growing, with one log and one more table at each reopen", func(t *testing.T) {
		dir := t.TempDir()
		var want int
		for round := range 6 {
			db := openDB(t, dir, Options{})
			if got := db.lastSeq.Load(); got != uint64(want) {
				t.Fatalf("round %d: last sequence number is %d, want %d", round, got, want)
			}
			for i := range want {
				mustGet(t, db, string(keyN(i)), "value")
			}
			for range 5 {
				mustPut(t, db, string(keyN(want)), "value")
				want++
			}
			closeDB(t, db)
			if n := len(logFiles(t, dir)); n != 1 {
				t.Fatalf("round %d: %d log files, want 1", round, n)
			}
			if n := len(tableFiles(t, dir)); n != round {
				t.Fatalf("round %d: %d table files, want %d", round, n, round)
			}
		}
		db := openDB(t, dir, Options{})
		defer db.Close()
		if rec := db.Recovery(); rec.Logs != 1 || rec.LastSeq != 30 || rec.Tables != 6 {
			t.Fatalf("Recovery = %+v", rec)
		}
	})

	t.Run("Should let a later write win over an earlier one in another log", func(t *testing.T) {
		dir := t.TempDir()
		for _, v := range []string{"1", "2", "3"} {
			db := openDB(t, dir, Options{})
			mustPut(t, db, "k", v)
			closeDB(t, db)
		}
		db := openDB(t, dir, Options{})
		defer db.Close()
		mustGet(t, db, "k", "3")
	})

	t.Run("Should ignore names that are not log files and number the next log after the largest", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{"notes.txt", "12.log", "x.log", "000003.log.tmp", "-00001.log"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("junk"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Mkdir(filepath.Join(dir, "000009.log"), 0o755); err != nil {
			t.Fatal(err)
		}
		db := openDB(t, dir, Options{})
		mustPut(t, db, "a", "1")
		closeDB(t, db)
		if got := logFiles(t, dir); len(got) != 1 || got[0] != 1 {
			t.Fatalf("log files %v, want [1]", got)
		}
	})

	t.Run("Should create the directory when it does not exist", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "a", "b")
		db := openDB(t, dir, Options{})
		mustPut(t, db, "k", "v")
		closeDB(t, db)
	})
}

func TestLockAndClose(t *testing.T) {
	t.Run("Should refuse a second Open of the same directory until the first is closed", func(t *testing.T) {
		dir := t.TempDir()
		db := openDB(t, dir, Options{})
		if _, err := Open(dir, Options{}); !errors.Is(err, ErrLocked) {
			t.Fatalf("second Open = %v, want ErrLocked", err)
		}
		mustPut(t, db, "k", "v")
		closeDB(t, db)
		db = openDB(t, dir, Options{})
		defer db.Close()
		mustGet(t, db, "k", "v")
	})

	t.Run("Should return ErrClosed from all methods after Close", func(t *testing.T) {
		db := openDB(t, t.TempDir(), Options{})
		mustPut(t, db, "k", "v")
		closeDB(t, db)
		if err := db.Put([]byte("a"), []byte("b")); !errors.Is(err, ErrClosed) {
			t.Fatalf("Put = %v", err)
		}
		if err := db.Delete([]byte("a")); !errors.Is(err, ErrClosed) {
			t.Fatalf("Delete = %v", err)
		}
		if _, err := db.Get([]byte("k")); !errors.Is(err, ErrClosed) {
			t.Fatalf("Get = %v", err)
		}
		if err := db.Close(); !errors.Is(err, ErrClosed) {
			t.Fatalf("second Close = %v", err)
		}
	})
}

func TestReplayErrors(t *testing.T) {
	cases := map[string][][]byte{
		"a record with a valid checksum that is not a batch":       {[]byte("this is not a batch")},
		"a batch that is cut but has a valid checksum":             {batchBytes(1, "k", "v")[:15]},
		"a sequence number that does not follow the one before it": {batchBytes(5, "a", "1"), batchBytes(5, "b", "2")},
		"a sequence number that goes back":                         {batchBytes(5, "a", "1"), batchBytes(2, "b", "2")},
		"a first sequence number of 0":                             {batchBytes(0, "a", "1")},
		"a batch with no operation":                                {append(batchBytes(1, "k", "v")[:8:8], 0, 0, 0, 0)},
	}
	for name, records := range cases {
		t.Run("Should return an error for "+name+" and free the lock", func(t *testing.T) {
			dir := t.TempDir()
			writeLog(t, dir, 1, records...)
			_, err := Open(dir, Options{})
			if err == nil {
				t.Fatal("Open succeeded")
			}
			if errors.Is(err, ErrLocked) {
				t.Fatal("Open reported a lock instead of the corruption")
			}
			if err := os.Remove(filepath.Join(dir, logName(1))); err != nil {
				t.Fatal(err)
			}
			db := openDB(t, dir, Options{})
			closeDB(t, db)
		})
	}

	t.Run("Should wrap the batch error", func(t *testing.T) {
		dir := t.TempDir()
		writeLog(t, dir, 1, []byte("junk"))
		if _, err := Open(dir, Options{}); !errors.Is(err, batch.ErrCorrupt) {
			t.Fatalf("Open = %v, want batch.ErrCorrupt", err)
		}
	})

	t.Run("Should read batches that have many operations and skip a cut end of the log", func(t *testing.T) {
		dir := t.TempDir()
		var b batch.Batch
		b.Reset(10)
		b.Put([]byte("a"), []byte("1"))
		b.Delete([]byte("a"))
		b.Put([]byte("b"), []byte("2"))
		writeLog(t, dir, 1, b.Bytes())
		db := openDB(t, dir, Options{})
		defer db.Close()
		mustNotFind(t, db, "a")
		mustGet(t, db, "b", "2")
		if got := db.lastSeq.Load(); got != 12 {
			t.Fatalf("last sequence number %d, want 12", got)
		}
	})
}

func TestRecoveryTruncation(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t, dir, Options{})
	const writes = 15
	var ends []int
	end := 0
	for i := range writes {
		key, value := string(keyN(i)), "value-"+string(bytes.Repeat([]byte{'x'}, i))
		mustPut(t, db, key, value)
		end += walHeaderSize + len(batchBytes(uint64(i+1), key, value))
		ends = append(ends, end)
	}
	closeDB(t, db)
	full, err := os.ReadFile(filepath.Join(dir, logName(1)))
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != end {
		t.Fatalf("log has %d bytes, want %d", len(full), end)
	}

	t.Run("Should recover exactly the writes that are whole, with the log cut at any length", func(t *testing.T) {
		for length := 0; length <= len(full); length++ {
			cut := t.TempDir()
			if err := os.WriteFile(filepath.Join(cut, logName(1)), full[:length], 0o644); err != nil {
				t.Fatal(err)
			}
			db := openDB(t, cut, Options{})
			whole, lastEnd := 0, 0
			for _, e := range ends {
				if e <= length {
					whole++
					lastEnd = e
				}
			}
			for i := range writes {
				if i < whole {
					mustGet(t, db, string(keyN(i)), "value-"+string(bytes.Repeat([]byte{'x'}, i)))
				} else {
					mustNotFind(t, db, string(keyN(i)))
				}
			}
			rec := db.Recovery()
			if rec.Batches != whole || rec.LastSeq != uint64(whole) || rec.DroppedBytes != length-lastEnd {
				t.Fatalf("length %d: Recovery = %+v, want %d batches and %d dropped bytes", length, rec, whole, length-lastEnd)
			}
			if length%4 == 0 || length == lastEnd {
				mustPut(t, db, "after", "crash")
				closeDB(t, db)
				db = openDB(t, cut, Options{})
				mustGet(t, db, "after", "crash")
				if got := db.lastSeq.Load(); got != uint64(whole)+1 {
					t.Fatalf("length %d: last sequence number %d, want %d", length, got, whole+1)
				}
			}
			closeDB(t, db)
		}
	})
}

func TestStickyError(t *testing.T) {
	t.Run("Should refuse all later writes after an error of the log and keep the data that it has", func(t *testing.T) {
		db := openDB(t, t.TempDir(), Options{})
		mustPut(t, db, "before", "1")
		boom := errors.New("disk full")
		swapLog(db, &fakeLog{writeErr: boom})
		err := db.Put([]byte("lost"), []byte("2"))
		if !errors.Is(err, boom) {
			t.Fatalf("Put = %v, want the error of the log", err)
		}
		if err := db.Put([]byte("again"), []byte("3")); !errors.Is(err, boom) {
			t.Fatalf("second Put = %v", err)
		}
		if err := db.Delete([]byte("before")); !errors.Is(err, boom) {
			t.Fatalf("Delete = %v", err)
		}
		mustGet(t, db, "before", "1")
		mustNotFind(t, db, "lost")
		mustNotFind(t, db, "again")
		if err := db.Close(); err != nil {
			t.Fatalf("Close = %v", err)
		}
	})
}

func TestSyncOption(t *testing.T) {
	for _, sync := range []bool{false, true} {
		name := "Should not sync the log at each write"
		if sync {
			name = "Should sync the log at each write"
		}
		t.Run(name, func(t *testing.T) {
			db := openDB(t, t.TempDir(), Options{Sync: sync})
			fake := &fakeLog{}
			swapLog(db, fake)
			for i := range 5 {
				mustPut(t, db, string(keyN(i)), "v")
			}
			mustDelete(t, db, "x")
			want := 0
			if sync {
				want = 6
			}
			if fake.syncs != want {
				t.Fatalf("got %d syncs, want %d", fake.syncs, want)
			}
		})
	}
}
