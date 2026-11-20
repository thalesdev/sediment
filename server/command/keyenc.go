// SPDX-License-Identifier: MIT

package command

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/thalesdev/sediment/internal/shale"
)

// The engine keys of the server. Every key starts with a class and a hash slot,
// and the rest depends on the class.
//
//	SYSTEM   [0x00][0xFFFF][name]
//	KEY      [0x01][slot][user key]
//	ELEMENT  [0x02][slot][uid u64][sub u8][field]
//	OWNER    [0x03][slot][uid u64]
//
// A KEY record exists for each logical key, whatever its type. An ELEMENT record
// is one field of a hash or one member of a set, named by the id of the
// collection and not by the user key, so a collection can be renamed without a
// copy, and a field never collides with another key. An OWNER record exists for
// each collection and keeps its user key, so that a sweeper can find the
// collections whose KEY record is gone. The slot is the one of Redis Cluster, and
// the integers are big endian, so a slot and a collection are ranges of keys.
const (
	classSystem  byte = 0x00
	classKey     byte = 0x01
	classElement byte = 0x02
	classOwner   byte = 0x03

	slotCount  = 16384
	slotSystem = 0xFFFF

	keyHeaderLen = 3
)

var errBadKey = errors.New("command: engine key is not valid")

var crc16Table = func() (t [256]uint16) {
	for i := range t {
		c := uint16(i) << 8
		for range 8 {
			if c&0x8000 != 0 {
				c = c<<1 ^ 0x1021
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

// crc16 returns the CRC-16 of b with the XMODEM parameters that Redis Cluster
// uses.
func crc16(b []byte) uint16 {
	var c uint16
	for _, x := range b {
		c = c<<8 ^ crc16Table[byte(c>>8)^x]
	}
	return c
}

// hashTag returns the part of key that decides its slot: the bytes between the
// first { and the next }, if there is at least one, and the whole key otherwise.
func hashTag(key []byte) []byte {
	start := bytes.IndexByte(key, '{')
	if start < 0 {
		return key
	}
	end := bytes.IndexByte(key[start+1:], '}')
	if end <= 0 {
		return key
	}
	return key[start+1 : start+1+end]
}

// slotOf returns the hash slot of a user key.
func slotOf(key []byte) uint16 {
	return crc16(hashTag(key)) % slotCount
}

// engineKey returns a new key of the class, in the slot, followed by rest.
func engineKey(class byte, slot uint16, rest ...[]byte) []byte {
	n := keyHeaderLen
	for _, r := range rest {
		n += len(r)
	}
	k := make([]byte, 0, n)
	k = append(k, class)
	k = binary.BigEndian.AppendUint16(k, slot)
	for _, r := range rest {
		k = append(k, r...)
	}
	return k
}

// keyOf returns the engine key of the KEY record of a user key.
func keyOf(user []byte) []byte {
	return engineKey(classKey, slotOf(user), user)
}

// userKeyOf returns the user key of the engine key of a KEY record. It returns an
// error, and never panics, if k is not one.
func userKeyOf(k []byte) ([]byte, error) {
	if len(k) < keyHeaderLen || k[0] != classKey {
		return nil, errBadKey
	}
	return k[keyHeaderLen:], nil
}

// uidBytes returns the big endian form of a collection id.
func uidBytes(uid uint64) []byte {
	return binary.BigEndian.AppendUint64(nil, uid)
}

// elementPrefix returns the start of the keys of all the elements of a collection.
func elementPrefix(slot uint16, uid uint64) []byte {
	return engineKey(classElement, slot, uidBytes(uid))
}

// elementKey returns the engine key of one element of a collection. sub says what
// kind of element it is, and is 0 for a field of a hash and a member of a set.
func elementKey(slot uint16, uid uint64, sub byte, field []byte) []byte {
	return engineKey(classElement, slot, uidBytes(uid), []byte{sub}, field)
}

// ownerKey returns the engine key of the OWNER record of a collection.
func ownerKey(slot uint16, uid uint64) []byte {
	return engineKey(classOwner, slot, uidBytes(uid))
}

// systemKey returns the engine key of a SYSTEM record.
func systemKey(name string) []byte {
	return engineKey(classSystem, slotSystem, []byte(name))
}

// classBounds returns the options of an iterator that sees only the records of a
// class.
func classBounds(class byte) *shale.IterOptions {
	return &shale.IterOptions{Lower: []byte{class}, Upper: []byte{class + 1}}
}

// elementBounds returns the options of an iterator that sees only the elements of
// a collection.
func elementBounds(slot uint16, uid uint64) *shale.IterOptions {
	lower := elementPrefix(slot, uid)
	return &shale.IterOptions{Lower: lower, Upper: prefixEnd(lower)}
}

// EncodeString returns the engine key and the stored value of a string key that
// does not expire. It is for code that loads a store without a server, such as a
// benchmark.
func EncodeString(key, value []byte) (k, stored []byte) {
	return keyOf(key), encodeRecord(typeString, 0, value)
}
