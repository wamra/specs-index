package search

import (
	"os"
	"path/filepath"
	"testing"
)

// The document paths in an index are not trusted: a repository can ship an
// index whose rows point outside the root. isStale must not open such a path;
// it reports the row stale (the file cannot be vouched for).
func TestIsStaleDoesNotOpenPathsOutsideRoot(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "x.md")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		t.Fatal(err)
	}
	// A matching size and mtime would make a readable file look fresh.
	fi, _ := os.Stat(target)
	d := docState{}
	d.size.Valid, d.size.Int64 = true, fi.Size()
	d.mtime.Valid, d.mtime.Int64 = true, fi.ModTime().UnixNano()
	if !isStale(root, filepath.ToSlash(rel), d) {
		t.Fatal("a path outside the root was treated as fresh")
	}
}
