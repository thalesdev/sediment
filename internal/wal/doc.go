// SPDX-License-Identifier: MIT

// Package wal writes and reads a write-ahead log in the LevelDB log format.
//
// The log is a sequence of blocks of 32 KiB. A record that does not fit in the
// rest of a block is split into fragments. Each fragment has a header with a
// masked CRC-32C, a length and a type.
//
// Use NewWriter to append records and NewReader to read them. A Reader ends the
// log at the first data that is not valid.
package wal
