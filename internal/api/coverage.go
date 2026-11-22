package api

import (
	"context"

	"github.com/wamra/specs-index/internal/coverage"
	"github.com/wamra/specs-index/internal/store"
)

// CoverageItem is an entity named in a coverage list.
type CoverageItem struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Title string `json:"title"`
	Path  string `json:"path"`
	Line  int    `json:"line"`
}

// DanglingRef is an explicit edge end that no entity defines.
type DanglingRef struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Referrer string `json:"referrer"`
	Side     string `json:"side"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Mention  bool   `json:"mention"`
}

// DuplicateDef is a later definition of an ID already defined elsewhere.
type DuplicateDef struct {
	Key    string `json:"key"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Detail string `json:"detail"`
}

// KeyPair is two keys with where the first is declared.
type KeyPair struct {
	A    string `json:"a"`
	B    string `json:"b"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

// CoverageResult is the result of coverage. Candidates are suggestions from
// vector similarity, never conflicts; Conflicts are explicit conflicts_with
// pairs.
type CoverageResult struct {
	RequirementsWithoutDesign  []CoverageItem `json:"requirements_without_design"`
	DesignsWithoutVerification []CoverageItem `json:"designs_without_verification"`
	UnlinkedScenarios          []CoverageItem `json:"unlinked_scenarios"`
	Dangling                   []DanglingRef  `json:"dangling"`
	Duplicates                 []DuplicateDef `json:"duplicates"`
	Conflicts                  []KeyPair      `json:"conflicts"`
	Candidates                 []KeyPair      `json:"candidates"`
}

// Coverage reports the gaps in the indexed graph.
func Coverage(ctx context.Context, st *store.Store) (CoverageResult, error) {
	rep, err := coverage.Run(ctx, st)
	if err != nil {
		return CoverageResult{}, err
	}
	items := func(in []coverage.Item) []CoverageItem {
		out := make([]CoverageItem, 0, len(in))
		for _, i := range in {
			out = append(out, CoverageItem{Key: i.Key, Type: i.Type, Title: i.Title, Path: i.Path, Line: i.Line})
		}
		return out
	}
	pairs := func(in []coverage.Pair) []KeyPair {
		out := make([]KeyPair, 0, len(in))
		for _, p := range in {
			out = append(out, KeyPair{A: p.A, B: p.B, Path: p.Path, Line: p.Line})
		}
		return out
	}
	r := CoverageResult{
		RequirementsWithoutDesign:  items(rep.RequirementsWithoutDesign),
		DesignsWithoutVerification: items(rep.DesignsWithoutVerification),
		UnlinkedScenarios:          items(rep.UnlinkedScenarios),
		Dangling:                   make([]DanglingRef, 0, len(rep.Dangling)),
		Duplicates:                 make([]DuplicateDef, 0, len(rep.Duplicates)),
		Conflicts:                  pairs(rep.Conflicts),
		Candidates:                 pairs(rep.Candidates),
	}
	for _, d := range rep.Dangling {
		r.Dangling = append(r.Dangling, DanglingRef{ID: d.ID, Kind: d.Kind, Referrer: d.Referrer, Side: d.Side, Path: d.Path, Line: d.Line, Mention: d.Mention})
	}
	for _, d := range rep.Duplicates {
		r.Duplicates = append(r.Duplicates, DuplicateDef{Key: d.Key, Path: d.Path, Line: d.Line, Detail: d.Detail})
	}
	return r, nil
}
