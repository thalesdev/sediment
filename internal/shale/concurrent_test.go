// SPDX-License-Identifier: MIT

package shale

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConcurrent(t *testing.T) {
	t.Parallel()
	t.Run("Should give each reader a value that was written, and never go back for a key", func(t *testing.T) {
		const writers, readers, keysPerWriter = 4, 4, 8
		rounds := sweepStep(600, 150)
		db := openDB(t, t.TempDir(), Options{})
		defer db.Close()

		var wg sync.WaitGroup
		stop := make(chan struct{})
		for w := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range rounds {
					for k := range keysPerWriter {
						key := fmt.Appendf(nil, "w%d-k%d", w, k)
						value := fmt.Appendf(nil, "%d:%d:%d", w, k, i)
						if err := db.Put(key, value); err != nil {
							t.Errorf("Put: %v", err)
							return
						}
					}
				}
			}()
		}

		var readerWG sync.WaitGroup
		for r := range readers {
			readerWG.Add(1)
			go func() {
				defer readerWG.Done()
				rng := rand.New(rand.NewPCG(uint64(r), 1))
				last := map[string]int{}
				for {
					select {
					case <-stop:
						return
					default:
					}
					w, k := rng.IntN(writers), rng.IntN(keysPerWriter)
					key := fmt.Sprintf("w%d-k%d", w, k)
					got, err := db.Get([]byte(key))
					seen, ok := last[key]
					if errors.Is(err, ErrNotFound) {
						if ok {
							t.Errorf("Get(%s) went back to not found", key)
							return
						}
						continue
					}
					if err != nil {
						t.Errorf("Get(%s): %v", key, err)
						return
					}
					var gw, gk, gi int
					if _, err := fmt.Sscanf(string(got), "%d:%d:%d", &gw, &gk, &gi); err != nil || gw != w || gk != k || gi < 0 || gi >= rounds {
						t.Errorf("Get(%s) = %q, which was not written for this key", key, got)
						return
					}
					if ok && gi < seen {
						t.Errorf("Get(%s) went back from round %d to %d", key, seen, gi)
						return
					}
					last[key] = gi
				}
			}()
		}
		wg.Wait()
		close(stop)
		readerWG.Wait()

		for w := range writers {
			for k := range keysPerWriter {
				mustGet(t, db, fmt.Sprintf("w%d-k%d", w, k), fmt.Sprintf("%d:%d:%d", w, k, rounds-1))
			}
		}
	})

	t.Run("Should keep every write that returned nil when Close runs during the writes", func(t *testing.T) {
		const writers = 6
		dir := t.TempDir()
		db := openDB(t, dir, Options{})

		acked := make([]int, writers)
		started := make(chan struct{}, writers)
		var wg sync.WaitGroup
		for w := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				started <- struct{}{}
				for i := 0; ; i++ {
					err := db.Put(keyN(w*1_000_000+i), keyN(i))
					if errors.Is(err, ErrClosed) {
						return
					}
					if err != nil {
						t.Errorf("Put: %v", err)
						return
					}
					acked[w] = i + 1
				}
			}()
		}
		for range writers {
			<-started
		}
		for db.lastSeq.Load() < 200 {
		}
		closeDB(t, db)
		wg.Wait()

		db = openDB(t, dir, Options{})
		defer db.Close()
		total := 0
		for w := range writers {
			for i := range acked[w] {
				got, err := db.Get(keyN(w*1_000_000 + i))
				if err != nil || string(got) != string(keyN(i)) {
					t.Fatalf("write %d of writer %d was acknowledged but Get = %q, %v", i, w, got, err)
				}
			}
			total += acked[w]
		}
		if total < 200 {
			t.Fatalf("only %d writes were acknowledged", total)
		}
		if got := db.lastSeq.Load(); got < uint64(total) {
			t.Fatalf("last sequence number %d is below the %d acknowledged writes", got, total)
		}
	})
}

func TestConcurrentFlush(t *testing.T) {
	t.Parallel()
	t.Run("Should never miss a write that returned, with flushes during the reads", func(t *testing.T) {
		const writers, readers, keysPerWriter = 3, 4, 6
		rounds := sweepStep(300, 100)
		db := openDB(t, t.TempDir(), Options{MemtableSize: 4096})
		defer db.Close()

		var acked [writers][keysPerWriter]atomic.Int64
		for w := range writers {
			for k := range keysPerWriter {
				acked[w][k].Store(-1)
			}
		}
		pad := strings.Repeat("p", 90)
		var wg sync.WaitGroup
		for w := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range rounds {
					for k := range keysPerWriter {
						key := []byte(fmt.Sprintf("w%d-k%d", w, k))
						if err := db.Put(key, []byte(fmt.Sprintf("%d:%d:%d:%s", w, k, i, pad))); err != nil {
							t.Errorf("Put: %v", err)
							return
						}
						acked[w][k].Store(int64(i))
					}
				}
			}()
		}

		stop := make(chan struct{})
		var readerWG sync.WaitGroup
		for r := range readers {
			readerWG.Add(1)
			go func() {
				defer readerWG.Done()
				rng := rand.New(rand.NewPCG(uint64(r), 9))
				for {
					select {
					case <-stop:
						return
					default:
					}
					w, k := rng.IntN(writers), rng.IntN(keysPerWriter)
					floor := acked[w][k].Load()
					got, err := db.Get([]byte(fmt.Sprintf("w%d-k%d", w, k)))
					if errors.Is(err, ErrNotFound) {
						if floor >= 0 {
							t.Errorf("Get(w%d-k%d) found nothing after the write %d returned", w, k, floor)
							return
						}
						continue
					}
					if err != nil {
						t.Errorf("Get: %v", err)
						return
					}
					var gw, gk, gi int
					if _, err := fmt.Sscanf(string(got), "%d:%d:%d:", &gw, &gk, &gi); err != nil || gw != w || gk != k {
						t.Errorf("Get(w%d-k%d) = %q", w, k, got)
						return
					}
					if int64(gi) < floor {
						t.Errorf("Get(w%d-k%d) gave round %d after round %d returned", w, k, gi, floor)
						return
					}
				}
			}()
		}
		wg.Wait()
		close(stop)
		readerWG.Wait()
		waitIdle(t, db)
		if m := db.Metrics(); m.FlushBytes < 5*4096 || m.Compactions == 0 {
			t.Fatalf("Metrics = %+v, the test does not cover flushes and compactions", m)
		}
		for w := range writers {
			for k := range keysPerWriter {
				got, err := db.Get([]byte(fmt.Sprintf("w%d-k%d", w, k)))
				if want := fmt.Sprintf("%d:%d:%d:%s", w, k, rounds-1, pad); err != nil || string(got) != want {
					t.Fatalf("final Get(w%d-k%d) = %q, %v", w, k, got, err)
				}
			}
		}
		scanTables(t, db)
	})
}
