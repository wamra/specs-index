// Package coverage reports gaps in the indexed requirement/design/scenario
// graph: uncovered requirements and designs, unlinked scenarios, dangling and
// duplicate IDs, and conflicts. Only explicit edges (explicit=1) count;
// inferred suggestions never satisfy coverage.
package coverage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/wamra/specs-index/internal/markdown"
	"github.com/wamra/specs-index/internal/store"
	"github.com/wamra/specs-index/internal/vector"
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
// table and tag edges (non-mentions sort first). A mention is dangling only
// when its prefix is known (used by a defined entity or in the prefix
// mapping), so prose such as UTF-8 or SHA-256 is not reported.
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
// declaration (either side). For Candidates, A < B and Path/Line locate A.
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
// incoming verified_by edge. Candidates: pairs of same-type entities whose
// embeddings are closer than CandidateMaxDistance (cosine) but whose statuses
// differ; they are suggestions to look at, never conflicts. It is empty when
// the index has no vectors, and also when the scan was skipped (on request or
// above CandidateMaxVectors), which CandidatesNote then explains.
type Report struct {
	RequirementsWithoutDesign  []Item
	DesignsWithoutVerification []Item
	UnlinkedScenarios          []Item
	Dangling                   []Dangling
	Duplicates                 []Duplicate
	Conflicts                  []Pair
	Candidates                 []Pair
	CandidatesNote             string
}

// CandidateMaxDistance is the cosine distance below which two same-type
// entities with different statuses are listed as candidates.
const CandidateMaxDistance = 0.10

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

// Options selects what Run computes. NoCandidates skips the candidate scan.
// Types is the configured Markdown prefix mapping (merged over
// markdown.DefaultTypes); its prefixes count as known for dangling mentions.
type Options struct {
	NoCandidates bool
	Types        map[string]string
}

// CandidateMaxVectors is the number of embedded chunks above which the
// candidate scan (one nearest-neighbour query per vector, so its cost grows
// with the square of the count) is skipped and CandidatesNote says so.
const CandidateMaxVectors = 5000

var candidateLimit = CandidateMaxVectors // tests lower it

// Run computes the report.
func Run(ctx context.Context, st *store.Store, o Options) (Report, error) {
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
	known, err := knownPrefixes(ctx, db, o.Types)
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
		if mention && !known[prefixOf(dst)] {
			dstOK = true // ID-shaped prose such as UTF-8 or SHA-256, not a reference
		}
		if mention && !known[prefixOf(src)] {
			srcOK = true
		}
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
	for drows.Next() {
		var d Duplicate
		if err := drows.Scan(&d.Key, &d.Path, &d.Line, &d.Detail); err != nil {
			drows.Close()
			return r, err
		}
		r.Duplicates = append(r.Duplicates, d)
	}
	if err := drows.Close(); err != nil {
		return r, err
	}
	if err := drows.Err(); err != nil {
		return r, err
	}
	if o.NoCandidates {
		r.CandidatesNote = "candidate conflicts not computed: skipped on request"
		return r, nil
	}
	if r.Candidates, r.CandidatesNote, err = candidates(ctx, db); err != nil {
		return r, err
	}
	return r, nil
}

func prefixOf(key string) string {
	p, _, _ := strings.Cut(key, "-")
	return p
}

// knownPrefixes returns the ID prefixes of the defined entities and of the
// prefix mapping (defaults included).
func knownPrefixes(ctx context.Context, db *sql.DB, types map[string]string) (map[string]bool, error) {
	known := map[string]bool{}
	for p := range markdown.MergeTypes(types) {
		known[p] = true
	}
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT substr(key, 1, instr(key, '-') - 1) FROM entities WHERE instr(key, '-') > 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		known[p] = true
	}
	return known, rows.Err()
}

// candidates finds the near-duplicate pairs from the stored vectors. Above
// candidateLimit vectors it returns none and a note.
func candidates(ctx context.Context, db *sql.DB) ([]Pair, string, error) {
	out := []Pair{}
	vs := vector.NewVec1(db)
	metas, err := vs.Metas(ctx)
	if err != nil || len(metas) == 0 {
		return out, "", err
	}
	if len(metas) > candidateLimit {
		return out, fmt.Sprintf("candidate conflicts not computed: %d embedded chunks is more than the limit of %d (the scan grows with the square of the count)", len(metas), candidateLimit), nil
	}
	type info struct {
		key, typ, status, path string
		line                   int
	}
	rows, err := db.QueryContext(ctx, `SELECT c.id, e.key, e.type, COALESCE(e.status, ''), d.path, COALESCE(e.start_line, 0)
		FROM chunks c JOIN entities e ON e.id = c.entity_id JOIN documents d ON d.id = e.doc_id ORDER BY e.key`)
	if err != nil {
		return out, "", err
	}
	byChunk := map[int64]info{}
	var order []int64
	for rows.Next() {
		var id int64
		var i info
		if err := rows.Scan(&id, &i.key, &i.typ, &i.status, &i.path, &i.line); err != nil {
			rows.Close()
			return out, "", err
		}
		if _, ok := metas[id]; ok {
			byChunk[id] = i
			order = append(order, id)
		}
	}
	if err := rows.Close(); err != nil {
		return out, "", err
	}
	if err := rows.Err(); err != nil {
		return out, "", err
	}
	type pairKey struct{ a, b string }
	seen := map[pairKey]bool{}
	for _, id := range order {
		self := byChunk[id]
		en, ok, err := vs.Get(ctx, id)
		if err != nil {
			return out, "", err
		}
		if !ok {
			continue
		}
		// Ask for more neighbours until the farthest one returned is out of
		// range, so a cluster of near-duplicates is never cut off.
		for k := 8; ; k *= 4 {
			ms, err := vs.KNN(ctx, en.Vec, k, vector.Filter{Type: self.typ})
			if err != nil {
				return out, "", err
			}
			for _, m := range ms {
				o, ok := byChunk[m.ID]
				if !ok || m.ID == id || m.Distance >= CandidateMaxDistance ||
					strings.EqualFold(strings.TrimSpace(o.status), strings.TrimSpace(self.status)) {
					continue
				}
				a, b := self, o
				if b.key < a.key {
					a, b = b, a
				}
				if !seen[pairKey{a.key, b.key}] {
					seen[pairKey{a.key, b.key}] = true
					out = append(out, Pair{A: a.key, B: b.key, Path: a.path, Line: a.line})
				}
			}
			if len(ms) < k || ms[len(ms)-1].Distance >= CandidateMaxDistance {
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	return out, "", nil
}
