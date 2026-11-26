package api

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wamra/specs-index/internal/config"
	"github.com/wamra/specs-index/internal/embed"
	"github.com/wamra/specs-index/internal/graph"
	"github.com/wamra/specs-index/internal/provider"
	"github.com/wamra/specs-index/internal/search"
	"github.com/wamra/specs-index/internal/store"
	"github.com/wamra/specs-index/internal/workspace"
)

// SearchOptions are the flags of search.
type SearchOptions struct {
	Mode       string // auto (default), exact, fts, vector, hybrid
	K          int
	AllowCloud bool
}

// HitInfo is one search result. Source is exact, fts, vector or filter.
type HitInfo struct {
	Key       string  `json:"key"`
	Type      string  `json:"type"`
	Title     string  `json:"title"`
	Path      string  `json:"path"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Status    string  `json:"status"`
	Snippet   string  `json:"snippet"`
	Score     float64 `json:"score"`
	Stale     bool    `json:"stale"`
	Source    string  `json:"source"`
}

// SearchResult is the result of search. Notes explains a degraded run (for
// example a cloud embedder that is not allowed).
type SearchResult struct {
	Query string    `json:"query"`
	Mode  string    `json:"mode"`
	Hits  []HitInfo `json:"hits"`
	Notes []string  `json:"notes"`
	// Notice is set when auto mode degraded to exact and full-text results
	// (fingerprint mismatch, no embeddings yet, embedder down); omitted otherwise.
	Notice string `json:"notice,omitempty"`
}

// ValidateSearchOptions rejects an unknown mode with ErrInvalidArgument.
func ValidateSearchOptions(o SearchOptions) error {
	switch o.Mode {
	case "", "auto", "exact", "fts", "vector", "hybrid":
		return nil
	}
	return fmt.Errorf("%w: unknown search mode %q (use auto, exact, fts, vector or hybrid)", ErrInvalidArgument, o.Mode)
}

// QueryEmbedTimeout bounds the embedding of a search query. In auto mode a
// timeout degrades to full text with a notice; in vector and hybrid mode it is
// an error. A variable so tests can shorten it.
var QueryEmbedTimeout = 5 * time.Second

// Search runs query against the index. In auto mode a missing or cloud-blocked
// embedder, an unreachable or unresponsive one, or an embedder that differs from
// the one the index was built with means exact and full-text results plus a
// notice; vector and hybrid modes report the problem as an error. Database
// errors are errors in every mode.
func Search(ctx context.Context, st *store.Store, root, query string, cfg config.Config, o SearchOptions) (SearchResult, error) {
	mode := o.Mode
	if mode == "" {
		mode = "auto"
	}
	res := SearchResult{Query: query, Mode: mode, Hits: []HitInfo{}, Notes: []string{}}
	if err := ValidateSearchOptions(o); err != nil {
		return res, err
	}
	// A query that is exactly an entity key (any key, including scenario keys
	// and feature paths) finds that entity first.
	var exact []search.Hit
	if mode != "fts" {
		if k := strings.TrimSpace(query); k != "" {
			var err error
			if exact, err = search.Search(ctx, st, search.Query{IDs: []string{k}}, search.Options{Mode: "exact", K: 1, Root: root}); err != nil {
				return res, err
			}
		}
	}
	q, perr := search.ParseQuery(query)
	if perr != nil && len(exact) == 0 {
		return res, perr
	}
	var hits []search.Hit
	if perr == nil {
		emb, state, note, err := SetupEmbedder(cfg, o.AllowCloud)
		if err != nil {
			return res, err
		}
		if state == EmbedCloudBlocked {
			if mode == "vector" || mode == "hybrid" {
				return res, errors.New(note)
			}
			res.Notes = append(res.Notes, "full text only: "+note)
		}
		if emb != nil {
			emb = timedEmbedder{emb, QueryEmbedTimeout}
			if mode == "vector" || mode == "hybrid" {
				// search skips the vector side for a pure-ID query, so check the
				// embedder here: an explicit vector mode must not succeed silently
				// with an embedder that does not match the index.
				if err := embed.CheckFingerprint(ctx, st, emb); err != nil {
					return res, err
				}
			}
		}
		var notice string
		if hits, err = search.Search(ctx, st, q, search.Options{Mode: mode, K: o.K, Root: root, Embedder: emb, Notice: &notice}); err != nil {
			return res, err
		}
		res.Notice = notice
	}
	if len(exact) > 0 {
		merged := append([]search.Hit{}, exact...)
		for _, h := range hits {
			if h.Key != exact[0].Key {
				merged = append(merged, h)
			}
		}
		if k := o.K; k > 0 && len(merged) > k {
			merged = merged[:k]
		}
		hits = merged
	}
	for _, h := range hits {
		res.Hits = append(res.Hits, HitInfo{
			Key: h.Key, Type: h.Type, Title: h.Title, Path: h.Path, StartLine: h.StartLine, EndLine: h.EndLine,
			Status: h.Status, Snippet: h.Snippet, Score: round6(h.Score), Stale: h.Stale, Source: h.Source,
		})
	}
	return res, nil
}

// timedEmbedder bounds every call to the embedder made on behalf of a search
// (the query embedding and the dimension probe). A timeout is reported as
// provider.ErrDown saying the embedder is unresponsive, so auto mode degrades
// with a notice and explicit modes fail.
type timedEmbedder struct {
	embed.Embedder
	d time.Duration
}

func (t timedEmbedder) bound(ctx context.Context, call func(context.Context) error) error {
	cctx, cancel := context.WithTimeout(ctx, t.d)
	defer cancel()
	err := call(cctx)
	if err != nil && cctx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
		return fmt.Errorf("%w: the embedder is unresponsive (no answer within %s)", provider.ErrDown, t.d)
	}
	return err
}

func (t timedEmbedder) Embed(ctx context.Context, texts []string) (out [][]float32, err error) {
	err = t.bound(ctx, func(c context.Context) (e error) { out, e = t.Embedder.Embed(c, texts); return })
	return
}

func (t timedEmbedder) EmbedQuery(ctx context.Context, q string) (out []float32, err error) {
	err = t.bound(ctx, func(c context.Context) (e error) { out, e = t.Embedder.EmbedQuery(c, q); return })
	return
}

// Detect forwards the dimension probe when the embedder has one.
func (t timedEmbedder) Detect(ctx context.Context) error {
	d, ok := t.Embedder.(interface{ Detect(context.Context) error })
	if !ok {
		return nil
	}
	return t.bound(ctx, d.Detect)
}

// EdgeInfo is one edge as seen from an entity: Key is the entity at the other
// end, Missing is true when no entity defines it. Path and Line say where the
// edge is written.
type EdgeInfo struct {
	Kind     string `json:"kind"`
	Key      string `json:"key"`
	Missing  bool   `json:"missing"`
	Explicit bool   `json:"explicit"`
	Origin   string `json:"origin"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
}

