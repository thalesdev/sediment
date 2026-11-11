// SPDX-License-Identifier: MIT

package ikey

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// TrailerSize is the number of bytes that follow the user key.
	TrailerSize = 8

	// MaxSeq is the largest sequence number that an internal key can hold.
	MaxSeq uint64 = 1<<56 - 1
)

// ErrCorrupt is the base of all errors that Parse returns.
var ErrCorrupt = errors.New("ikey: corrupt internal key")

// Kind tells what an entry does to its user key.
type Kind uint8

const (
	// KindDelete is the kind of an entry that deletes its user key.
	KindDelete Kind = 0

	// KindPut is the kind of an entry that sets the value of its user key.
	KindPut Kind = 1
)

// Append appends the internal key for user, seq and kind to dst and returns the
// result. It panics if seq is larger than MaxSeq or if kind is not valid.
func Append(dst, user []byte, seq uint64, kind Kind) []byte {
	if seq > MaxSeq {
		panic("ikey: sequence number is larger than MaxSeq")
	}
	if kind > KindPut {
		panic("ikey: kind is not valid")
	}
	dst = append(dst, user...)
	return binary.LittleEndian.AppendUint64(dst, seq<<8|uint64(kind))
}

// AppendLookup appends the internal key that finds the newest version of user
// with a sequence number that is not larger than snapshot. The first entry that
// is greater than or equal to this key is that version, if the entry has the
// same user key. It panics if snapshot is larger than MaxSeq.
func AppendLookup(dst, user []byte, snapshot uint64) []byte {
	return Append(dst, user, snapshot, KindPut)
}

// Parse splits key into its user key, sequence number and kind. The user key is
// a slice of key. It returns an error that wraps ErrCorrupt if key is shorter
// than the trailer or if the kind is not valid.
func Parse(key []byte) (user []byte, seq uint64, kind Kind, err error) {
	if len(key) < TrailerSize {
		return nil, 0, 0, fmt.Errorf("%w: key of %d bytes is shorter than the trailer", ErrCorrupt, len(key))
	}
	trailer := binary.LittleEndian.Uint64(key[len(key)-TrailerSize:])
	kind = Kind(trailer & 0xff)
	if kind > KindPut {
		return nil, 0, 0, fmt.Errorf("%w: kind %d is not valid", ErrCorrupt, kind)
	}
	return key[:len(key)-TrailerSize], trailer >> 8, kind, nil
}

// UserKey returns the user key of key without a check. If key is shorter than
// the trailer, it returns key.
func UserKey(key []byte) []byte {
	if len(key) < TrailerSize {
		return key
	}
	return key[:len(key)-TrailerSize]
}

// FilterKey returns the part of key that goes into a bloom filter. It is the
// user key. Use it as the FilterKey option of a table.
func FilterKey(key []byte) []byte {
	return UserKey(key)
}

// split returns the user key and the trailer of key. A key that is shorter than
// the trailer has itself as user key and a trailer of 0.
func split(key []byte) (user []byte, trailer uint64) {
	if len(key) < TrailerSize {
		return key, 0
	}
	return key[:len(key)-TrailerSize], binary.LittleEndian.Uint64(key[len(key)-TrailerSize:])
}

// Compare orders internal keys. It compares the user keys first. If they are
// equal, the key with the larger trailer goes first. If both are equal, it
// compares the bytes. The order is total for all byte slices, also for keys
// that are not valid, and Compare returns 0 only for keys with equal bytes.
// It does not allocate.
func Compare(a, b []byte) int {
	userA, trailerA := split(a)
	userB, trailerB := split(b)
	if c := bytes.Compare(userA, userB); c != 0 {
		return c
	}
	switch {
	case trailerA > trailerB:
		return -1
	case trailerA < trailerB:
		return 1
	}
	return bytes.Compare(a, b)
}
