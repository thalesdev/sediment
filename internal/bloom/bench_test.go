// SPDX-License-Identifier: MIT

package bloom

import "testing"

func BenchmarkNew(b *testing.B) {
	k := keys("in", 10000)
	b.ReportAllocs()
	for range b.N {
		New(k, DefaultBitsPerKey)
	}
}

func BenchmarkMayContain(b *testing.B) {
	const n = 10000
	f := New(keys("in", n), DefaultBitsPerKey)
	b.Run("hit", func(b *testing.B) {
		k := keys("in", n)
		b.ReportAllocs()
		for i := range b.N {
			f.MayContain(k[i%n])
		}
	})
	b.Run("miss", func(b *testing.B) {
		k := keys("out", n)
		b.ReportAllocs()
		for i := range b.N {
			f.MayContain(k[i%n])
		}
	})
}
