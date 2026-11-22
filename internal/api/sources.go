package api

import (
	"path/filepath"
	"strings"

	"github.com/wamra/specs-index/internal/indexer"
	"github.com/wamra/specs-index/internal/store"
	"github.com/wamra/specs-index/internal/workspace"
)

// SourceInfo is one registered source.
type SourceInfo struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// SourcesResult is the result of sources, add and remove. Changed lists the
// paths added or removed by this call (empty for sources).
type SourcesResult struct {
	Changed []string     `json:"changed"`
	Sources []SourceInfo `json:"sources"`
}

// Sources lists the registered sources.
func Sources(st *store.Store) (SourcesResult, error) {
	list, err := indexer.ListSources(st)
	if err != nil {
		return SourcesResult{}, err
	}
	r := SourcesResult{Changed: []string{}, Sources: make([]SourceInfo, 0, len(list))}
	for _, s := range list {
		r.Sources = append(r.Sources, SourceInfo{Path: s.Path, Kind: s.Kind})
	}
	return r, nil
}

// absFrom resolves p against base (the caller's working directory) so that the
// indexer, which reads a relative path as relative to the root, gets an
// absolute one.
func absFrom(base, p string) string {
	if filepath.IsAbs(p) || base == "" {
		return p
	}
	return filepath.Join(base, p)
}

// Add registers paths as sources. Relative paths are resolved against base
// (normally the working directory). It stops at the first refused path.
func Add(st *store.Store, root, base string, paths []string) (SourcesResult, error) {
	var changed []string
	for _, p := range paths {
		abs := absFrom(base, p)
		if err := indexer.AddSource(st, root, abs); err != nil {
			return SourcesResult{}, err
		}
		rel, _ := workspace.Rel(root, abs)
		changed = append(changed, rel)
	}
	r, err := Sources(st)
	r.Changed = nonNil(changed)
	return r, err
}

// Remove unregisters paths (resolved against base) and drops the documents no
// remaining source covers.
func Remove(st *store.Store, root, base string, paths []string) (SourcesResult, error) {
	var changed []string
	for _, p := range paths {
		abs := absFrom(base, p)
		arg := abs
		rel, err := workspace.Rel(root, abs)
		if err != nil {
			// The path may be gone from disk: fall back to the lexical form.
			lex, lerr := filepath.Rel(root, abs)
			if lerr != nil || lex == ".." || strings.HasPrefix(lex, ".."+string(filepath.Separator)) {
				return SourcesResult{}, err
			}
			rel = filepath.ToSlash(lex)
			arg = rel
		}
		if err := indexer.RemoveSource(st, root, arg); err != nil {
			return SourcesResult{}, err
		}
		changed = append(changed, rel)
	}
	r, err := Sources(st)
	r.Changed = nonNil(changed)
	return r, err
}
