// Package graph walks the edge graph the indexer wrote: requirement to design
// to scenario and back.
package graph

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/wamra/specs-index/internal/store"
)

// DefaultDepth is used when TraceOpts.Depth is 0.
const DefaultDepth = 5

// defaultKinds are followed when TraceOpts.Kinds is empty. references,
// depends_on and conflicts_with are followed only when named.
var defaultKinds = []string{"parent_of", "implements", "verified_by"}

// TraceOpts selects what Trace walks.
//
// Direction is "down" (default), "up" or "both". Depth 0 means DefaultDepth.
// Kinds, when non-empty, is exactly the set of edge kinds followed (so
// ["references"] follows only mentions); empty means parent_of, implements and
// verified_by. IncludeInferred also follows explicit=0 edges.
type TraceOpts struct {
	Direction       string
	Depth           int
	Kinds           []string
	IncludeInferred bool
}

// Node is one entity in the trace tree.
//
// Via is empty on the root; otherwise "<kind> forward" when the edge was
// followed from its stored source to its stored target, or "<kind> reversed"
// when followed target to source. Explicit is false only for nodes reached over
// an inferred edge. Repeat marks a key already reached earlier in the same walk
// direction (breadth-first, so at its shallowest depth), whether through a cycle
// or a diamond: it is listed again but not expanded, which keeps output and
// queries linear in nodes and edges. Missing marks an edge end that no
// entity defines (Type, Title and Path are then empty). Path and Line locate
// the entity itself.
type Node struct {
	Key      string
	Type     string
	Title    string
	Path     string
	Line     int
	Via      string
	Explicit bool
	Repeat   bool
	Missing  bool
	Children []*Node
}

// UnknownKeyError is returned by Trace when no entity has the key.
type UnknownKeyError struct {
	Key  string
	Near []string
}

func (e *UnknownKeyError) Error() string {
	if len(e.Near) == 0 {
		return fmt.Sprintf("unknown key %q", e.Key)
	}
	return fmt.Sprintf("unknown key %q; near matches: %s", e.Key, strings.Join(e.Near, ", "))
}

type walker struct {
	db      *sql.DB
	kinds   []string
	inferr  bool
	maxDeep int
	info    map[string]*Node // entity info by key; nil value = no entity
}

// Trace returns the tree rooted at key.
func Trace(ctx context.Context, st *store.Store, key string, o TraceOpts) (*Node, error) {
	w := &walker{db: st.DB(), kinds: o.Kinds, inferr: o.IncludeInferred, maxDeep: o.Depth, info: map[string]*Node{}}
	if len(w.kinds) == 0 {
		w.kinds = defaultKinds
	}
	if w.maxDeep <= 0 {
		w.maxDeep = DefaultDepth
	}
	valid := map[string]bool{"parent_of": true, "implements": true, "verified_by": true, "depends_on": true, "conflicts_with": true, "references": true}
	var kinds []string
	seenKind := map[string]bool{}
	for _, k := range w.kinds {
		if !valid[k] {
			return nil, fmt.Errorf("unknown edge kind %q (valid: parent_of, implements, verified_by, depends_on, conflicts_with, references)", k)
		}
		if !seenKind[k] {
			seenKind[k] = true
			kinds = append(kinds, k)
		}
	}
	w.kinds = kinds
	dirs := []bool{true} // true = down
	switch o.Direction {
	case "", "down":
	case "up":
		dirs = []bool{false}
	case "both":
		dirs = []bool{true, false}
	default:
		return nil, fmt.Errorf("invalid direction %q (want down, up or both)", o.Direction)
	}
	root, err := w.entity(ctx, key)
	if err != nil {
		return nil, err
	}
	if root == nil {
		near, err := w.near(ctx, key)
		if err != nil {
			return nil, err
		}
		return nil, &UnknownKeyError{Key: key, Near: near}
	}
	n := *root
	n.Explicit = true
	for _, down := range dirs {
		kids, err := w.children(ctx, key, down)
		if err != nil {
			return nil, err
		}
		n.Children = append(n.Children, kids...)
	}
	return &n, nil
}

