// SPDX-License-Identifier: MIT

package shale

import (
	"bytes"
	"testing"
)

func BenchmarkOverwrite(b *testing.B) {
	db := openDB(b, b.TempDir(), Options{MemtableSize: 1 << 20})
	defer db.Close()
	const keys = 10000
	value := bytes.Repeat([]byte("v"), 100)
	var userBytes uint64
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		key := keyN(i % keys)
		if err := db.Put(key, value); err != nil {
			b.Fatal(err)
		}
		userBytes += uint64(len(key) + len(value))
	}
	b.StopTimer()
	waitQuiet(b, db)
	m := db.Metrics()
	b.ReportMetric(float64(m.DiskBytes), "disk-bytes")
	b.ReportMetric(float64(m.Tables), "tables")
	b.ReportMetric(float64(m.FlushBytes+m.CompactionBytesWritten)/float64(userBytes), "write-amp")
}
