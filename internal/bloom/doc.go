// SPDX-License-Identifier: MIT

// Package bloom implements the bloom filter of LevelDB.
//
// A filter can show that a key is not in a set of keys. It cannot show that a
// key is in the set. A false positive is possible. A false negative is not.
// Use New to build a filter and Filter.MayContain to query it.
package bloom
