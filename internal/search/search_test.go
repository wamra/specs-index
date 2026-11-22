package search

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wamra/specs-index/internal/corpus"
	"github.com/wamra/specs-index/internal/indexer"
	"github.com/wamra/specs-index/internal/store"
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
	rep, err := indexer.Run(context.Background(), st, root)
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
