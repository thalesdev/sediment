// SPDX-License-Identifier: MIT

package command

import (
	"testing"

	"github.com/thalesdev/sediment/server/acl"
)

func TestKeyspaceSpecs(t *testing.T) {
	reg := NewRegistry()

	t.Run("Should mark KEYS as dangerous and the others as not", func(t *testing.T) {
		if reg.Lookup([]byte("keys")).Categories&acl.CatDangerous == 0 {
			t.Fatal("KEYS should be @dangerous")
		}
		for _, name := range []string{"scan", "dbsize", "randomkey", "type"} {
			if reg.Lookup([]byte(name)).Categories&acl.CatDangerous != 0 {
				t.Fatalf("%s should not be @dangerous", name)
			}
		}
	})

	t.Run("Should keep the cursors of a connection bounded", func(t *testing.T) {
		var s scanState
		first := s.remember([]byte("a"))
		for range maxScanCursors + 5 {
			s.remember([]byte("b"))
		}
		if _, ok := s.lookup(first); ok {
			t.Fatal("the oldest cursor should be gone")
		}
		if len(s.last) != maxScanCursors || len(s.order) != maxScanCursors {
			t.Fatalf("holds %d cursors", len(s.last))
		}
		if first == 0 {
			t.Fatal("a cursor is never 0")
		}
	})
}
