// SPDX-License-Identifier: MIT

// Package merge combines many sorted sources into one sorted iterator.
//
// A source is anything that has the methods of shale.Iterator, for example
// a skip list or a table. New takes the sources in order of priority. The
// first source is the newest.
//
// The iterator does not remove duplicates. It returns every entry of every
// source in key order. If entries from different sources have the same key,
// the entry of the source with the lowest index comes first. The caller decides
// what to do with entries that have the same key, for example with deleted
// keys.
//
// If one source reports an error, the iterator stops and returns that error.
// It never skips a source that failed.
package merge
