//go:build darwin

package main

import (
	"os"
	"syscall"
)

// peakRSS returns the child's peak resident set size in bytes (macOS reports
// ru_maxrss in bytes).
func peakRSS(ps *os.ProcessState) (int64, bool) {
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok || ru == nil {
		return 0, false
	}
	return int64(ru.Maxrss), true
}
