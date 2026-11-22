package coverage

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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

func itemKeys(items []Item) []string {
	out := []string{}
	for _, i := range items {
		out = append(out, i.Key)
	}
	sort.Strings(out)
	return out
}

func sorted(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

func same(t *testing.T, name string, got, want []string) {
	t.Helper()
	got, want = sorted(got), sorted(want)
	if len(got) != len(want) {
		t.Errorf("%s: got %v want %v", name, got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s: got %v want %v", name, got, want)
			return
		}
	}
}

// truthUnlinked derives the scenarios with no incoming verified_by edge.
func truthUnlinked(t *testing.T) []string {
	tr, err := corpus.LoadTruth()
	if err != nil {
		t.Fatal(err)
	}
	linked := map[string]bool{}
	for _, e := range tr.Edges {
		if e.Kind == "verified_by" {
			linked[e.Dst] = true
		}
	}
	out := []string{}
	for _, e := range tr.Entities {
		if e.Type == "scenario" && !linked[e.Key] {
			out = append(out, e.Key)
		}
	}
	return out
}

func TestCoverageMatchesTruth(t *testing.T) {
	st := indexedCorpus(t)
	tr, _ := corpus.LoadTruth()
	rep, err := Run(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	same(t, "requirements without design", itemKeys(rep.RequirementsWithoutDesign), tr.Uncovered.RequirementsWithoutDesign)
	same(t, "designs without verification", itemKeys(rep.DesignsWithoutVerification), tr.Uncovered.DesignsWithoutScenario)
	same(t, "unlinked scenarios", itemKeys(rep.UnlinkedScenarios), truthUnlinked(t))
	if len(rep.UnlinkedScenarios) != 1 || rep.UnlinkedScenarios[0].Key != "features/access.feature#Warn before a session times out" {
		t.Errorf("unlinked = %+v", rep.UnlinkedScenarios)
	}
	for _, i := range rep.RequirementsWithoutDesign {
		if i.Type != "requirement" || i.Path != "docs/brd.md" || i.Line == 0 || i.Title == "" {
			t.Errorf("item %+v", i)
		}
	}

	var ids []string
	for _, d := range rep.Dangling {
		if d.Mention {
			t.Errorf("unexpected dangling mention %+v", d)
		}
		ids = append(ids, d.ID)
	}
	same(t, "dangling", ids, tr.Uncovered.Dangling)
	d := rep.Dangling[0]
	if d.Kind != "depends_on" || d.Referrer != "BRD-025" || d.Path != "docs/brd.md" || d.Line == 0 || d.Side != "target" {
		t.Errorf("dangling = %+v", d)
	}

	var dups []string
	for _, d := range rep.Duplicates {
		dups = append(dups, d.Key)
	}
	same(t, "duplicates", dups, tr.Uncovered.Duplicates)
	if rep.Duplicates[0].Path == "" || rep.Duplicates[0].Line == 0 || rep.Duplicates[0].Detail == "" {
		t.Errorf("duplicate = %+v", rep.Duplicates[0])
	}

	if len(rep.Conflicts) != 1 || rep.Conflicts[0].A != "BRD-013" || rep.Conflicts[0].B != "BRD-014" {
		t.Errorf("conflicts = %+v", rep.Conflicts)
	}
	if len(rep.Candidates) != 0 {
		t.Errorf("candidates = %+v", rep.Candidates)
	}
}

func TestConflictReportedOnceWhenBothDeclare(t *testing.T) {
	st := indexedCorpus(t)
	if _, err := st.DB().Exec(`INSERT INTO edges(src_key, dst_key, kind, explicit, origin) VALUES ('BRD-014','BRD-013','conflicts_with',1,'field')`); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v", rep.Conflicts)
	}
}

func TestMentionsFlaggedSeparately(t *testing.T) {
	st := indexedCorpus(t)
	if _, err := st.DB().Exec(`INSERT INTO edges(src_key, dst_key, kind, doc_id, line, explicit, origin) VALUES ('BRD-018','BRD-555','references',1,3,1,'mention')`); err != nil {
		t.Fatal(err)
	}
	rep, _ := Run(context.Background(), st)
	var m, f int
	for _, d := range rep.Dangling {
		if d.Mention {
			m++
			if d.ID != "BRD-555" || d.Kind != "references" {
				t.Errorf("mention %+v", d)
			}
		} else {
			f++
		}
	}
	if m != 1 || f != 1 {
		t.Errorf("mentions=%d others=%d", m, f)
	}
}

func TestInferredEdgesDoNotCover(t *testing.T) {
	st := indexedCorpus(t)
	before, _ := Run(context.Background(), st)
	for _, q := range []string{
		`INSERT INTO edges(src_key, dst_key, kind, explicit, confidence, origin) VALUES ('FDS-001','BRD-015','implements',0,0.9,'inferred')`,
		`INSERT INTO edges(src_key, dst_key, kind, explicit, confidence, origin) VALUES ('FDS-014','AC-001','verified_by',0,0.9,'inferred')`,
		`INSERT INTO edges(src_key, dst_key, kind, explicit, confidence, origin) VALUES ('BRD-001','features/access.feature#Warn before a session times out','verified_by',0,0.9,'inferred')`,
		`INSERT INTO edges(src_key, dst_key, kind, explicit, confidence, origin) VALUES ('BRD-001','BRD-404','depends_on',0,0.9,'inferred')`,
		`INSERT INTO edges(src_key, dst_key, kind, explicit, confidence, origin) VALUES ('BRD-001','BRD-002','conflicts_with',0,0.9,'inferred')`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	after, err := Run(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	same(t, "req", itemKeys(after.RequirementsWithoutDesign), itemKeys(before.RequirementsWithoutDesign))
	same(t, "design", itemKeys(after.DesignsWithoutVerification), itemKeys(before.DesignsWithoutVerification))
	same(t, "unlinked", itemKeys(after.UnlinkedScenarios), itemKeys(before.UnlinkedScenarios))
	if len(after.Dangling) != len(before.Dangling) || len(after.Conflicts) != len(before.Conflicts) {
		t.Errorf("dangling/conflicts changed: %+v %+v", after.Dangling, after.Conflicts)
	}
}
