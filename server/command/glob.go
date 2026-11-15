// SPDX-License-Identifier: MIT

package command

// globMatch reports whether s matches the glob pattern, with the rules of Redis:
// * matches any bytes, ? matches one byte, [abc] and [a-z] match a set, [^a]
// matches a byte that is not in the set, and a backslash escapes the next byte.
// It is iterative, and its time is at most the product of the two lengths, so a
// pattern like a*a*a*b is not a way to make the server spin.
func globMatch(pattern, s []byte) bool {
	p, i := 0, 0
	starP, starI := -1, 0
	for i < len(s) {
		if p < len(pattern) {
			switch pattern[p] {
			case '*':
				for p < len(pattern) && pattern[p] == '*' {
					p++
				}
				if p == len(pattern) {
					return true
				}
				starP, starI = p, i
				continue
			case '?':
				p++
				i++
				continue
			case '[':
				if ok, next := matchClass(pattern, p, s[i]); ok {
					p = next
					i++
					continue
				}
			case '\\':
				if p+1 < len(pattern) {
					if pattern[p+1] == s[i] {
						p += 2
						i++
						continue
					}
				} else if s[i] == '\\' {
					p++
					i++
					continue
				}
			default:
				if pattern[p] == s[i] {
					p++
					i++
					continue
				}
			}
		}
		if starP < 0 {
			return false
		}
		starI++
		i = starI
		p = starP
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// matchClass matches c against the set that starts at pattern[p], which is a
// bracket. It returns whether c is in the set, and the index after the set. A set
// that is not closed runs to the end of the pattern.
func matchClass(pattern []byte, p int, c byte) (bool, int) {
	q := p + 1
	negate := false
	if q < len(pattern) && pattern[q] == '^' {
		negate = true
		q++
	}
	matched := false
	for q < len(pattern) && pattern[q] != ']' {
		switch {
		case pattern[q] == '\\' && q+1 < len(pattern):
			q++
			if pattern[q] == c {
				matched = true
			}
			q++
		case q+2 < len(pattern) && pattern[q+1] == '-' && pattern[q+2] != ']':
			lo, hi := pattern[q], pattern[q+2]
			if lo > hi {
				lo, hi = hi, lo
			}
			if c >= lo && c <= hi {
				matched = true
			}
			q += 3
		default:
			if pattern[q] == c {
				matched = true
			}
			q++
		}
	}
	if q < len(pattern) {
		q++
	}
	return matched != negate, q
}

// globPrefix returns the bytes at the start of the pattern that only match
// themselves. Every key that matches the pattern starts with them.
func globPrefix(pattern []byte) []byte {
	for i, c := range pattern {
		switch c {
		case '*', '?', '[', '\\':
			return pattern[:i]
		}
	}
	return pattern
}

// prefixEnd returns the smallest key that is greater than every key that starts
// with prefix, or nil if there is none.
func prefixEnd(prefix []byte) []byte {
	end := append([]byte(nil), prefix...)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] != 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}
