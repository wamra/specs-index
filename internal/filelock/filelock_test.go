package filelock

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSecondHandleIsRefusedUntilRelease(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	a, err := Try(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Try(p); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Try = %v, want ErrLocked", err)
	}
	a.Release()
	b, err := Try(p)
	if err != nil {
		t.Fatalf("Try after Release: %v", err)
	}
	b.Release()
	if _, err := os.Stat(p); err != nil {
		t.Errorf("lock file removed: %v", err)
	}
}

// TestHelperHoldLock is the child process of TestOtherProcessHoldsTheLock.
func TestHelperHoldLock(t *testing.T) {
	p := os.Getenv("FILELOCK_TEST_HOLD")
	if p == "" {
		t.Skip("helper process only")
	}
	l, err := Try(p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	os.Stdout.WriteString("locked\n")
	bufio.NewReader(os.Stdin).ReadString('\n') // hold until the parent closes stdin
}

func TestOtherProcessHoldsTheLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	cmd := exec.Command(os.Args[0], "-test.run", "^TestHelperHoldLock$")
	cmd.Env = append(os.Environ(), "FILELOCK_TEST_HOLD="+p)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		stdin.Close()
		cmd.Wait()
		t.Fatalf("child: %q %v", line, err)
	}
	if _, err := Try(p); !errors.Is(err, ErrLocked) {
		t.Errorf("Try while another process holds the lock = %v, want ErrLocked", err)
	}
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v", err)
	}
	l, err := Try(p)
	if err != nil {
		t.Fatalf("Try after the other process exited: %v", err)
	}
	l.Release()
}
