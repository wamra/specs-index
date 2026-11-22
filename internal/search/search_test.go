package search

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wamra/specs-index/internal/config"
	"github.com/wamra/specs-index/internal/corpus"
	"github.com/wamra/specs-index/internal/embed"
	"github.com/wamra/specs-index/internal/embedtest"
	"github.com/wamra/specs-index/internal/indexer"
	"github.com/wamra/specs-index/internal/store"
	"github.com/wamra/specs-index/internal/vector"
)

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// indexedCorpus copies the corpus to a temp root, indexes it and returns both.
func indexedCorpus(t *testing.T) (string, *store.Store) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	copyDir(t, corpus.Dir(), root)
	dbDir, _ := filepath.EvalSymlinks(t.TempDir())
	st, err := store.Open(filepath.Join(dbDir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, s := range []string{"docs", "features"} {
		if err := indexer.AddSource(st, root, s); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := indexer.Run(context.Background(), st, root, nil)
	if err != nil || rep.Failed != 0 {
		t.Fatalf("index: %v %+v", err, rep)
	}
	return root, st
}

func run(t *testing.T, st *store.Store, root, query string, k int) []Hit {
	t.Helper()
	q, err := ParseQuery(query)
	if err != nil {
		t.Fatalf("parse %q: %v", query, err)
	}
	hits, err := Search(context.Background(), st, q, Options{Mode: "auto", K: k, Root: root})
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	return hits
}

func keys(hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.Key)
	}
	return out
}

func TestExactIDIsFirst(t *testing.T) {
	root, st := indexedCorpus(t)
	// FDS-010 is also mentioned in the body of BRD-018: it still comes first
	hits := run(t, st, root, "FDS-010", 10)
	if len(hits) < 2 || hits[0].Key != "FDS-010" || hits[0].Source != "exact" {
		t.Fatalf("want exact FDS-010 first, then mentions: %+v", hits)
	}
	if !strings.Contains(strings.Join(keys(hits), " "), "BRD-018") {
		t.Errorf("mention BRD-018 missing: %v", keys(hits))
	}
	hits = run(t, st, root, "BRD-004", 10)
	h := hits[0]
	if h.Key != "BRD-004" || h.Source != "exact" {
		t.Fatalf("first hit = %+v", h)
	}
	if h.Path != "docs/brd.md" || h.StartLine == 0 || h.EndLine < h.StartLine || h.Type != "requirement" || h.Title == "" {
		t.Errorf("hit fields: %+v", h)
	}
	seen := map[string]bool{}
	for _, x := range hits {
		if seen[x.Key] {
			t.Errorf("duplicate key %s in %v", x.Key, keys(hits))
		}
		seen[x.Key] = true
	}
}

func TestTruthQueries(t *testing.T) {
	root, st := indexedCorpus(t)
	truth, err := corpus.LoadTruth()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range truth.Queries {
		hits := run(t, st, root, q.Q, 10)
		top := map[string]bool{}
		for i, h := range hits {
			if i < 5 {
				top[h.Key] = true
			}
		}
		for _, k := range q.ExpectTop {
			if !top[k] {
				t.Errorf("%q: %s not in top 5: %v", q.Q, k, keys(hits))
			}
		}
		for _, k := range q.ExpectAbsent {
			for _, h := range hits {
				if h.Key == k {
					t.Errorf("%q: %s must be absent: %v", q.Q, k, keys(hits))
				}
			}
		}
	}
}

func TestExcludedTag(t *testing.T) {
	root, st := indexedCorpus(t)
	with := run(t, st, root, "legal hold", 20)
	found := false
	for _, h := range with {
		if h.Key == "features/lifecycle.feature#Legal hold blocks deletion" {
			found = true
		}
	}
	if !found {
		t.Fatalf("scenario should match without the exclusion: %v", keys(with))
	}
	for _, h := range run(t, st, root, "legal hold -tag:wip", 20) {
		if strings.Contains(h.Key, "Legal hold blocks deletion") {
			t.Errorf("wip scenario returned: %v", h.Key)
		}
	}
	tagged := run(t, st, root, "tag:wip", 50)
	if len(tagged) == 0 {
		t.Fatal("tag:wip should match something")
	}
	for _, h := range tagged {
		if h.Type != "scenario" {
			t.Errorf("tag:wip returned %s (%s)", h.Key, h.Type)
		}
	}
}

