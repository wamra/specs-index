package search

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/wamra/specs-index/internal/store"
)

// Options controls a search. Mode is auto, exact, fts, vector or hybrid; in
// this version auto is exact-ID lookup followed by full text, and vector and
// hybrid report that no embedder is configured. K defaults to 10. Root is the
// workspace root used to mark stale hits; when empty staleness is not checked.
type Options struct {
	Mode string
	K    int
	Root string
}

// Hit is one search result. Source is exact, fts or filter (a query made of
// filters only). Score is the negated bm25 rank for fts hits, zero otherwise.
type Hit struct {
	Key, Type, Title, Path string
	StartLine, EndLine     int
	Status, Snippet        string
	Score                  float64
	Stale                  bool
	Source                 string
}

const defaultK = 10

type docState struct {
	size, mtime sql.NullInt64
	sha         sql.NullString
}

type found struct {
	Hit
	doc docState
}

// Search runs q against the index.
func Search(ctx context.Context, st *store.Store, q Query, o Options) ([]Hit, error) {
	mode := o.Mode
	if mode == "" {
		mode = "auto"
	}
	k := o.K
	if k <= 0 {
		k = defaultK
	}
	switch mode {
	case "auto", "exact", "fts":
	case "vector", "hybrid":
		return nil, fmt.Errorf("%s search needs an embedder, but no embedder is configured", mode)
	default:
		return nil, fmt.Errorf("unknown search mode %q (use auto, exact, fts, vector or hybrid)", mode)
	}
	s := &searcher{ctx: ctx, db: st.DB(), q: q}
	s.where, s.args = s.filterSQL()

	var res []found
	seen := map[string]bool{}
	add := func(fs []found) {
		for _, f := range fs {
			if !seen[f.Key] && len(res) < k {
				seen[f.Key] = true
				res = append(res, f)
			}
		}
	}
	if mode != "fts" {
		fs, err := s.exact()
		if err != nil {
			return nil, err
		}
		add(fs)
	}
	if mode != "exact" {
		fs, err := s.text(k + len(res))
		if err != nil {
			return nil, err
		}
		add(fs)
	}
	if mode == "auto" && len(res) == 0 && q.Text == "" && len(q.IDs) == 0 && len(q.Exclude) == 0 &&
		len(q.Filters) == 0 && len(q.Has) == 0 && len(q.HasNot) == 0 {
		return nil, errors.New("empty search query")
	}
	hits := make([]Hit, len(res))
	stale := map[string]bool{}
	for i, f := range res {
		hits[i] = f.Hit
		if o.Root != "" {
			v, ok := stale[f.Path]
			if !ok {
				v = isStale(o.Root, f.Path, f.doc)
				stale[f.Path] = v
			}
			hits[i].Stale = v
		}
	}
	return hits, nil
}

type searcher struct {
	ctx   context.Context
	db    *sql.DB
	q     Query
	where string // extra conditions over aliases e (entities) and d (documents), starting with " AND "
	args  []any
}

const baseSelect = `SELECT e.key, e.type, COALESCE(e.title,''), d.path, COALESCE(e.start_line,0), COALESCE(e.end_line,0),
	COALESCE(e.status,''), d.size, d.mtime, d.sha256`

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(s))
}

// filterSQL turns filters and has: predicates into AND-ed conditions. Only
// explicit edges count for has:fds (incoming implements) and has:scenario
// (verified_by to a scenario).
func (s *searcher) filterSQL() (string, []any) {
	var sb strings.Builder
	var args []any
	for _, f := range s.q.Filters {
		var cond string
		switch f.Field {
		case "type", "status", "priority":
			cond = "COALESCE(lower(e." + f.Field + "),'') = lower(?)"
			args = append(args, f.Value)
		case "tag":
			cond = `(',' || lower(COALESCE(e.tags,'')) || ',') LIKE ? ESCAPE '\'`
			args = append(args, "%,"+likeEscape(f.Value)+",%")
		case "path":
			cond = `lower(d.path) LIKE ? ESCAPE '\'`
			args = append(args, "%"+likeEscape(f.Value)+"%")
		default:
			continue
		}
		if f.Negate {
			cond = "NOT (" + cond + ")"
		}
		sb.WriteString(" AND " + cond)
	}
	has := map[string]string{
		"fds":      `EXISTS (SELECT 1 FROM edges x WHERE x.dst_key = e.key AND x.kind = 'implements' AND x.explicit = 1)`,
		"scenario": `EXISTS (SELECT 1 FROM edges x JOIN entities t ON t.key = x.dst_key WHERE x.src_key = e.key AND x.kind = 'verified_by' AND x.explicit = 1 AND t.type = 'scenario')`,
	}
	for _, h := range s.q.Has {
		sb.WriteString(" AND " + has[h])
	}
	for _, h := range s.q.HasNot {
		sb.WriteString(" AND NOT " + has[h])
	}
	return sb.String(), args
}

