// Package indexer registers sources and keeps the index in step with the
// files under them.
package indexer

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wamra/specs-index/internal/store"
	"github.com/wamra/specs-index/internal/workspace"
)

// Source is a registered file or directory, root-relative with forward slashes.
type Source struct {
	ID   int
	Path string
	Kind string // "file" or "dir"
}

// AddSource registers path (absolute, or relative to root) as a source. It
// refuses a path that resolves outside root, including through a symlink.
func AddSource(st *store.Store, root, p string) error {
	rel, err := workspace.Rel(root, p)
	if err != nil {
		return err
	}
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	kind := "file"
	if fi.IsDir() {
		kind = "dir"
	}
	_, err = st.DB().Exec(`INSERT OR IGNORE INTO sources(path, kind) VALUES (?, ?)`, rel, kind)
	return err
}

// RemoveSource unregisters p and drops every document no remaining source
// covers.
func RemoveSource(st *store.Store, root, p string) error {
	rel, err := workspace.Rel(root, p)
	if err != nil {
		// the path may be gone from disk; accept an already-relative form
		if filepath.IsAbs(p) {
			return err
		}
		rel = path.Clean(filepath.ToSlash(p))
	}
	res, err := st.DB().Exec(`DELETE FROM sources WHERE path = ?`, rel)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("source %q is not registered", rel)
	}
	_, err = run(context.Background(), st, root, true)
	return err
}

// ListSources returns the registered sources ordered by path.
func ListSources(st *store.Store) ([]Source, error) {
	rows, err := st.DB().Query(`SELECT id, path, kind FROM sources ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var s Source
		if err := rows.Scan(&s.ID, &s.Path, &s.Kind); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// skippedDir reports directories that are never walked.
func skippedDir(name string) bool { return name == ".git" || name == workspace.StateDir }

func hasSkippedComponent(rel string) bool {
	for _, c := range strings.Split(rel, "/") {
		if skippedDir(c) {
			return true
		}
	}
	return false
}

func supported(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".feature":
		return true
	}
	return false
}

// discover lists the root-relative files under the registered sources, sorted
// and de-duplicated, plus failures for entries that were skipped.
func discover(ctx context.Context, st *store.Store, root string) ([]string, []Failure, error) {
	srcs, err := ListSources(st)
	if err != nil {
		return nil, nil, err
	}
	set := map[string]bool{}
	var fails []Failure
	failed := map[string]bool{}
	fail := func(rel, reason string) {
		if !failed[rel] {
			failed[rel] = true
			fails = append(fails, Failure{Path: rel, Reason: reason})
		}
	}
	for _, s := range srcs {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		abs := filepath.Join(root, filepath.FromSlash(s.Path))
		if _, err := workspace.Rel(root, abs); err != nil {
			fail(s.Path, "source: "+err.Error())
			continue
		}
		fi, err := os.Stat(abs)
		if err != nil {
			fail(s.Path, "source: "+err.Error())
			continue
		}
		if !fi.IsDir() {
			if hasSkippedComponent(s.Path) {
				continue
			}
			if !supported(s.Path) {
				fail(s.Path, "unsupported file type")
				continue
			}
			set[s.Path] = true
			continue
		}
		walkErr := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if err != nil {
				fail(rel, err.Error())
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if p != abs && skippedDir(d.Name()) {
					return fs.SkipDir
				}
				if p == abs && hasSkippedComponent(rel) {
					return fs.SkipDir
				}
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				if hasSkippedComponent(rel) {
					return nil
				}
				if _, lerr := workspace.Rel(root, p); lerr != nil {
					if os.IsNotExist(lerr) {
						fail(rel, "broken symlink")
					} else {
						fail(rel, "symlink leaves the project root")
					}
					return nil
				}
				ti, serr := os.Stat(p)
				if serr != nil || ti.IsDir() || !supported(d.Name()) {
					return nil // linked directories are covered at their real location
				}
				set[rel] = true
				return nil
			}
			if supported(d.Name()) && !hasSkippedComponent(rel) {
				set[rel] = true
			}
			return nil
		})
		if walkErr != nil {
			return nil, nil, walkErr
		}
	}
	files := make([]string, 0, len(set))
	for f := range set {
		files = append(files, f)
	}
	sort.Strings(files)
	return files, fails, nil
}
