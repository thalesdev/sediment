// SPDX-License-Identifier: MIT

package skiplist

// maxHeight is the number of levels of the tallest node. With a branch probability
// of one in four, a list of 4^12 entries still has a node of that height.
const maxHeight = 12

// Comparator compares a and b. It returns a negative number if a is less than b,
// 0 if they are equal, and a positive number if a is greater than b.
type Comparator func(a []byte, b []byte) int
