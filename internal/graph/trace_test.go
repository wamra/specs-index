package graph

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

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

func indexedCorpus(t *testing.T) *store.Store {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
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
	return st
}

// emptyStore returns a store holding only the entities and edges the test inserts.
func emptyStore(t *testing.T, keys []string, edges [][3]string) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	if _, err := db.Exec(`INSERT INTO documents(id, path, type) VALUES (1, 'x.md', 'requirements')`); err != nil {
		t.Fatal(err)
	}
	for i, k := range keys {
		if _, err := db.Exec(`INSERT INTO entities(doc_id, key, type, title, start_line) VALUES (1, ?, 'requirement', ?, ?)`, k, "t "+k, i+1); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range edges {
		if _, err := db.Exec(`INSERT INTO edges(src_key, dst_key, kind, doc_id, line, explicit, origin) VALUES (?, ?, ?, 1, 1, 1, 'field')`, e[0], e[1], e[2]); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func collect(n *Node, into map[string]bool) {
	into[n.Key] = true
	for _, c := range n.Children {
		collect(c, into)
	}
}

func depthOf(n *Node) int {
	d := 0
	for _, c := range n.Children {
		if x := 1 + depthOf(c); x > d {
			d = x
		}
	}
	return d
}

func keySet(n *Node) []string {
	m := map[string]bool{}
	collect(n, m)
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// truthReach computes the reachable key set from truth edges with the default
// kinds. down: parent_of fwd, implements reversed, verified_by fwd; up: reverse.
func truthReach(t *testing.T, root string, down bool) []string {
	tr, err := corpus.LoadTruth()
	if err != nil {
		t.Fatal(err)
	}
	adj := map[string][]string{}
	for _, e := range tr.Edges {
		fwd := e.Kind == "parent_of" || e.Kind == "verified_by"
		rev := e.Kind == "implements"
		if !fwd && !rev {
			continue
		}
		from, to := e.Src, e.Dst
		if rev {
			from, to = to, from
		}
		if !down {
			from, to = to, from
		}
		adj[from] = append(adj[from], to)
	}
	seen := map[string]bool{root: true}
	q := []string{root}
	for len(q) > 0 {
		k := q[0]
		q = q[1:]
		for _, n := range adj[k] {
			if !seen[n] {
				seen[n] = true
				q = append(q, n)
			}
		}
	}
	var out []string
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func eq(a, b []string) bool { return strings.Join(a, "\n") == strings.Join(b, "\n") }

func TestTraceDownMatchesTruth(t *testing.T) {
	st := indexedCorpus(t)
	for _, root := range []string{"BRD-001", "BRD-010", "BRD-013", "BRD-022", "FDS-005", "FDS-014", "AC-001", "BRD-015"} {
		n, err := Trace(context.Background(), st, root, TraceOpts{Direction: "down", Depth: 50})
		if err != nil {
			t.Fatalf("%s: %v", root, err)
		}
		got, want := keySet(n), truthReach(t, root, true)
		if !eq(got, want) {
			t.Errorf("down %s:\n got %v\nwant %v", root, got, want)
		}
	}
}

func TestTraceUpMatchesTruth(t *testing.T) {
	st := indexedCorpus(t)
	tr, _ := corpus.LoadTruth()
	roots := []string{"BRD-004", "BRD-022", "FDS-005", "AC-001"}
	for _, e := range tr.Entities {
		if e.Type == "scenario" && len(roots) < 8 {
			roots = append(roots, e.Key)
		}
	}
	for _, root := range roots {
		n, err := Trace(context.Background(), st, root, TraceOpts{Direction: "up", Depth: 50})
		if err != nil {
			t.Fatalf("%s: %v", root, err)
		}
		got, want := keySet(n), truthReach(t, root, false)
		if !eq(got, want) {
			t.Errorf("up %s:\n got %v\nwant %v", root, got, want)
		}
	}
}

func TestBothMergesAndMarksVia(t *testing.T) {
	st := indexedCorpus(t)
	n, err := Trace(context.Background(), st, "BRD-005", TraceOpts{Direction: "both", Kinds: []string{"parent_of"}})
	if err != nil {
		t.Fatal(err)
	}
	via := map[string]string{}
	for _, c := range n.Children {
		via[c.Key] = c.Via
	}
	if via["BRD-006"] != "parent_of forward" || via["BRD-001"] != "parent_of reversed" {
		t.Fatalf("via = %v", via)
	}
}

func TestManyToMany(t *testing.T) {
	st := indexedCorpus(t)
	ctx := context.Background()
	up, err := Trace(ctx, st, "FDS-005", TraceOpts{Direction: "up", Depth: 1, Kinds: []string{"implements"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(up.Children) != 3 {
		t.Fatalf("FDS-005 implements %d BRDs, want 3", len(up.Children))
	}
	for _, c := range up.Children {
		if c.Via != "implements forward" {
			t.Errorf("via %q", c.Via)
		}
	}
	down, err := Trace(ctx, st, "BRD-022", TraceOpts{Direction: "down", Depth: 1, Kinds: []string{"implements"}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range down.Children {
		got = append(got, c.Key)
		if c.Via != "implements reversed" || !c.Explicit || c.Type != "design" || c.Path == "" || c.Line == 0 {
			t.Errorf("child %+v", c)
		}
	}
	if !eq(got, []string{"FDS-013", "FDS-014"}) {
		t.Fatalf("BRD-022 designs = %v", got)
	}
}

func TestDepthLimit(t *testing.T) {
	st := indexedCorpus(t)
	ctx := context.Background()
	kinds := []string{"parent_of"}
	n, _ := Trace(ctx, st, "BRD-001", TraceOpts{Depth: 2, Kinds: kinds})
	if d := depthOf(n); d != 2 {
		t.Errorf("depth 2: tree depth %d", d)
	}
	m := map[string]bool{}
	collect(n, m)
	if !m["BRD-003"] || m["BRD-004"] {
		t.Errorf("depth 2 reach wrong: 003=%v 004=%v", m["BRD-003"], m["BRD-004"])
	}
	n, _ = Trace(ctx, st, "BRD-001", TraceOpts{Kinds: kinds}) // default 5
	m = map[string]bool{}
	collect(n, m)
	if !m["BRD-004"] {
		t.Error("default depth should reach BRD-004")
	}
	n, _ = Trace(ctx, st, "BRD-002", TraceOpts{Depth: 1, Kinds: kinds})
	if depthOf(n) != 1 {
		t.Error("depth 1")
	}
}

func TestCycleTerminates(t *testing.T) {
	st := emptyStore(t, []string{"A-1", "B-1", "C-1"}, [][3]string{
		{"A-1", "B-1", "parent_of"}, {"B-1", "C-1", "parent_of"}, {"C-1", "A-1", "parent_of"}, {"C-1", "C-1", "parent_of"},
	})
	for _, dir := range []string{"down", "up", "both"} {
		n, err := Trace(context.Background(), st, "A-1", TraceOpts{Direction: dir, Depth: 50})
		if err != nil {
			t.Fatal(err)
		}
		var repeats, total int
		var walk func(*Node)
		walk = func(x *Node) {
			total++
			if x.Repeat {
				repeats++
				if len(x.Children) != 0 {
					t.Errorf("%s: repeat node %s expanded", dir, x.Key)
				}
			}
			for _, c := range x.Children {
				walk(c)
			}
		}
		walk(n)
		if repeats == 0 || total > 20 {
			t.Errorf("%s: repeats=%d total=%d", dir, repeats, total)
		}
	}
	n, _ := Trace(context.Background(), st, "A-1", TraceOpts{Direction: "down"})
	c := n.Children[0].Children[0].Children // A -> B -> C -> {A repeat, C repeat}
	if len(c) != 2 || !c[0].Repeat || c[0].Key != "A-1" || !c[1].Repeat || c[1].Key != "C-1" {
		t.Errorf("cycle children = %+v", c)
	}
}

func TestUnknownKey(t *testing.T) {
	st := indexedCorpus(t)
	_, err := Trace(context.Background(), st, "BRD-0011", TraceOpts{})
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "BRD-001") || !strings.Contains(err.Error(), "BRD-0011") {
		t.Errorf("error lacks near matches: %v", err)
	}
	_, err = Trace(context.Background(), st, "zzzzzz-1", TraceOpts{})
	if err == nil || strings.Contains(err.Error(), "BRD") {
		t.Errorf("unrelated key: %v", err)
	}
	_, err = Trace(context.Background(), st, "BRD", TraceOpts{})
	if err == nil || !strings.Contains(err.Error(), "BRD-001") {
		t.Errorf("prefix key: %v", err)
	}
}

func TestDanglingTargetShownAsMissing(t *testing.T) {
	st := indexedCorpus(t)
	n, err := Trace(context.Background(), st, "BRD-025", TraceOpts{Direction: "down", Kinds: []string{"depends_on"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Children) != 1 {
		t.Fatalf("children = %+v", n.Children)
	}
	c := n.Children[0]
	if c.Key != "BRD-099" || !c.Missing || c.Via != "depends_on forward" || c.Type != "" {
		t.Errorf("child = %+v", c)
	}
}

func TestReferencesOnlyWhenNamed(t *testing.T) {
	tr, _ := corpus.LoadTruth()
	var ref struct{ src, dst string }
	for _, e := range tr.Edges {
		if e.Kind == "references" {
			ref.src, ref.dst = e.Src, e.Dst
			break
		}
	}
	st := indexedCorpus(t)
	has := func(o TraceOpts) bool {
		o.Direction = "down"
		o.Depth = 1
		n, err := Trace(context.Background(), st, ref.src, o)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range n.Children {
			if c.Key == ref.dst {
				return true
			}
		}
		return false
	}
	if has(TraceOpts{}) {
		t.Error("references followed by default")
	}
	if !has(TraceOpts{Kinds: []string{"references"}}) {
		t.Error("references not followed when named")
	}
}

func TestInferredEdgesIgnoredUnlessRequested(t *testing.T) {
	st := indexedCorpus(t)
	if _, err := st.DB().Exec(`INSERT INTO edges(src_key, dst_key, kind, explicit, confidence, origin) VALUES ('FDS-001','BRD-015','implements',0,0.9,'inferred')`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	n, _ := Trace(ctx, st, "BRD-015", TraceOpts{Direction: "down", Depth: 1, Kinds: []string{"implements"}})
	if len(n.Children) != 0 {
		t.Fatalf("inferred edge followed: %+v", n.Children)
	}
	n, _ = Trace(ctx, st, "BRD-015", TraceOpts{Direction: "down", Depth: 1, Kinds: []string{"implements"}, IncludeInferred: true})
	if len(n.Children) != 1 || n.Children[0].Key != "FDS-001" || n.Children[0].Explicit {
		t.Fatalf("children = %+v", n.Children)
	}
	if !n.Explicit {
		t.Error("root is explicit")
	}
}
