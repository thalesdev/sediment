// SPDX-License-Identifier: MIT

// Package skiplist implements an ordered in-memory map from byte-slice keys to
// byte-slice values, for the memtable of the engine.
//
// List is safe for concurrent use and nobody waits for a lock: an insert links a
// node with an atomic compare and swap at each level, and a reader only does atomic
// loads. A key can be inserted one time. The list has no operation that deletes a
// key, so it needs no way to free a node.
//
// The nodes, the keys and the values are in blocks of memory without pointers, with
// the links as indexes, so the garbage collector does not read them. The package does
// not use package unsafe.
package skiplist
