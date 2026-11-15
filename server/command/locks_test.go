// SPDX-License-Identifier: MIT

package command

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLocks(t *testing.T) {
	keys := [][]byte{[]byte("a"), []byte("b"), []byte("c")}
	at := func(i int) []byte { return keys[i] }

	t.Run("Should let many shared holders in at the same time", func(t *testing.T) {
		l := NewLocks()
		var inside atomic.Int32
		var wg sync.WaitGroup
		release := make(chan struct{})
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s := l.RLockKeys(len(keys), at)
				inside.Add(1)
				<-release
				l.RUnlockStripes(s)
			}()
		}
		eventually(t, "8 shared holders at once", func() bool { return inside.Load() == 8 })
		close(release)
		wg.Wait()
	})

	t.Run("Should keep an exclusive holder away from shared ones and the other way", func(t *testing.T) {
		l := NewLocks()
		shared := l.RLock([]byte("a"))
		got := make(chan struct{})
		go func() {
			s := l.Lock([]byte("a"))
			close(got)
			l.Unlock(s)
		}()
		select {
		case <-got:
			t.Fatal("the exclusive lock was taken while a shared one was held")
		case <-time.After(50 * time.Millisecond):
		}
		l.RUnlock(shared)
		eventually(t, "the exclusive lock after the shared one", func() bool {
			select {
			case <-got:
				return true
			default:
				return false
			}
		})

		ex := l.Lock([]byte("a"))
		entered := make(chan struct{})
		go func() {
			s := l.RLock([]byte("a"))
			close(entered)
			l.RUnlock(s)
		}()
		select {
		case <-entered:
			t.Fatal("a shared lock was taken while the exclusive one was held")
		case <-time.After(50 * time.Millisecond):
		}
		l.Unlock(ex)
		eventually(t, "the shared lock after the exclusive one", func() bool {
			select {
			case <-entered:
				return true
			default:
				return false
			}
		})
	})

	t.Run("Should not deadlock when callers take the same keys in different order", func(t *testing.T) {
		l := NewLocks()
		forward := [][]byte{[]byte("a"), []byte("b"), []byte("c"), []byte("d")}
		backward := [][]byte{[]byte("d"), []byte("c"), []byte("b"), []byte("a")}
		var wg sync.WaitGroup
		for i := range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				set := forward
				if i%2 == 1 {
					set = backward
				}
				at := func(n int) []byte { return set[n] }
				for range 500 {
					if i%4 < 2 {
						s := l.LockKeys(len(set), at)
						l.UnlockStripes(s)
					} else {
						s := l.RLockKeys(len(set), at)
						l.RUnlockStripes(s)
					}
				}
			}()
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatal("deadlock")
		}
	})

	t.Run("Should take a stripe once for keys that share it", func(t *testing.T) {
		l := NewLocks()
		same := [][]byte{[]byte("k"), []byte("k"), []byte("k")}
		s := l.LockKeys(3, func(i int) []byte { return same[i] })
		if len(s) != 1 {
			t.Fatalf("got %d stripes", len(s))
		}
		l.UnlockStripes(s)
	})
}
