//go:build !windows

package filelock

import (
	"os"
	"syscall"
)

// noFollow makes OpenFile fail on a symbolic link.
const noFollow = syscall.O_NOFOLLOW

func lock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == syscall.EWOULDBLOCK {
		return ErrLocked
	}
	return err
}

func unlock(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
