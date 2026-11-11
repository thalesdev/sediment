// SPDX-License-Identifier: MIT

// Package sediment is a key-value storage engine that uses a log-structured
// merge-tree. It stores its tables in the LevelDB table format.
//
// This package defines the Iterator interface. The in-memory list, the tables
// and the merged views of the engine implement it.
package sediment
