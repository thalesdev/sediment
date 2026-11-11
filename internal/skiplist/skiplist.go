// SPDX-License-Identifier: MIT

package skiplist

import "math/rand/v2"

const (
	maxHeight         = 12
	branchProbability = 0.25
)

// Comparator compares a and b. It returns a negative number if a is less than b,
// 0 if they are equal, and a positive number if a is greater than b.
type Comparator func(a []byte, b []byte) int

// heightGen generates the heights of new nodes from a seeded random number
// generator.
type heightGen struct {
	rng *rand.Rand
}

// randomHeight returns the height of a new node.
func (hg *heightGen) randomHeight() int {
	height := 1
	for height < maxHeight && hg.rng.Float64() < branchProbability {
		height++
	}
	return height
}

// newHeightGen returns a heightGen that uses seed. The same seed gives the same
// sequence of heights.
func newHeightGen(seed uint64) *heightGen {
	return &heightGen{
		rng: rand.New(rand.NewPCG(seed, ^seed)),
	}
}
