package skiplist

import "math/rand/v2"

const (
	maxHeight         = 12
	branchProbability = 0.25
)

type Comparator func(a []byte, b []byte) int

type heightGen struct {
	rng *rand.Rand
}

// randomHeight returns a random height for a new node in the skip list.
func (hg *heightGen) randomHeight() int {
	height := 1
	for height < maxHeight && hg.rng.Float64() < branchProbability {
		height++
	}
	return height
}

func newHeightGen(seed uint64) *heightGen {
	return &heightGen{
		rng: rand.New(rand.NewPCG(seed, ^seed)),
	}
}
