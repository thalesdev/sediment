// SPDX-License-Identifier: MIT

package shale

import "time"

// idle tells if no flush or compaction is running and none is due. The caller
// must not hold db.mu.
func (db *DB) idle() bool {
	state := db.state.Load()
	_, planned := pickCompaction(tableInfos(state.tables))
	return state.imm == nil && !db.compacting.Load() && (!planned || db.opts.disableCompaction)
}

// WaitIdle waits until no flush or compaction is running and none is due. It
// does not flush the active memtable. It returns the error of the DB if the DB
// has failed, and ErrClosed if the DB is closed. It is for tests and
// benchmarks, to measure the DB when its background work is done.
func (db *DB) WaitIdle() error {
	for {
		db.mu.Lock()
		err, closed := db.err, db.closed.Load()
		db.mu.Unlock()
		if err != nil {
			return err
		}
		if closed {
			return ErrClosed
		}
		if db.idle() {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
}
