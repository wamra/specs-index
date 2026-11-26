package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

func evalDir(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFindNearestGitAncestor(t *testing.T) {
	root := evalDir(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Find(sub)
	if err != nil || got != root {
		t.Fatalf("Find = %q, %v; want %q", got, err, root)
	}
}

func TestFindWithoutGitReturnsStart(t *testing.T) {
	start := evalDir(t, t.TempDir())
	got, err := Find(start)
	if err != nil || got != start {
		t.Fatalf("Find = %q, %v; want %q", got, err, start)
	}
}

func TestDBPath(t *testing.T) {
	t.Setenv("SPECS_INDEX_DB", "")
	root := filepath.FromSlash("/x/proj")
	want := filepath.Join(root, ".Wammy", "specs.sqlite")
	if got := DBPath(root); got != want {
		t.Fatalf("DBPath = %q, want %q", got, want)
	}
	t.Setenv("SPECS_INDEX_DB", "/elsewhere/db.sqlite")
	if got := DBPath(root); got != "/elsewhere/db.sqlite" {
		t.Fatalf("override ignored: %q", got)
	}
}

func TestRel(t *testing.T) {
	root := evalDir(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(root, "docs", "a.md")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := Rel(root, f); err != nil || got != "docs/a.md" {
		t.Fatalf("abs: %q, %v", got, err)
	}
	if got, err := Rel(root, "docs/a.md"); err != nil || got != "docs/a.md" {
		t.Fatalf("relative to root: %q, %v", got, err)
	}
	if got, err := Rel(root, root); err != nil || got != "." {
		t.Fatalf("root itself: %q, %v", got, err)
	}
	if _, err := Rel(root, "../outside"); err == nil {
		t.Fatal("expected error for ..")
	}
	if _, err := Rel(root, filepath.Dir(root)); err == nil {
		t.Fatal("expected error for parent dir")
	}
}

func TestRelRejectsSymlinkLeavingRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	root := evalDir(t, t.TempDir())
	outside := evalDir(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(outside, "x.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := Rel(root, filepath.Join(root, "link", "x.md")); err == nil {
		t.Fatal("expected error for symlink leaving root")
	}
}

func TestEnsureExcludedIdempotent(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	for i := 0; i < 2; i++ {
		if err := EnsureExcluded(root); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), ".Wammy/"); n != 1 {
		t.Fatalf(".Wammy/ appears %d times:\n%s", n, b)
	}
}

func TestEnsureExcludedWithoutGitDoesNothing(t *testing.T) {
	root := t.TempDir()
	if err := EnsureExcluded(root); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(root); len(ents) != 0 {
		t.Fatalf("created files: %v", ents)
	}
}

func TestEnsureExcludedWorktree(t *testing.T) {
	main := t.TempDir()
	gitInit(t, main)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", main, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("commit", "--allow-empty", "-q", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	run("worktree", "add", "-q", wt)
	if err := EnsureExcluded(wt); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", wt, "rev-parse", "--git-dir").Output()
	if err != nil {
		t.Fatal(err)
	}
	gd := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(wt, gd)
	}
	b, err := os.ReadFile(filepath.Join(gd, "info", "exclude"))
	if err != nil || !strings.Contains(string(b), ".Wammy/") {
		t.Fatalf("worktree exclude missing entry: %v\n%s", err, b)
	}
}

func TestCheckStateRefusesSymlinkedStateDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	t.Setenv("SPECS_INDEX_DB", "")
	root := evalDir(t, t.TempDir())
	outside := evalDir(t, t.TempDir())
	if err := CheckState(root); err != nil {
		t.Fatalf("clean root: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, StateDir)); err != nil {
		t.Fatal(err)
	}
	if err := CheckState(root); !errors.Is(err, ErrStateSymlink) {
		t.Fatalf("symlinked .Wammy: err = %v, want ErrStateSymlink", err)
	}
}

func TestCheckStateRefusesSymlinkedDBFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	t.Setenv("SPECS_INDEX_DB", "")
	for _, suffix := range []string{"", "-wal", "-shm", ".lock"} {
		root := evalDir(t, t.TempDir())
		outside := filepath.Join(evalDir(t, t.TempDir()), "target")
		if err := os.MkdirAll(filepath.Join(root, StateDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, DBPath(root)+suffix); err != nil {
			t.Fatal(err)
		}
		if err := CheckState(root); !errors.Is(err, ErrStateSymlink) {
			t.Fatalf("symlinked db%s: err = %v, want ErrStateSymlink", suffix, err)
		}
	}
}

func TestCheckStateEnvOverrideChecksFilesOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	root := evalDir(t, t.TempDir())
	real := evalDir(t, t.TempDir())
	// The user chose this location: a linked directory is theirs to use.
	linkDir := filepath.Join(evalDir(t, t.TempDir()), "dir")
	if err := os.Symlink(real, linkDir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPECS_INDEX_DB", filepath.Join(linkDir, "x.sqlite"))
	if err := CheckState(root); err != nil {
		t.Fatalf("linked directory with SPECS_INDEX_DB: %v", err)
	}
	if err := os.Symlink(filepath.Join(real, "elsewhere"), filepath.Join(real, "x.sqlite")); err != nil {
		t.Fatal(err)
	}
	if err := CheckState(root); !errors.Is(err, ErrStateSymlink) {
		t.Fatalf("linked db file: err = %v, want ErrStateSymlink", err)
	}
}
