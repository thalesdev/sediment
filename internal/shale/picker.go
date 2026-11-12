// SPDX-License-Identifier: MIT

package shale

const (
	// compactRatio is how many times larger than the run so far the next older
	// table can be, for the compaction to add it to the run.
	compactRatio = 2

	// minRun is the number of tables that a run needs for a compaction.
	minRun = 4

	// maxRun is the largest number of tables in one compaction.
	maxRun = 16

	// tableCountTrigger is the number of tables above which a compaction
	// starts even if no run of similar size exists.
	tableCountTrigger = 24

	// countRun is the number of the newest tables that a compaction by count
	// joins.
	countRun = 8

	// stallTables is the number of tables at which a write that needs to freeze
	// the memtable waits for the compaction.
	stallTables = 36
)

// tableInfo is what the picker needs to know about a table.
type tableInfo struct {
	size uint64
}

// plan is a choice of the picker. The tables from start to end, not including
// end, are the inputs of the compaction. They are next to each other in the list
// of the picker.
type plan struct {
	start, end     int
	includesOldest bool
	byCount        bool
}

// pickCompaction chooses the tables to join. The tables are in order from the
// newest to the oldest. It starts at the newest table and adds the next older
// table while its size is not above compactRatio times the sum of the run so
// far, up to maxRun tables. If the run has at least minRun tables, it is the
// plan. If not, and there are more than tableCountTrigger tables, the plan is
// the countRun newest tables, and byCount is true. Otherwise there is no plan.
// The plan always starts at the newest table, and includesOldest tells if it
// reaches the oldest one.
func pickCompaction(tables []tableInfo) (plan, bool) {
	n := len(tables)
	if n >= minRun {
		sum := tables[0].size
		end := 1
		for end < n && end < maxRun && tables[end].size <= compactRatio*sum {
			sum += tables[end].size
			end++
		}
		if end >= minRun {
			return plan{start: 0, end: end, includesOldest: end == n}, true
		}
	}
	if n > tableCountTrigger {
		return plan{start: 0, end: countRun, includesOldest: countRun == n, byCount: true}, true
	}
	return plan{}, false
}

// tableInfos returns the information that the picker needs for tables.
func tableInfos(tables []*table) []tableInfo {
	infos := make([]tableInfo, len(tables))
	for i, t := range tables {
		infos[i] = tableInfo{size: t.meta.Size}
	}
	return infos
}
