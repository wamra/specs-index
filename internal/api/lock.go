package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/wamra/specs-index/internal/filelock"
	"github.com/wamra/specs-index/internal/workspace"
)

// IndexLockWait bounds how long LockIndex waits for another index run of the
// same project. It is a variable only so tests can shorten it.
var IndexLockWait = 2 * time.Minute

// lockPoll is how often LockIndex retries the lock.
const lockPoll = 100 * time.Millisecond

// LockIndex takes the inter-process lock that serialises index runs of root:
// the file <index>.lock next to the index database (it is never removed). When
// another process holds it, LockIndex writes one line to note and retries until
// the lock is free, ctx ends or IndexLockWait passes, then fails. Read commands
// never take it. The caller must call the returned release function.
func LockIndex(ctx context.Context, root string, note io.Writer) (release func(), err error) {
	if err := workspace.CheckState(root); err != nil {
		return nil, err
	}
	p := workspace.DBPath(root) + ".lock"
	deadline := time.Now().Add(IndexLockWait)
	noted := false
	for {
		l, err := filelock.Try(p)
		if err == nil {
			return l.Release, nil
		}
		if !errors.Is(err, filelock.ErrLocked) {
			return nil, fmt.Errorf("index lock %s: %w", DisplayPath(root, p), err)
		}
		if !noted {
			noted = true
			fmt.Fprintf(note, "specs-index: another `specs-index index` of this project is running; waiting up to %s for it to finish\n", IndexLockWait)
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("another `specs-index index` of this project is still running after %s (lock file %s); run index again when it has finished", IndexLockWait, DisplayPath(root, p))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}
