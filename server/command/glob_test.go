// SPDX-License-Identifier: MIT

package command

import "testing"

func TestPrefixEnd(t *testing.T) {
	t.Run("Should return the end of a prefix range", func(t *testing.T) {
		if got := string(prefixEnd([]byte("user:"))); got != "user;" {
			t.Fatalf("got %q", got)
		}
		if got := prefixEnd([]byte{'a', 0xff}); string(got) != "b" {
			t.Fatalf("got %q", got)
		}
		if got := prefixEnd([]byte{0xff, 0xff}); got != nil {
			t.Fatalf("got %q", got)
		}
	})
}
