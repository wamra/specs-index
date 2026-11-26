package api

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/wamra/specs-index/internal/workspace"
)

func tempRoot(t *testing.T) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A hostile index can name any path in its documents table; reading the source
// lines for a hit must stay inside the root.
func TestReadLinesRefusesPathsOutsideRoot(t *testing.T) {
	root := tempRoot(t)
	outside := tempRoot(t)
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ok.md"), []byte("fine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readLines(root, "ok.md", 1, 1); got != "fine" {
		t.Fatalf("inside file: %q", got)
	}
	rel, err := filepath.Rel(root, filepath.Join(outside, "secret.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := readLines(root, filepath.ToSlash(rel), 1, 1); got != "" {
		t.Fatalf("../ path was read: %q", got)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.md")); err != nil {
			t.Fatal(err)
		}
		if got := readLines(root, "link.md", 1, 1); got != "" {
			t.Fatalf("symlink leaving the root was read: %q", got)
		}
	}
}

func TestIndexOpenersRefuseSymlinkedStateDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	t.Setenv("SPECS_INDEX_DB", "")
	root := tempRoot(t)
	outside := tempRoot(t)
	if err := os.Symlink(outside, filepath.Join(root, workspace.StateDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenForWrite(root); !errors.Is(err, workspace.ErrStateSymlink) {
		t.Fatalf("OpenForWrite: %v", err)
	}
	if _, err := OpenExisting(root); !errors.Is(err, workspace.ErrStateSymlink) {
		t.Fatalf("OpenExisting: %v", err)
	}
	if _, err := LockIndex(context.Background(), root, io.Discard); !errors.Is(err, workspace.ErrStateSymlink) {
		t.Fatalf("LockIndex: %v", err)
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatalf("files were created through the link: %v", entries)
	}
}

func TestIndexOpenersRefuseSymlinkedDBFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	t.Setenv("SPECS_INDEX_DB", "")
	root := tempRoot(t)
	outside := tempRoot(t)
	if err := os.MkdirAll(filepath.Join(root, workspace.StateDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "victim"), workspace.DBPath(root)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenForWrite(root); !errors.Is(err, workspace.ErrStateSymlink) {
		t.Fatalf("OpenForWrite: %v", err)
	}
	if _, err := OpenExisting(root); !errors.Is(err, workspace.ErrStateSymlink) {
		t.Fatalf("OpenExisting: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "victim")); err == nil {
		t.Fatal("the link target was created")
	}
}