// entity returns a template node for key, or nil when no entity defines it.
func (w *walker) entity(ctx context.Context, key string) (*Node, error) {
	if n, ok := w.info[key]; ok {
		return n, nil
	}
	var n Node
	var title sql.NullString
	var line sql.NullInt64
	err := w.db.QueryRowContext(ctx, `SELECT e.type, e.title, d.path, e.start_line
		FROM entities e JOIN documents d ON d.id = e.doc_id WHERE e.key = ?`, key).Scan(&n.Type, &title, &n.Path, &line)
	if err == sql.ErrNoRows {
		w.info[key] = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	n.Key, n.Title, n.Line = key, title.String, int(line.Int64)
	w.info[key] = &n
	return &n, nil
}

type step struct {
	other    string
	kind     string
	forward  bool
	explicit bool
}

// steps lists the neighbours of key in one direction. implements is stored
// design -> requirement, so going down it is followed reversed; every other
// kind is followed forward going down. Up is the exact reverse.
func (w *walker) steps(ctx context.Context, key string, down bool) ([]step, error) {
	var out []step
	for _, kind := range w.kinds {
		forward := down != (kind == "implements")
		col, oth := "src_key", "dst_key"
		if !forward {
			col, oth = "dst_key", "src_key"
		}
		q := `SELECT ` + oth + `, MAX(explicit) FROM edges WHERE ` + col + ` = ? AND kind = ?`
		if !w.inferr {
			q += ` AND explicit = 1`
		}
		q += ` GROUP BY ` + oth + ` ORDER BY ` + oth
		rows, err := w.db.QueryContext(ctx, q, key, kind)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			s := step{kind: kind, forward: forward}
			var ex int
			if err := rows.Scan(&s.other, &ex); err != nil {
				rows.Close()
				return nil, err
			}
			s.explicit = ex != 0
			out = append(out, s)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// children expands the tree below key breadth-first, so a node is first
// reached (and expanded) at its shallowest depth. A key already reached in this
// walk, by a cycle or by another path, is emitted again with Repeat and no
// children; queries and output are linear in nodes and edges.
func (w *walker) children(ctx context.Context, key string, down bool) ([]*Node, error) {
	type item struct {
		n     *Node
		depth int
	}
	visited := map[string]bool{key: true}
	rootKids := &Node{}
	queue := []item{{rootKids, 0}}
	// rootKids is a holder; the root's key is expanded first.
	keys := map[*Node]string{rootKids: key}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		steps, err := w.steps(ctx, keys[it.n], down)
		if err != nil {
			return nil, err
		}
		for _, s := range steps {
			t, err := w.entity(ctx, s.other)
			if err != nil {
				return nil, err
			}
			n := &Node{Key: s.other, Explicit: s.explicit, Via: s.kind + " forward"}
			if !s.forward {
				n.Via = s.kind + " reversed"
			}
			if t == nil {
				n.Missing = true
			} else {
				n.Type, n.Title, n.Path, n.Line = t.Type, t.Title, t.Path, t.Line
			}
			it.n.Children = append(it.n.Children, n)
			if visited[s.other] {
				n.Repeat = true
				continue
			}
			visited[s.other] = true
			if it.depth+1 < w.maxDeep {
				keys[n] = s.other
				queue = append(queue, item{n, it.depth + 1})
			}
		}
	}
	return rootKids.Children, nil
}

// near returns up to 10 keys close to a key that does not exist: id_fts
// trigram matches, then keys sharing the longest prefix (at least 3 chars).
func (w *walker) near(ctx context.Context, key string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(q string, args ...any) error {
		rows, err := w.db.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return err
			}
			if !seen[k] && len(out) < 10 {
				seen[k] = true
				out = append(out, k)
			}
		}
		return rows.Err()
	}
	r := []rune(key)
	if len(r) >= 3 {
		q := `"` + strings.ReplaceAll(key, `"`, `""`) + `"`
		if err := add(`SELECT key FROM id_fts WHERE id_fts MATCH ? ORDER BY rank LIMIT 10`, q); err != nil {
			out = nil // a diagnostic must not fail on a bad MATCH
		}
	}
	for n := len(r); n >= 3 && len(out) == 0; n-- {
		p := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(string(r[:n]))
		if err := add(`SELECT key FROM entities WHERE key LIKE ? ESCAPE '\' ORDER BY key LIMIT 10`, p+"%"); err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}