// StepInfo is one Gherkin step.
type StepInfo struct {
	Keyword string `json:"keyword"`
	Text    string `json:"text"`
	Line    int    `json:"line"`
}

// ShowResult is the result of show. Source holds the entity's lines read from
// its file; Stale is true when the file changed since indexing (the lines may
// then not match the indexed entity).
type ShowResult struct {
	Key       string     `json:"key"`
	Type      string     `json:"type"`
	Title     string     `json:"title"`
	Status    string     `json:"status"`
	Priority  string     `json:"priority"`
	Tags      []string   `json:"tags"`
	Path      string     `json:"path"`
	StartLine int        `json:"start_line"`
	EndLine   int        `json:"end_line"`
	Stale     bool       `json:"stale"`
	Source    string     `json:"source"`
	Steps     []StepInfo `json:"steps"`
	Outgoing  []EdgeInfo `json:"outgoing"`
	Incoming  []EdgeInfo `json:"incoming"`
}

// Show returns the entity key with its source lines and direct edges. An
// unknown key returns *graph.UnknownKeyError.
func Show(ctx context.Context, st *store.Store, root, key string) (ShowResult, error) {
	db := st.DB()
	r := ShowResult{Tags: []string{}, Steps: []StepInfo{}, Outgoing: []EdgeInfo{}, Incoming: []EdgeInfo{}}
	var id int64
	var title, status, priority, tags sql.NullString
	var start, end sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT e.id, e.key, e.type, e.title, e.status, e.priority, e.tags, e.start_line, e.end_line, d.path
		FROM entities e JOIN documents d ON d.id = e.doc_id WHERE e.key = ?`, key).
		Scan(&id, &r.Key, &r.Type, &title, &status, &priority, &tags, &start, &end, &r.Path)
	if err == sql.ErrNoRows {
		_, terr := graph.Trace(ctx, st, key, graph.TraceOpts{Depth: 1})
		if terr == nil {
			terr = &graph.UnknownKeyError{Key: key}
		}
		return r, terr
	}
	if err != nil {
		return r, err
	}
	r.Title, r.Status, r.Priority = title.String, status.String, priority.String
	r.StartLine, r.EndLine = int(start.Int64), int(end.Int64)
	for _, t := range strings.Split(tags.String, ",") {
		if t = strings.TrimSpace(t); t != "" {
			r.Tags = append(r.Tags, t)
		}
	}
	hits, err := search.Search(ctx, st, search.Query{IDs: []string{key}}, search.Options{Mode: "exact", K: 1, Root: root})
	if err != nil {
		return r, err
	}
	r.Stale = len(hits) == 1 && hits[0].Stale
	r.Source = readLines(root, r.Path, r.StartLine, r.EndLine)

	rows, err := db.QueryContext(ctx, `SELECT seq, keyword, text, line FROM steps WHERE entity_id = ? ORDER BY seq`, id)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var s StepInfo
		var kw, tx sql.NullString
		var ln sql.NullInt64
		if err := rows.Scan(new(int), &kw, &tx, &ln); err != nil {
			rows.Close()
			return r, err
		}
		s.Keyword, s.Text, s.Line = kw.String, tx.String, int(ln.Int64)
		r.Steps = append(r.Steps, s)
	}
	rows.Close()

	edges := func(out bool) ([]EdgeInfo, error) {
		self, other := "src_key", "dst_key"
		if !out {
			self, other = other, self
		}
		rows, err := db.QueryContext(ctx, `SELECT x.kind, x.`+other+`, EXISTS(SELECT 1 FROM entities WHERE key = x.`+other+`),
				x.explicit, COALESCE(x.origin, ''), COALESCE(d.path, ''), COALESCE(x.line, 0)
			FROM edges x LEFT JOIN documents d ON d.id = x.doc_id WHERE x.`+self+` = ?
			ORDER BY x.kind, x.`+other+`, x.line, x.id`, key)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		list := []EdgeInfo{}
		for rows.Next() {
			var e EdgeInfo
			var exists, explicit int
			if err := rows.Scan(&e.Kind, &e.Key, &exists, &explicit, &e.Origin, &e.Path, &e.Line); err != nil {
				return nil, err
			}
			e.Missing, e.Explicit = exists == 0, explicit == 1
			list = append(list, e)
		}
		return list, rows.Err()
	}
	if r.Outgoing, err = edges(true); err != nil {
		return r, err
	}
	if r.Incoming, err = edges(false); err != nil {
		return r, err
	}
	return r, nil
}

// readLines returns lines start..end (1-based, inclusive) of the file under
// root, or "" when it cannot be read. The file is only read.
func readLines(root, rel string, start, end int) string {
	// The path comes from the index, which a document repository can ship
	// pre-filled: never read outside the root.
	if _, err := workspace.Rel(root, rel); err != nil {
		return ""
	}
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for n := 1; sc.Scan(); n++ {
		if n > end {
			break
		}
		if n >= start {
			out = append(out, strings.TrimRight(sc.Text(), "\r"))
		}
	}
	return strings.Join(out, "\n")
}
