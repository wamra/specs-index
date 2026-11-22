//go:build windows

package filelock

import (
	"os"

	"golang.org/x/sys/windows"
)

func lock(f *os.File) error {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err == windows.ERROR_LOCK_VIOLATION {
		return ErrLocked
	}
	return err
}

func unlock(f *os.File) {
	windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}
