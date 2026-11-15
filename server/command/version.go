// SPDX-License-Identifier: MIT

package command

import (
	"hash/maphash"
	"sync/atomic"

	"github.com/thalesdev/sediment/internal/batch"
	"github.com/thalesdev/sediment/internal/shale"
)

// versionStripes is the number of counters. Two keys that share a counter make a
// WATCH on one of them abort when the other changes, which is a retry for the
// client and never a lost update.
const versionStripes = 1 << 16

// Versions counts the writes of each key, so that WATCH can tell if a key changed.
// A key has a counter that depends on its user key, and every write of the KEY
// record of the key adds one to it. A nil Versions counts nothing.
type Versions struct {
	seed     maphash.Seed
	counters [versionStripes]atomic.Uint64
}

// NewVersions returns counters that are all at zero.
func NewVersions() *Versions {
	return &Versions{seed: maphash.MakeSeed()}
}

func (v *Versions) index(user []byte) int {
	return int(maphash.Bytes(v.seed, user) & (versionStripes - 1))
}

// Load returns the count of a user key.
func (v *Versions) Load(user []byte) uint64 {
	if v == nil {
		return 0
	}
	return v.counters[v.index(user)].Load()
}

// bump adds one to the count of the user key of an engine key, if the engine key
// is a KEY record. The records of elements do not count, because every write of a
// collection also writes its KEY record.
func (v *Versions) bump(engine []byte) {
	user, err := userKeyOf(engine)
	if err != nil {
		return
	}
	v.counters[v.index(user)].Add(1)
}

// versionStore is a Store that counts the writes of the KEY records. It counts
// after the write returns, while the command still holds the lock of the key, so
// an EXEC that holds the lock and sees the count of a WATCH sees every write that
// came before it.
type versionStore struct {
	Store
	v *Versions
}

// WithVersions returns a Store that counts the writes of s in v.
func WithVersions(s Store, v *Versions) Store {
	return &versionStore{Store: s, v: v}
}

// Put sets the value of key and counts the write.
func (s *versionStore) Put(key, value []byte) error {
	err := s.Store.Put(key, value)
	if err == nil {
		s.v.bump(key)
	}
	return err
}

// Delete deletes key and counts the write.
func (s *versionStore) Delete(key []byte) error {
	err := s.Store.Delete(key)
	if err == nil {
		s.v.bump(key)
	}
	return err
}

// Write applies a batch and counts each write of a KEY record in it.
func (s *versionStore) Write(b *shale.Batch) error {
	err := s.Store.Write(b)
	if err == nil {
		b.Each(func(kind batch.Kind, key, _ []byte) {
			if kind != batch.KindRangeDelete {
				s.v.bump(key)
			}
		})
	}
	return err
}
