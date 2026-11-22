// Package coverage reports gaps in the indexed requirement/design/scenario
// graph: uncovered requirements and designs, unlinked scenarios, dangling and
// duplicate IDs, and conflicts. Only explicit edges (explicit=1) count;
// inferred suggestions never satisfy coverage.
package coverage

import (
	"context"
	"database/sql"
	"sort"

	"github.com/wamra/specs-index/internal/store"
)

// Item is an entity named in a coverage list.
type Item struct {
	Key   string
	Type  string
	Title string
	Path  string
	Line  int
}

// Dangling is an explicit edge end that no entity defines. ID is the undefined
// key; Referrer is the entity at the other end of the edge; Side is "target"
// when the undefined ID is the edge's destination and "source" when it is the
// edge's source. Path and Line say where the edge is written. Mention is true
// for references/origin=mention edges, which are listed apart from field,
// table and tag edges (non-mentions sort first).
type Dangling struct {
	ID       string
	Kind     string
	Referrer string
	Side     string
	Path     string
	Line     int
	Mention  bool
}

// Duplicate is a later definition of an ID already defined elsewhere. Path and
// Line locate the duplicate definition; Detail names the owning path.
type Duplicate struct {
	Key    string
	Path   string
	Line   int
	Detail string
}

// Pair is two keys. For Conflicts, A < B and Path/Line locate the first
// declaration (either side). Candidates are filled by a later task.
type Pair struct {
	A, B string
	Path string
	Line int
}

// Report is the coverage result. All lists are sorted and non-nil.
//
// RequirementsWithoutDesign: requirements that are the target of no explicit
// implements edge. DesignsWithoutVerification: designs that are the source of
// no explicit verified_by edge (to a scenario or an acceptance criterion; the
// corpus has no design verified only by an AC, so this matches the truth's
// designs_without_scenario). UnlinkedScenarios: scenarios with no explicit
// incoming verified_by edge.
type Report struct {
	RequirementsWithoutDesign  []Item
	DesignsWithoutVerification []Item
	UnlinkedScenarios          []Item
	Dangling                   []Dangling
	Duplicates                 []Duplicate
	Conflicts                  []Pair
	Candidates                 []Pair
}

const itemSelect = `SELECT e.key, e.type, e.title, d.path, e.start_line FROM entities e JOIN documents d ON d.id = e.doc_id `

func items(ctx context.Context, db *sql.DB, q string) ([]Item, error) {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var i Item
		var title sql.NullString
		var line sql.NullInt64
		if err := rows.Scan(&i.Key, &i.Type, &title, &i.Path, &line); err != nil {
			return nil, err
		}
		i.Title, i.Line = title.String, int(line.Int64)
		out = append(out, i)
	}
	return out, rows.Err()
}

// Run computes the report.
func Run(ctx context.Context, st *store.Store) (Report, error) {
	db := st.DB()
	r := Report{Dangling: []Dangling{}, Duplicates: []Duplicate{}, Conflicts: []Pair{}, Candidates: []Pair{}}
	var err error
	if r.RequirementsWithoutDesign, err = items(ctx, db, itemSelect+`WHERE e.type = 'requirement' AND NOT EXISTS
		(SELECT 1 FROM edges x WHERE x.kind = 'implements' AND x.explicit = 1 AND x.dst_key = e.key) ORDER BY e.key`); err != nil {
		return r, err
	}
	if r.DesignsWithoutVerification, err = items(ctx, db, itemSelect+`WHERE e.type = 'design' AND NOT EXISTS
		(SELECT 1 FROM edges x WHERE x.kind = 'verified_by' AND x.explicit = 1 AND x.src_key = e.key) ORDER BY e.key`); err != nil {
		return r, err
	}
	if r.UnlinkedScenarios, err = items(ctx, db, itemSelect+`WHERE e.type = 'scenario' AND NOT EXISTS
		(SELECT 1 FROM edges x WHERE x.kind = 'verified_by' AND x.explicit = 1 AND x.dst_key = e.key) ORDER BY e.key`); err != nil {
		return r, err
	}

	rows, err := db.QueryContext(ctx, `SELECT x.src_key, x.dst_key, x.kind, x.origin, COALESCE(d.path, ''), COALESCE(x.line, 0),
			EXISTS(SELECT 1 FROM entities WHERE key = x.src_key), EXISTS(SELECT 1 FROM entities WHERE key = x.dst_key)
		FROM edges x LEFT JOIN documents d ON d.id = x.doc_id WHERE x.explicit = 1
		ORDER BY x.id`)
	if err != nil {
		return r, err
	}
	type pairKey struct{ a, b string }
	seenConf := map[pairKey]bool{}
	for rows.Next() {
		var src, dst, kind, path string
		var origin sql.NullString
		var line int
		var srcOK, dstOK bool
		if err := rows.Scan(&src, &dst, &kind, &origin, &path, &line, &srcOK, &dstOK); err != nil {
			rows.Close()
			return r, err
		}
		mention := kind == "references" || origin.String == "mention"
		if !dstOK {
			r.Dangling = append(r.Dangling, Dangling{ID: dst, Kind: kind, Referrer: src, Side: "target", Path: path, Line: line, Mention: mention})
		}
		if !srcOK {
			r.Dangling = append(r.Dangling, Dangling{ID: src, Kind: kind, Referrer: dst, Side: "source", Path: path, Line: line, Mention: mention})
		}
		if kind == "conflicts_with" {
			a, b := src, dst
			if b < a {
				a, b = b, a
			}
			if !seenConf[pairKey{a, b}] {
				seenConf[pairKey{a, b}] = true
				r.Conflicts = append(r.Conflicts, Pair{A: a, B: b, Path: path, Line: line})
			}
		}
	}
	if err := rows.Close(); err != nil {
		return r, err
	}
	sort.SliceStable(r.Dangling, func(i, j int) bool {
		a, b := r.Dangling[i], r.Dangling[j]
		if a.Mention != b.Mention {
			return !a.Mention
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Referrer < b.Referrer
	})
	sort.Slice(r.Conflicts, func(i, j int) bool {
		a, b := r.Conflicts[i], r.Conflicts[j]
		if a.A != b.A {
			return a.A < b.A
		}
		return a.B < b.B
	})

	drows, err := db.QueryContext(ctx, `SELECT f.key, COALESCE(d.path, ''), COALESCE(f.line, 0), COALESCE(f.detail, '')
		FROM findings f LEFT JOIN documents d ON d.id = f.doc_id WHERE f.kind = 'duplicate' ORDER BY f.key, d.path, f.line`)
	if err != nil {
		return r, err
	}
	defer drows.Close()
	for drows.Next() {
		var d Duplicate
		if err := drows.Scan(&d.Key, &d.Path, &d.Line, &d.Detail); err != nil {
			return r, err
		}
		r.Duplicates = append(r.Duplicates, d)
	}
	return r, drows.Err()
}
