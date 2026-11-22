// Package filelock takes a non-blocking exclusive lock on a file: flock on
// Unix, LockFileEx on Windows. The lock dies with the process. The lock file
// is never removed.
package filelock

import (
	"errors"
	"os"
)

// ErrLocked is returned by Try when another process (or another handle in this
// process) holds the lock.
var ErrLocked = errors.New("locked by another process")

// Lock is a held lock.
type Lock struct{ f *os.File }

// ErrSymlink is returned by Try when path is a symbolic link: following it
// could create or lock a file anywhere.
var ErrSymlink = errors.New("lock file is a symbolic link")

// Try creates path if needed and locks it without blocking. It refuses a path
// that is a symbolic link (Lstat first, and O_NOFOLLOW where the platform has
// it, against a link made in between).
func Try(path string) (*Lock, error) {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSymlink // the caller names the path as it shows paths
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|noFollow, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock(f); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f}, nil
}

// Release drops the lock.
func (l *Lock) Release() {
	unlock(l.f)
	l.f.Close()
}
