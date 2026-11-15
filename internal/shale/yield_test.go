// SPDX-License-Identifier: MIT

package shale

import (
	"runtime"
	"testing"
)

func TestYielder(t *testing.T) {
	t.Run("Should pause once for each yieldEvery entries", func(t *testing.T) {
		pauses := 0
		y := &yielder{pause: func() { pauses++ }}
		for range 10 * yieldEvery {
			y.tick()
		}
		if pauses != 10 {
			t.Fatalf("got %d pauses, want 10", pauses)
		}
	})

	t.Run("Should do nothing when there is no pause", func(t *testing.T) {
		y := &yielder{}
		for range 1000 {
			y.tick()
		}
		if y.n != 0 {
			t.Fatalf("a yielder with no pause counted %d entries", y.n)
		}
	})

	t.Run("Should pause only when the program has one processor", func(t *testing.T) {
		old := runtime.GOMAXPROCS(1)
		defer runtime.GOMAXPROCS(old)
		if newYielder().pause == nil {
			t.Fatal("no pause with one processor")
		}
		runtime.GOMAXPROCS(2)
		if newYielder().pause != nil {
			t.Fatal("a pause with two processors")
		}
	})
}
