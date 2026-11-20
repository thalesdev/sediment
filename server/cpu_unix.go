// SPDX-License-Identifier: MIT

//go:build unix

package server

import "syscall"

// cpuTimes returns the CPU time that the process used in user mode and in system
// mode, in seconds, and false if the system does not say.
func cpuTimes() (user, system float64, ok bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, 0, false
	}
	return float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6, float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6, true
}
