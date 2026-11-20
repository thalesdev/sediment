// SPDX-License-Identifier: MIT

package command

import (
	"hash/maphash"
	"slices"
	"sync"
)

// lockStripes is the number of locks in a Locks.
const lockStripes = 256

// Locks is a fixed set of read-write locks. A key maps to one of them. A write
// that only replaces a value, such as SET or MSET, takes the shared side, so many
// of them can hold the same stripe at once and they share the group commit of the
// engine. A command that reads the key and then writes it, such as SET NX or DEL,
// takes the exclusive side, which waits for the shared holders and keeps them out,
// so it is atomic against every other write of the same key. Reads do not take a
// lock.
//
// A shared holder keeps its stripes until its write returns, and that includes the
// wait for the log. With an exclusive lock for every write, the commands of one
// pipeline would hold many stripes during that wait and the clients would wait
// for each other.
type Locks struct {
	seed maphash.Seed
	mu   [lockStripes]sync.RWMutex
}

// NewLocks returns a Locks.
func NewLocks() *Locks {
	return &Locks{seed: maphash.MakeSeed()}
}

// Lock takes the exclusive lock of key and returns the stripe to give to Unlock.
func (l *Locks) Lock(key []byte) int {
	i := int(maphash.Bytes(l.seed, key) % lockStripes)
	l.mu[i].Lock()
	return i
}

// Unlock releases the exclusive lock of a stripe that Lock returned.
func (l *Locks) Unlock(stripe int) {
	l.mu[stripe].Unlock()
}

// RLock takes the shared lock of key and returns the stripe to give to RUnlock.
func (l *Locks) RLock(key []byte) int {
	i := int(maphash.Bytes(l.seed, key) % lockStripes)
	l.mu[i].RLock()
	return i
}

// RUnlock releases the shared lock of a stripe that RLock returned.
func (l *Locks) RUnlock(stripe int) {
	l.mu[stripe].RUnlock()
}

// stripesOf returns the stripes of the keys, sorted and with no repeat. A caller
// that takes them in this order cannot wait for another caller that takes some of
// them in the same order. key returns the key at position i, for i from 0 to n.
func (l *Locks) stripesOf(n int, key func(i int) []byte) []int {
	stripes := make([]int, 0, n)
	for i := range n {
		stripes = append(stripes, int(maphash.Bytes(l.seed, key(i))%lockStripes))
	}
	slices.Sort(stripes)
	return slices.Compact(stripes)
}

// LockKeys takes the exclusive locks of all the keys, in the order of their
// stripes, and returns the stripes to give to UnlockStripes.
func (l *Locks) LockKeys(n int, key func(i int) []byte) []int {
	stripes := l.stripesOf(n, key)
	for _, s := range stripes {
		l.mu[s].Lock()
	}
	return stripes
}

// UnlockStripes releases the exclusive locks that LockKeys took.
func (l *Locks) UnlockStripes(stripes []int) {
	for _, s := range stripes {
		l.mu[s].Unlock()
	}
}

// LockAll takes the exclusive lock of every stripe, in order, so no write of any key
// runs until UnlockAll. A command that changes every key, such as FLUSHALL, uses
// it.
func (l *Locks) LockAll() {
	for i := range l.mu {
		l.mu[i].Lock()
	}
}

// UnlockAll releases the locks that LockAll took.
func (l *Locks) UnlockAll() {
	for i := range l.mu {
		l.mu[i].Unlock()
	}
}

// RLockKeys takes the shared locks of all the keys, in the order of their
// stripes, and returns the stripes to give to RUnlockStripes.
func (l *Locks) RLockKeys(n int, key func(i int) []byte) []int {
	stripes := l.stripesOf(n, key)
	for _, s := range stripes {
		l.mu[s].RLock()
	}
	return stripes
}

// RUnlockStripes releases the shared locks that RLockKeys took.
func (l *Locks) RUnlockStripes(stripes []int) {
	for _, s := range stripes {
		l.mu[s].RUnlock()
	}
}
