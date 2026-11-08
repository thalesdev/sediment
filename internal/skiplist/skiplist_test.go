package skiplist

import (
	"math"
	"testing"
)

func TestHeightGen(t *testing.T) {

	t.Parallel()

	t.Run("Assert that randomHeight returns values between 1 and maxHeight", func(t *testing.T) {
		hg := newHeightGen(42)
		for i := 0; i < 1000; i++ {
			height := hg.randomHeight()
			if height < 1 || height > maxHeight {
				t.Errorf("randomHeight returned %d, expected between 1 and %d", height, maxHeight)
			}
		}
	})

	t.Run("Assert that distribution follow spected p^(h-1) x (1 - p) in first 3 heights", func(t *testing.T) {
		nodes := 100000
		threshold := 0.05
		hProb := 3
		heights := make([]int, maxHeight)
		hg := newHeightGen(42)
		for i := 0; i < nodes; i++ {
			h := hg.randomHeight()
			heights[h-1]++
		}

		for i := 0; i < hProb; i++ {
			expected := math.Pow(branchProbability, float64(i)) * (1 - branchProbability) * float64(nodes)
			actual := float64(heights[i])
			if actual < expected*(1-threshold) || actual > expected*(1+threshold) {
				t.Errorf("Height %d: expected around %.2f, got %.2f", i+1, expected, actual)
			}
		}

	})
}
