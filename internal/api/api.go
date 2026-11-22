// Package api implements every specs-index command as a function that takes an
// opened index and returns an exported result struct. The CLI prints these
// structs as text or as JSON, and the MCP server returns the same JSON, so
// nothing about JSON shape lives outside this package.
//
// Conventions: JSON field names are lower_snake_case, slices are never null
// (an empty list is []), and paths are root-relative with forward slashes.
package api

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/wamra/specs-index/internal/store"
	"github.com/wamra/specs-index/internal/workspace"
)

// ErrInvalidArgument marks an invalid flag or parameter value (an unknown
// mode, direction or edge kind, or a contradictory combination). The CLI maps
// it to exit code 2.
var ErrInvalidArgument = errors.New("invalid argument")

// ErrNoIndex is returned by OpenExisting when the index file is missing.
var ErrNoIndex = errors.New("no index")

// ResolveRoot returns the absolute, symlink-resolved project root: dir when
// given, otherwise the nearest ancestor of the working directory with a .git
// entry (the working directory itself when there is none).
func ResolveRoot(dir string) (string, error) {
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return workspace.Find(cwd)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("root %s is not a directory", dir)
	}
	return abs, nil
}

// OpenForWrite opens (creating if needed) the index of root.
func OpenForWrite(root string) (*store.Store, error) {
	return store.Open(workspace.DBPath(root))
}

// OpenExisting opens the existing index of root for reading. It returns an
// error wrapping ErrNoIndex when there is none.
func OpenExisting(root string) (*store.Store, error) {
	p := workspace.DBPath(root)
	if _, err := os.Stat(p); err != nil {
		return nil, fmt.Errorf("%w for this project (looked for %s); run `specs-index add PATH` then `specs-index index`", ErrNoIndex, DisplayPath(root, p))
	}
	return store.Open(p)
}

// DisplayPath renders p root-relative with forward slashes when it is inside
// root, and as "(outside project)" otherwise, so no absolute path is shown.
func DisplayPath(root, p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "(outside project)"
	}
	if r, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		abs = filepath.Join(r, filepath.Base(abs))
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || len(rel) > 2 && rel[:3] == ".."+string(filepath.Separator) {
		return "(outside project)"
	}
	return filepath.ToSlash(rel)
}

func round6(f float64) float64 { return math.Round(f*1e6) / 1e6 }

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
