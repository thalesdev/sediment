// SPDX-License-Identifier: MIT

// Package ikey defines the internal key of the engine. It is the key format of
// LevelDB.
//
// An internal key is a user key followed by a trailer of 8 bytes. The trailer
// is a uint64 in little-endian order with the value (seq << 8) | kind. The
// sequence number seq has 56 bits. The kind has 8 bits: KindDelete is 0 and
// KindPut is 1. A delete is an ordinary entry with the kind KindDelete.
//
// Compare orders the internal keys by user key, from low to high. If the user
// keys are equal, it orders them by trailer, from high to low, so that the
// newest version of a key comes first. The order of the bytes is not the same
// as this order. Skip lists, tables and merge iterators that hold internal keys
// must use Compare.
//
// To find the newest version of a user key that a snapshot can see, seek to the
// key that AppendLookup returns and read the first entry from there.
package ikey
