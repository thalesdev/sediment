// SPDX-License-Identifier: MIT

// Package sstable writes and reads sorted string tables in the LevelDB table
// format, without compression.
//
// A table has data blocks, a filter block, a metaindex block, an index block
// and a footer of 48 bytes. Each block has a trailer with a type and a masked
// CRC-32C.
//
// Use NewWriter to write a table. The caller must add keys in strictly
// increasing order. Use Open to read a table. Get finds one key and reads one
// data block at most. NewIterator iterates over all entries.
package sstable