func TestExcludedPhrase(t *testing.T) {
	root, st := indexedCorpus(t)
	base := run(t, st, root, "upload", 50)
	if len(base) == 0 {
		t.Fatal("no upload hits")
	}
	// exclude a phrase that occurs in the first hit
	first := base[0]
	var title string
	if err := st.DB().QueryRow(`SELECT title FROM entities WHERE key = ?`, first.Key).Scan(&title); err != nil {
		t.Fatal(err)
	}
	for _, h := range run(t, st, root, `upload -"`+title+`"`, 50) {
		if h.Key == first.Key {
			t.Errorf("%s should be excluded by its own title", first.Key)
		}
	}
}

func TestHasPredicatesUseExplicitEdgesOnly(t *testing.T) {
	root, st := indexedCorpus(t)
	truth, _ := corpus.LoadTruth()
	typ := map[string]string{}
	for _, e := range truth.Entities {
		typ[e.Key] = e.Type
	}
	wantImpl, wantScn := map[string]bool{}, map[string]bool{}
	for _, e := range truth.Edges {
		switch {
		case e.Kind == "implements":
			wantImpl[e.Dst] = true
		case e.Kind == "verified_by" && typ[e.Dst] == "scenario":
			wantScn[e.Src] = true
		}
	}
	// inferred edges must never count
	db := st.DB()
	for _, q := range []string{
		`INSERT INTO edges(src_key,dst_key,kind,explicit,origin) VALUES ('FDS-014','BRD-015','implements',0,'inferred')`,
		`INSERT INTO edges(src_key,dst_key,kind,explicit,origin) VALUES ('BRD-015','features/access.feature#Warn before a session times out','verified_by',0,'inferred')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	check := func(query string, want func(Hit) bool, base string) {
		t.Helper()
		got := map[string]bool{}
		for _, h := range run(t, st, root, query, 500) {
			got[h.Key] = true
		}
		var all []string
		rows, _ := db.Query(base)
		for rows.Next() {
			var k string
			rows.Scan(&k)
			all = append(all, k)
		}
		rows.Close()
		for _, k := range all {
			if want(Hit{Key: k}) != got[k] {
				t.Errorf("%q: key %s: got %v want %v", query, k, got[k], !got[k])
			}
		}
		if len(got) == 0 {
			t.Errorf("%q: no hits", query)
		}
	}
	reqs := `SELECT key FROM entities WHERE type='requirement'`
	check("type:requirement has:fds", func(h Hit) bool { return wantImpl[h.Key] }, reqs)
	check("type:requirement -has:fds", func(h Hit) bool { return !wantImpl[h.Key] }, reqs)
	check("type:requirement has:scenario", func(h Hit) bool { return wantScn[h.Key] }, reqs)
	check("type:requirement -has:scenario", func(h Hit) bool { return !wantScn[h.Key] }, reqs)
	if wantImpl["BRD-015"] {
		t.Fatal("test premise: BRD-015 has no explicit design")
	}
}

func TestStaleMarker(t *testing.T) {
	root, st := indexedCorpus(t)
	for _, h := range run(t, st, root, "BRD-004", 10) {
		if h.Stale {
			t.Fatalf("fresh index but %s is stale", h.Key)
		}
	}
	p := filepath.Join(root, "docs", "brd.md")
	b, _ := os.ReadFile(p)
	if err := os.WriteFile(p, append(b, []byte("\nextra line\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	os.Chtimes(p, later, later)
	var sawBRD bool
	for _, h := range run(t, st, root, "BRD-004", 10) {
		if h.Path == "docs/brd.md" {
			sawBRD = true
			if !h.Stale {
				t.Errorf("%s should be stale", h.Key)
			}
		} else {
			if h.Stale {
				t.Errorf("%s should not be stale", h.Key)
			}
		}
	}
	if !sawBRD {
		t.Fatal("no hit from brd.md")
	}
	// missing file is stale
	os.Remove(filepath.Join(root, "docs", "brd.md"))
	for _, h := range run(t, st, root, "BRD-004", 10) {
		if h.Path == "docs/brd.md" && !h.Stale {
			t.Errorf("missing file: %s should be stale", h.Key)
		}
	}
	// same content, new mtime: not stale (sha256 fallback)
	root2, st2 := indexedCorpus(t)
	p2 := filepath.Join(root2, "docs", "brd.md")
	os.Chtimes(p2, later, later)
	for _, h := range run(t, st2, root2, "BRD-004", 10) {
		if h.Stale {
			t.Errorf("touched but identical: %s reported stale", h.Key)
		}
	}
	// empty Root: staleness is not checked
	q, _ := ParseQuery("BRD-004")
	hits, err := Search(context.Background(), st, q, Options{Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Stale {
			t.Errorf("no root given but %s stale", h.Key)
		}
	}
}

func TestFTSSyntaxErrorIsAUserError(t *testing.T) {
	_, st := indexedCorpus(t)
	// ParseQuery rejects an unbalanced quote
	if _, err := ParseQuery(`upload "unbalanced`); err == nil || !strings.Contains(err.Error(), `upload "unbalanced`) {
		t.Fatalf("parse error = %v", err)
	}
	// a Query built by hand with bad FTS text must not panic or leak a bare SQLite message
	for _, text := range []string{`"unbalanced`, "AND", "fax AND", "(fax"} {
		_, err := Search(context.Background(), st, Query{Text: text}, Options{Mode: "fts"})
		if err == nil {
			t.Errorf("%q: want error", text)
			continue
		}
		if !strings.Contains(err.Error(), text) || !strings.Contains(err.Error(), "invalid search query") {
			t.Errorf("%q: error %q must name the query", text, err)
		}
	}
}

func TestModes(t *testing.T) {
	root, st := indexedCorpus(t)
	q, _ := ParseQuery("fax")
	for _, m := range []string{"vector", "hybrid"} {
		_, err := Search(context.Background(), st, q, Options{Mode: m, Root: root})
		if err == nil || !strings.Contains(err.Error(), "no embedder") {
			t.Errorf("%s: error = %v", m, err)
		}
	}
	if _, err := Search(context.Background(), st, q, Options{Mode: "bogus"}); err == nil {
		t.Error("unknown mode: want error")
	}
	q, _ = ParseQuery("BRD-004 resume")
	hits, err := Search(context.Background(), st, q, Options{Mode: "exact"})
	if err != nil || len(hits) != 1 || hits[0].Key != "BRD-004" || hits[0].Source != "exact" {
		t.Errorf("exact: %v %v", keys(hits), err)
	}
	hits, err = Search(context.Background(), st, q, Options{Mode: "fts"})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Source != "fts" {
			t.Errorf("fts mode returned source %s", h.Source)
		}
	}
	// default K is 10
	all := run(t, st, root, "document", 0)
	if len(all) > 10 {
		t.Errorf("default K: got %d", len(all))
	}
}

