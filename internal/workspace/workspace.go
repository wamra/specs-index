// Package workspace finds the project root, the index location and decides
// which paths belong to the project.
package workspace

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// StateDir is the per-project directory that holds the index.
const StateDir = ".Wammy"

// Find returns the nearest ancestor of start (start included) that contains a
// .git entry, or start itself when there is none.
func Find(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	for dir := abs; ; {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs, nil
		}
		dir = parent
	}
}

// DBPath returns the index file for root; SPECS_INDEX_DB overrides it.
func DBPath(root string) string {
	if p := os.Getenv("SPECS_INDEX_DB"); p != "" {
		return p
	}
	return filepath.Join(root, StateDir, "specs.sqlite")
}

// Rel returns path relative to root with forward slashes ("." for root
// itself). A relative path is taken relative to root. Symlinks are resolved on
// both sides, so a link that leaves the root is an error, as is a path that
// does not exist.
func Rel(root, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(r, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the project root %s", path, root)
	}
	return filepath.ToSlash(rel), nil
}

// EnsureExcluded appends ".Wammy/" to <gitdir>/info/exclude once. Without git
// or without a git dir it does nothing.
func EnsureExcluded(root string) error {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil
	}
	cmd := exec.Command(git, "-C", root, "rev-parse", "--git-dir")
	out, err := cmd.Output()
	if err != nil {
		return nil // not a git work tree
	}
	gitDir := strings.TrimSpace(string(out))
	if gitDir == "" {
		return nil
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(root, gitDir)
	}
	file := filepath.Join(gitDir, "info", "exclude")
	entry := StateDir + "/"
	existing, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	sc := bufio.NewScanner(bytes.NewReader(existing))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == entry {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	line := entry + "\n"
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		line = "\n" + line
	}
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
