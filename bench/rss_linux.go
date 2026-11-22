//go:build linux

package main

import (
	"os"
	"syscall"
)

// peakRSS returns the child's peak resident set size in bytes (Linux reports
// ru_maxrss in kilobytes).
func peakRSS(ps *os.ProcessState) (int64, bool) {
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok || ru == nil {
		return 0, false
	}
	return int64(ru.Maxrss) * 1024, true
}
