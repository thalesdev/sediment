// SPDX-License-Identifier: MIT

package shale

import (
	"runtime"
	"time"
)

const (
	// yieldEvery is the number of entries that a job in the background handles between
	// two pauses. An entry takes about one microsecond.
	yieldEvery = 128

	// yieldPause is the length of a pause.
	yieldPause = 20 * time.Microsecond
)

// yielder lets the requests run in the middle of a long job that does not wait for
// anything, such as the flush and the compaction.
//
// It does something only when the program has one processor for the goroutines, which is
// the case in a container with one CPU. With one processor the scheduler of Go looks for
// network events only when its queues of goroutines are empty, or every 10 milliseconds
// from another thread. A job that is always ready keeps the queue from being empty, and
// the requests whose bytes already came wait for tens of milliseconds, even when the job
// calls runtime.Gosched, which puts it at the end of the queue and keeps the queue full.
// A short sleep takes the job out of the queue, the processor finds the queue empty, and
// it looks for network events. With two or more processors one of them is idle and looks
// for the events by itself, and the yielder does nothing.
type yielder struct {
	n     int
	pause func()
}

// newYielder returns a yielder for a job in the background.
func newYielder() *yielder {
	y := &yielder{}
	if runtime.GOMAXPROCS(0) == 1 {
		y.pause = func() { time.Sleep(yieldPause) }
	}
	return y
}

// tick counts one entry and pauses the job after yieldEvery of them, if there is one
// processor.
func (y *yielder) tick() {
	if y.pause == nil {
		return
	}
	y.n++
	if y.n%yieldEvery == 0 {
		y.pause()
	}
}