// embedded embeds every chunk of st with a fake server and returns the
// embedder (model names the model, so two calls can differ).
func embedded(t *testing.T, st *store.Store, model string) (embed.Embedder, *embedtest.Server) {
	t.Helper()
	srv := embedtest.New(768, "embeddinggemma", "other-model")
	t.Cleanup(srv.Close)
	e := newEmbedder(t, srv, model)
	if _, err := embed.Pending(context.Background(), st, e, vector.NewVec1(st.DB())); err != nil {
		t.Fatal(err)
	}
	return e, srv
}

func newEmbedder(t *testing.T, srv *embedtest.Server, model string) embed.Embedder {
	t.Helper()
	e, err := embed.New(config.Config{Provider: config.Providers{Embed: "ollama"}, Model: model, BaseURL: srv.URL, Dims: 768}, false)
	if err != nil || e == nil {
		t.Fatalf("embed.New: %v %v", e, err)
	}
	return e
}

func searchWith(t *testing.T, st *store.Store, e embed.Embedder, query, mode string, k int) ([]Hit, error) {
	t.Helper()
	q, err := ParseQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	return Search(context.Background(), st, q, Options{Mode: mode, K: k, Embedder: e})
}

func TestNoEmbedderAutoIsFTS(t *testing.T) {
	_, st := indexedCorpus(t)
	auto, err := searchWith(t, st, nil, "bulk upload", "auto", 10)
	if err != nil {
		t.Fatal(err)
	}
	fts, err := searchWith(t, st, nil, "bulk upload", "fts", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(auto) == 0 || !reflect.DeepEqual(keys(auto), keys(fts)) {
		t.Fatalf("auto = %v, fts = %v", keys(auto), keys(fts))
	}
	for _, h := range auto {
		if h.Source != "fts" {
			t.Errorf("%s has source %s without an embedder", h.Key, h.Source)
		}
	}
	for _, m := range []string{"vector", "hybrid"} {
		if _, err := searchWith(t, st, nil, "bulk upload", m, 10); err == nil || !strings.Contains(err.Error(), "no embedder") {
			t.Errorf("%s without an embedder: %v", m, err)
		}
	}
}

func TestHybridKeepsExactFirst(t *testing.T) {
	_, st := indexedCorpus(t)
	e, _ := embedded(t, st, "embeddinggemma")
	// the words of the query are those of BRD-004, but the ID asked for is BRD-013
	for _, mode := range []string{"hybrid", "vector", "auto"} {
		hits, err := searchWith(t, st, e, "BRD-013 upload resume interrupted", mode, 5)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if len(hits) == 0 || hits[0].Key != "BRD-013" || hits[0].Source != "exact" {
			t.Fatalf("%s: first hit = %v, want BRD-013 (exact): %v", mode, hits, keys(hits))
		}
		seen := map[string]bool{}
		for _, h := range hits {
			if seen[h.Key] {
				t.Errorf("%s: %s listed twice", mode, h.Key)
			}
			seen[h.Key] = true
		}
		if len(hits) < 2 {
			t.Errorf("%s: only %d hit(s); the vector side should add more", mode, len(hits))
		}
	}
}

func TestHybridIncludesVectorOnlyHits(t *testing.T) {
	_, st := indexedCorpus(t)
	e, _ := embedded(t, st, "embeddinggemma")
	// "banana" is in no document, so full text (implicit AND) finds nothing
	fts, err := searchWith(t, st, e, "resume interrupted bulk upload banana", "fts", 5)
	if err != nil || len(fts) != 0 {
		t.Fatalf("fts = %v, %v; want none", keys(fts), err)
	}
	hits, err := searchWith(t, st, e, "resume interrupted bulk upload banana", "hybrid", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Source != "vector" {
		t.Fatalf("hybrid = %v; want vector hits", keys(hits))
	}
	if !contains(keys(hits), "BRD-004") {
		t.Errorf("hybrid %v lacks BRD-004 (An interrupted bulk upload can be resumed)", keys(hits))
	}
	// auto with an embedder is hybrid
	auto, err := searchWith(t, st, e, "resume interrupted bulk upload banana", "auto", 5)
	if err != nil || !reflect.DeepEqual(keys(auto), keys(hits)) {
		t.Errorf("auto = %v, %v; want %v", keys(auto), err, keys(hits))
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestFilteredVectorSearch(t *testing.T) {
	_, st := indexedCorpus(t)
	e, _ := embedded(t, st, "embeddinggemma")
	cases := []struct {
		query string
		check func(h Hit) bool
	}{
		{"upload files banana type:design", func(h Hit) bool { return h.Type == "design" }},
		{"upload files banana status:draft", func(h Hit) bool { return strings.EqualFold(h.Status, "draft") }},
		{"upload files banana type:requirement -status:approved", func(h Hit) bool { return h.Type == "requirement" && !strings.EqualFold(h.Status, "approved") }},
		{"upload files banana tag:wip", func(h Hit) bool { return h.Type == "scenario" || h.Type == "feature" }},
	}
	for _, c := range cases {
		for _, mode := range []string{"vector", "hybrid"} {
			hits, err := searchWith(t, st, e, c.query, mode, 5)
			if err != nil {
				t.Fatalf("%s %q: %v", mode, c.query, err)
			}
			if len(hits) == 0 {
				t.Errorf("%s %q: no hits", mode, c.query)
			}
			for _, h := range hits {
				if !c.check(h) {
					t.Errorf("%s %q: %s (%s/%s) violates the filter", mode, c.query, h.Key, h.Type, h.Status)
				}
			}
		}
	}
	hits, err := searchWith(t, st, e, `upload banana -"resume"`, "vector", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		var text string
		st.DB().QueryRow(`SELECT c.text FROM chunks c JOIN entities x ON x.id=c.entity_id WHERE x.key=?`, h.Key).Scan(&text)
		if strings.Contains(strings.ToLower(text), "resume") {
			t.Errorf("excluded phrase present in %s", h.Key)
		}
	}
}

func TestFingerprintMismatchRefusesVectorSearch(t *testing.T) {
	_, st := indexedCorpus(t)
	_, srv := embedded(t, st, "embeddinggemma")
	other := newEmbedder(t, srv, "other-model")
	for _, mode := range []string{"vector", "hybrid"} {
		_, err := searchWith(t, st, other, "bulk upload", mode, 5)
		if !errors.Is(err, embed.ErrFingerprintMismatch) {
			t.Fatalf("%s: err = %v, want ErrFingerprintMismatch", mode, err)
		}
		for _, want := range []string{"index --reembed", "--mode fts"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error does not name `%s`: %v", mode, want, err)
			}
		}
	}
	// full text and exact search keep working
	for _, mode := range []string{"fts", "exact"} {
		q := "bulk upload"
		if mode == "exact" {
			q = "BRD-003"
		}
		hits, err := searchWith(t, st, other, q, mode, 5)
		if err != nil || len(hits) == 0 {
			t.Errorf("%s with a mismatching embedder: %v, %v", mode, keys(hits), err)
		}
	}
}

func TestAutoFallsBackToFTS(t *testing.T) {
	// an index with no embeddings yet
	_, st := indexedCorpus(t)
	srv := embedtest.New(768, "embeddinggemma")
	defer srv.Close()
	e := newEmbedder(t, srv, "embeddinggemma")
	hits, err := searchWith(t, st, e, "bulk upload", "auto", 5)
	if err != nil || len(hits) == 0 || srv.Requests.Load() != 0 {
		t.Fatalf("auto on an un-embedded index: %v, %v (requests %d)", keys(hits), err, srv.Requests.Load())
	}
	if _, err := searchWith(t, st, e, "bulk upload", "hybrid", 5); !errors.Is(err, embed.ErrNotEmbedded) {
		t.Errorf("hybrid on an un-embedded index: %v", err)
	}
	// an embedder that is down
	e2, srv2 := embedded(t, st, "embeddinggemma")
	srv2.Down.Store(true)
	hits, err = searchWith(t, st, e2, "bulk upload", "auto", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("auto with the embedder down: %v, %v", keys(hits), err)
	}
	if _, err := searchWith(t, st, e2, "bulk upload", "hybrid", 5); err == nil {
		t.Error("hybrid with the embedder down: want an error")
	}
}

func TestFuseReciprocalRank(t *testing.T) {
	f := func(keys ...string) []found {
		var out []found
		for _, k := range keys {
			out = append(out, found{Hit: Hit{Key: k}})
		}
		return out
	}
	got := fuse(f("a", "b", "c"), f("c", "a"))
	var order []string
	for _, h := range got {
		order = append(order, h.Key)
	}
	if !reflect.DeepEqual(order, []string{"a", "c", "b"}) {
		t.Fatalf("order = %v, want [a c b]", order)
	}
	want := 1.0/61 + 1.0/62
	if d := got[0].Score - want; d > 1e-12 || d < -1e-12 {
		t.Errorf("score of a = %v, want 1/(60+1) + 1/(60+2) = %v", got[0].Score, want)
	}
}

func TestPlainText(t *testing.T) {
	q, _ := ParseQuery(`resume "legal hold" OR upload -tag:wip -"dark mode" BRD-004`)
	if got := plainText(q); got != "resume legal hold upload" {
		t.Errorf("plainText = %q", got)
	}
}

func TestAutoDegradesOnFingerprintMismatch(t *testing.T) {
	_, st := indexedCorpus(t)
	_, srv := embedded(t, st, "embeddinggemma")
	other := newEmbedder(t, srv, "other-model")
	before := srv.Requests.Load()
	q, _ := ParseQuery("BRD-013 upload")
	var notice string
	hits, err := Search(context.Background(), st, q, Options{Mode: "auto", K: 10, Embedder: other, Notice: &notice})
	if err != nil {
		t.Fatalf("auto under a mismatch: %v", err)
	}
	if len(hits) < 2 || hits[0].Key != "BRD-013" || hits[0].Source != "exact" {
		t.Fatalf("hits = %v, want BRD-013 (exact) first then full text", keys(hits))
	}
	for _, h := range hits[1:] {
		if h.Source != "fts" {
			t.Errorf("%s has source %s; no vector hit may be mixed in", h.Key, h.Source)
		}
	}
	if !strings.Contains(notice, "index --reembed") {
		t.Errorf("notice = %q, want it to name `index --reembed`", notice)
	}
	if srv.Requests.Load() != before {
		t.Error("the mismatching embedder was still asked to embed")
	}
	// no notice when nothing degraded, and a nil Notice is fine
	good := newEmbedder(t, srv, "embeddinggemma")
	notice = ""
	if _, err := Search(context.Background(), st, q, Options{Mode: "auto", Embedder: good, Notice: &notice}); err != nil || notice != "" {
		t.Errorf("healthy auto: err=%v notice=%q", err, notice)
	}
}