func scanFound(rows *sql.Rows, withSnippet bool) (found, error) {
	var f found
	var snippet sql.NullString
	var sc sql.NullFloat64
	dest := []any{&f.Key, &f.Type, &f.Title, &f.Path, &f.StartLine, &f.EndLine, &f.Status, &f.doc.size, &f.doc.mtime, &f.doc.sha}
	if withSnippet {
		dest = append(dest, &snippet, &sc)
	}
	if err := rows.Scan(dest...); err != nil {
		return f, err
	}
	f.Snippet = oneLine(snippet.String)
	f.Score = sc.Float64
	return f, nil
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func (s *searcher) collect(rows *sql.Rows, withSnippet bool, source string) ([]found, error) {
	defer rows.Close()
	var out []found
	for rows.Next() {
		f, err := scanFound(rows, withSnippet)
		if err != nil {
			return nil, err
		}
		f.Source = source
		out = append(out, f)
	}
	return out, rows.Err()
}

// exact returns the entities whose key is one of the query's IDs, in query
// order, subject to the filters.
func (s *searcher) exact() ([]found, error) {
	var out []found
	for _, id := range s.q.IDs {
		rows, err := s.db.QueryContext(s.ctx, baseSelect+`, substr(COALESCE(e.body,''),1,200), 0
			FROM entities e JOIN documents d ON d.id = e.doc_id WHERE e.key = ?`+s.where,
			append([]any{id}, s.args...)...)
		if err != nil {
			return nil, err
		}
		fs, err := s.collect(rows, true, "exact")
		if err != nil {
			return nil, err
		}
		out = append(out, fs...)
	}
	return out, nil
}

// ftsExpr builds the MATCH expression, or "" when the query has no text part.
func (s *searcher) ftsExpr() string {
	expr := s.q.Text
	if expr == "" && len(s.q.IDs) > 0 {
		var ids []string
		for _, id := range s.q.IDs {
			ids = append(ids, quote(id))
		}
		expr = strings.Join(ids, " OR ")
	}
	if expr != "" && len(s.q.Exclude) > 0 {
		var ex []string
		for _, e := range s.q.Exclude {
			ex = append(ex, quote(e))
		}
		expr = "(" + expr + ") NOT (" + strings.Join(ex, " OR ") + ")"
	}
	return expr
}

// text runs the full-text search, or a filter-only listing when the query
// has no words.
func (s *searcher) text(limit int) ([]found, error) {
	expr := s.ftsExpr()
	if expr == "" {
		return s.filterOnly(limit)
	}
	rows, err := s.db.QueryContext(s.ctx, baseSelect+`,
		snippet(chunk_fts, -1, '[', ']', '...', 16), -bm25(chunk_fts, 4.0, 1.0, 1.0, 2.0)
		FROM chunk_fts JOIN entities e ON e.id = chunk_fts.rowid JOIN documents d ON d.id = e.doc_id
		WHERE chunk_fts MATCH ?`+s.where+` ORDER BY bm25(chunk_fts, 4.0, 1.0, 1.0, 2.0), e.key LIMIT ?`,
		append(append([]any{expr}, s.args...), limit)...)
	if err != nil {
		return nil, badFTS(s.q, expr, err)
	}
	fs, err := s.collect(rows, true, "fts")
	if err != nil {
		return nil, badFTS(s.q, expr, err)
	}
	return fs, nil
}

func badFTS(q Query, expr string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	name := q.Text
	if name == "" {
		name = expr
	}
	return fmt.Errorf("invalid search query (%s): %v", name, err)
}

// filterOnly lists entities matching the filters when the query has no text.
func (s *searcher) filterOnly(limit int) ([]found, error) {
	if len(s.q.Filters) == 0 && len(s.q.Has) == 0 && len(s.q.HasNot) == 0 && len(s.q.Exclude) == 0 {
		return nil, nil
	}
	where, args := s.where, append([]any(nil), s.args...)
	for _, e := range s.q.Exclude {
		where += ` AND NOT EXISTS (SELECT 1 FROM chunks c WHERE c.entity_id = e.id AND lower(c.text) LIKE ? ESCAPE '\')`
		args = append(args, "%"+likeEscape(e)+"%")
	}
	rows, err := s.db.QueryContext(s.ctx, baseSelect+`, substr(COALESCE(e.body,''),1,200), 0
		FROM entities e JOIN documents d ON d.id = e.doc_id WHERE 1=1`+where+` ORDER BY e.key LIMIT ?`,
		append(args, limit)...)
	if err != nil {
		return nil, err
	}
	return s.collect(rows, true, "filter")
}

// isStale reports whether the file behind a document changed since it was
// indexed: size and mtime are compared first, then the sha256. A missing or
// unreadable file counts as stale. Files are only read, never written.
func isStale(root, rel string, d docState) bool {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Stat(abs)
	if err != nil {
		return true
	}
	if d.size.Valid && d.mtime.Valid && d.size.Int64 == fi.Size() && d.mtime.Int64 == fi.ModTime().UnixNano() {
		return false
	}
	if !d.sha.Valid || d.sha.String == "" {
		return true
	}
	f, err := os.Open(abs)
	if err != nil {
		return true
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return true
	}
	return hex.EncodeToString(h.Sum(nil)) != d.sha.String
}
