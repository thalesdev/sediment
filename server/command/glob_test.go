// SPDX-License-Identifier: MIT

package command

import (
	"testing"
	"time"
)

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"*", "", true},
		{"*", "anything", true},
		{"", "", true},
		{"", "a", false},
		{"a", "a", true},
		{"a", "b", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"user:*", "user:1/2", true},
		{"user:*", "users", false},
		{"*:1", "user:1", true},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		{"h[ae]llo", "hello", true},
		{"h[ae]llo", "hallo", true},
		{"h[ae]llo", "hillo", false},
		{"h[^e]llo", "hallo", true},
		{"h[^e]llo", "hello", false},
		{"h[a-b]llo", "hbllo", true},
		{"h[a-b]llo", "hcllo", false},
		{"h[b-a]llo", "hallo", true},
		{`a\*b`, "a*b", true},
		{`a\*b`, "aXb", false},
		{`a\`, `a\`, true},
		{"[abc", "b", true},
		{"[abc", "d", false},
		{"[a-]", "-", true},
		{"**a", "ba", true},
		{"a**", "a", true},
		{"a*", "b", false},
		{"*a*", "bab", true},
		{"[]", "a", false},
	}
	for _, c := range cases {
		t.Run("Should match "+c.pattern+" with "+c.s, func(t *testing.T) {
			if got := globMatch([]byte(c.pattern), []byte(c.s)); got != c.want {
				t.Fatalf("globMatch(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
			}
		})
	}

	t.Run("Should not spin on a pattern with many stars", func(t *testing.T) {
		s := make([]byte, 5000)
		for i := range s {
			s[i] = 'a'
		}
		start := time.Now()
		if globMatch([]byte("a*a*a*a*a*a*a*a*b"), s) {
			t.Fatal("matched")
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("took %v", d)
		}
	})
}

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

func refGlob(p, s []byte, memo map[[2]int]bool, pi, si int) bool {
	key := [2]int{pi, si}
	if v, ok := memo[key]; ok {
		return v
	}
	var res bool
	switch {
	case pi == len(p):
		res = si == len(s)
	case p[pi] == '*':
		res = refGlob(p, s, memo, pi+1, si) || (si < len(s) && refGlob(p, s, memo, pi, si+1))
	case si == len(s):
		res = false
	case p[pi] == '?':
		res = refGlob(p, s, memo, pi+1, si+1)
	case p[pi] == '[':
		ok, next := matchClass(p, pi, s[si])
		res = ok && refGlob(p, s, memo, next, si+1)
	case p[pi] == '\\' && pi+1 < len(p):
		res = p[pi+1] == s[si] && refGlob(p, s, memo, pi+2, si+1)
	default:
		res = p[pi] == s[si] && refGlob(p, s, memo, pi+1, si+1)
	}
	memo[key] = res
	return res
}

func FuzzGlobMatch(f *testing.F) {
	f.Add([]byte("a*b"), []byte("axxb"))
	f.Add([]byte("[a-c]?*"), []byte("bzz"))
	f.Add([]byte(`\*[^x]`), []byte("*y"))
	f.Fuzz(func(t *testing.T, pattern, s []byte) {
		if len(pattern) > 16 || len(s) > 16 {
			return
		}
		got := globMatch(pattern, s)
		want := refGlob(pattern, s, map[[2]int]bool{}, 0, 0)
		if got != want {
			t.Fatalf("globMatch(%q, %q) = %v, reference says %v", pattern, s, got, want)
		}
	})
}
