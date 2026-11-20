// SPDX-License-Identifier: MIT

package command

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
