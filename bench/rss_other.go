//go:build !darwin && !linux

package main

import "os"

// peakRSS is not measured on this platform.
func peakRSS(*os.ProcessState) (int64, bool) { return 0, false }
