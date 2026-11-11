// SPDX-License-Identifier: MIT

// Package shale is the storage engine of the project. It is a key-value store
// that uses a log-structured merge-tree and the LevelDB formats.
//
// A write goes to a write-ahead log and to a memtable. When the memtable is
// large, a goroutine writes it to a table file, and a MANIFEST names the table
// files of the database. Open reads the MANIFEST and the logs, so a database
// recovers all the writes that returned before a crash.
//
// All tables are in level 0, and there is no compaction yet. The package also
// defines the Iterator interface that the memtable, the tables and the merged
// views implement.
package shale
