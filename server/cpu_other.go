// SPDX-License-Identifier: MIT

//go:build !unix

package server

// cpuTimes returns the CPU time that the process used, and false, because this
// system has no call for it that the server uses.
func cpuTimes() (user, system float64, ok bool) {
	return 0, 0, false
}
