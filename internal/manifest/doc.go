// SPDX-License-Identifier: MIT

// Package manifest defines the record of a change of the set of table files of a
// database, and the state that a sequence of such records builds.
//
// An Edit is one record. It holds some of these fields: the number of the first
// log file that is not in a table yet, the next file number, the last sequence
// number, tables to add and tables to delete. Each field starts with a tag that
// is a varint. The tags are the ones of the LevelDB MANIFEST. A Version is the
// state after a sequence of edits.
//
// The package does not read or write files. The caller writes each encoded edit
// as one record of a log, and each record is one atomic change.
package manifest
