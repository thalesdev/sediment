// SPDX-License-Identifier: MIT

package command

import (
	"hash/maphash"
	"slices"
	"sync"
)

// lockStripes is the number of locks in a Locks.
const lockStripes = 256

// Locks is a fixed set of mutexes. A key maps to one of them. Every write takes
// the lock of its key, so a read-modify-write command such as SET NX is atomic
// against any other write of the same key. Reads do not take a lock. Keys in
// different stripes still share the group commit of the engine.
type Locks struct {
	seed maphash.Seed
	mu   [lockStripes]sync.Mutex
}

// NewLocks returns a Locks.
func NewLocks() *Locks {
	return &Locks{seed: maphash.MakeSeed()}
}

// Lock takes the lock of key and returns the stripe to give to Unlock.
func (l *Locks) Lock(key []byte) int {
	i := int(maphash.Bytes(l.seed, key) % lockStripes)
	l.mu[i].Lock()
	return i
}

// Unlock releases a stripe that Lock returned.
func (l *Locks) Unlock(stripe int) {
	l.mu[stripe].Unlock()
}

// LockKeys takes the locks of all the keys, in the order of their stripes so
// that two callers cannot wait for each other, and returns the stripes to give
// to UnlockStripes. keys returns the key at position i, for i from 0 to n.
func (l *Locks) LockKeys(n int, key func(i int) []byte) []int {
	stripes := make([]int, 0, n)
	for i := range n {
		stripes = append(stripes, int(maphash.Bytes(l.seed, key(i))%lockStripes))
	}
	slices.Sort(stripes)
	stripes = slices.Compact(stripes)
	for _, s := range stripes {
		l.mu[s].Lock()
	}
	return stripes
}

// UnlockStripes releases the stripes that LockKeys returned.
func (l *Locks) UnlockStripes(stripes []int) {
	for _, s := range stripes {
		l.mu[s].Unlock()
	}
}
