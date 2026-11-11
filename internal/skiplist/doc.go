// SPDX-License-Identifier: MIT

// Package skiplist implements an ordered in-memory map from byte-slice keys to
// byte-slice values.
//
// LockedList is safe for concurrent use. A key can be inserted one time. The
// list has no operation that deletes a key.
package skiplist
