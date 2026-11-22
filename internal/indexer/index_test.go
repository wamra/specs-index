package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/wamra/specs-index/internal/corpus"
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

func evalDir(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// newWorkspace returns a temp root with the corpus copied in, an open store
// and the root registered as a source.
func newWorkspace(t *testing.T) (string, *store.Store) {
	t.Helper()
	root := evalDir(t, t.TempDir())
	copyDir(t, corpus.Dir(), root)
	return root, openStore(t, root)
}

func openStore(t *testing.T, root string) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(evalDir(t, t.TempDir()), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func runIdx(t *testing.T, st *store.Store, root string) Report {
	t.Helper()
	rep, err := Run(context.Background(), st, root)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func count(t *testing.T, st *store.Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIndexCorpusMatchesTruth(t *testing.T) {
	root, st := newWorkspace(t)
	if err := AddSource(st, root, "docs"); err != nil {
		t.Fatal(err)
	}
	if err := AddSource(st, root, "features"); err != nil {
		t.Fatal(err)
	}
	rep := runIdx(t, st, root)
	if rep.Failed != 0 || len(rep.Failures) != 0 {
		t.Fatalf("failures: %+v", rep.Failures)
	}
	truth, err := corpus.LoadTruth()
	if err != nil {
		t.Fatal(err)
	}
	wantPath := map[string]string{}
	for _, e := range truth.Entities {
		if p, ok := wantPath[e.Key]; !ok || e.Path < p {
			wantPath[e.Key] = e.Path
		}
	}
	rows, err := st.DB().Query(`SELECT e.key, d.path FROM entities e JOIN documents d ON d.id = e.doc_id`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for rows.Next() {
		var k, p string
		rows.Scan(&k, &p)
		got[k] = p
	}
	rows.Close()
	if len(got) != len(wantPath) {
		t.Errorf("entities: got %d, want %d", len(got), len(wantPath))
	}
	for k, p := range wantPath {
		if got[k] != p {
			t.Errorf("entity %s: owner %q, want %q", k, got[k], p)
		}
	}
	if got["AC-010"] != "docs/acceptance.md" {
		t.Errorf("AC-010 owner = %q", got["AC-010"])
	}
	if rep.Entities != len(wantPath) {
		t.Errorf("Report.Entities = %d, want %d", rep.Entities, len(wantPath))
	}

	wantEdges := map[string]bool{}
	for _, e := range truth.Edges {
		wantEdges[e.Src+"|"+e.Dst+"|"+e.Kind] = true
	}
	er, err := st.DB().Query(`SELECT src_key, dst_key, kind FROM edges WHERE explicit = 1`)
	if err != nil {
		t.Fatal(err)
	}
	gotEdges := map[string]bool{}
	for er.Next() {
		var a, b, c string
		er.Scan(&a, &b, &c)
		gotEdges[a+"|"+b+"|"+c] = true
	}
	er.Close()
	for k := range wantEdges {
		if !gotEdges[k] {
			t.Errorf("missing edge %s", k)
		}
	}
	for k := range gotEdges {
		if !wantEdges[k] {
			t.Errorf("unexpected edge %s", k)
		}
	}
	// one chunk per entity, FTS rows present, scenarios carry steps
	if c, e := count(t, st, `SELECT count(*) FROM chunks`), len(got); c != e {
		t.Errorf("chunks = %d, want %d", c, e)
	}
	if n := count(t, st, `SELECT count(*) FROM chunk_fts`); n != len(got) {
		t.Errorf("chunk_fts rows = %d", n)
	}
	if n := count(t, st, `SELECT count(*) FROM id_fts WHERE id_fts MATCH '"BRD-001"'`); n < 1 {
		t.Errorf("id_fts has no BRD-001")
	}
	if n := count(t, st, `SELECT count(*) FROM steps`); n == 0 {
		t.Errorf("no steps stored")
	}
	if v, _ := st.Meta("indexed_at"); v == "" {
		t.Errorf("meta.indexed_at not set")
	}
	if n := count(t, st, `SELECT count(*) FROM chunks WHERE embed_fingerprint IS NOT NULL AND embed_fingerprint <> ''`); n != 0 {
		t.Errorf("embed_fingerprint should be empty")
	}
}

func TestSecondRunParsesNothing(t *testing.T) {
	root, st := newWorkspace(t)
	AddSource(st, root, "docs")
	AddSource(st, root, "features")
	first := runIdx(t, st, root)
	if first.Parsed != 11 || first.Scanned != 11 {
		t.Fatalf("first: %+v", first)
	}
	second := runIdx(t, st, root)
	if second.Parsed != 0 || second.Unchanged != 11 || second.Removed != 0 || second.Failed != 0 {
		t.Fatalf("second: %+v", second)
	}
	if second.Entities != first.Entities || second.Edges != first.Edges {
		t.Fatalf("totals changed: %+v vs %+v", first, second)
	}
}

func TestChangedFileReplacesOnlyItsRows(t *testing.T) {
	root, st := newWorkspace(t)
	AddSource(st, root, "docs")
	AddSource(st, root, "features")
	runIdx(t, st, root)

	type chunk struct {
		id  int
		sha string
	}
	snap := func() map[string]chunk {
		m := map[string]chunk{}
		rows, err := st.DB().Query(`SELECT e.key, c.id, c.text_sha256 FROM entities e JOIN chunks c ON c.entity_id = e.id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			var c chunk
			rows.Scan(&k, &c.id, &c.sha)
			m[k] = c
		}
		return m
	}
	before := snap()
	p := filepath.Join(root, "docs", "brd.md")
	b, _ := os.ReadFile(p)
	edited := strings.Replace(string(b), "Document upload", "Document upload and intake", 1)
	if edited == string(b) {
		t.Fatal("test edit did not apply")
	}
	write(t, p, edited)
	rep := runIdx(t, st, root)
	if rep.Parsed != 1 || rep.Unchanged != 10 {
		t.Fatalf("report: %+v", rep)
	}
	after := snap()
	if len(after) != len(before) {
		t.Fatalf("entity count %d -> %d", len(before), len(after))
	}
	changed := 0
	for k, a := range after {
		b := before[k]
		if a != b {
			changed++
			if k != "BRD-002" {
				t.Errorf("%s changed unexpectedly: %v -> %v", k, b, a)
			}
		}
	}
	if changed != 1 {
		t.Errorf("changed chunks = %d, want 1 (BRD-002)", changed)
	}
	var title string
	st.DB().QueryRow(`SELECT title FROM entities WHERE key='BRD-002'`).Scan(&title)
	if title != "Document upload and intake" {
		t.Errorf("title = %q", title)
	}
}

func TestDeletedFileRemovedFromIndex(t *testing.T) {
	root, st := newWorkspace(t)
	AddSource(st, root, "features")
	runIdx(t, st, root)
	rel := "features/upload.feature"
	if count(t, st, `SELECT count(*) FROM documents WHERE path=?`, rel) != 1 {
		t.Fatal("document not indexed")
	}
	if err := os.Remove(filepath.Join(root, rel)); err != nil {
		t.Fatal(err)
	}
	rep := runIdx(t, st, root)
	if rep.Removed != 1 {
		t.Fatalf("report: %+v", rep)
	}
	assertNoRowsFor(t, st, rel)
}

func assertNoRowsFor(t *testing.T, st *store.Store, rel string) {
	t.Helper()
	q := func(label, sqlText string) {
		if n := count(t, st, sqlText, rel); n != 0 {
			t.Errorf("%s: %d leftover rows for %s", label, n, rel)
		}
	}
	q("documents", `SELECT count(*) FROM documents WHERE path=?`)
	if n := count(t, st, `SELECT count(*) FROM entities WHERE doc_id NOT IN (SELECT id FROM documents)`); n != 0 {
		t.Errorf("orphan entities: %d", n)
	}
	if n := count(t, st, `SELECT count(*) FROM steps WHERE entity_id NOT IN (SELECT id FROM entities)`); n != 0 {
		t.Errorf("orphan steps: %d", n)
	}
	if n := count(t, st, `SELECT count(*) FROM chunks WHERE entity_id NOT IN (SELECT id FROM entities)`); n != 0 {
		t.Errorf("orphan chunks: %d", n)
	}
	if n := count(t, st, `SELECT count(*) FROM edges WHERE doc_id NOT IN (SELECT id FROM documents)`); n != 0 {
		t.Errorf("orphan edges: %d", n)
	}
	if n := count(t, st, `SELECT count(*) FROM findings WHERE doc_id NOT IN (SELECT id FROM documents)`); n != 0 {
		t.Errorf("orphan findings: %d", n)
	}
	e := count(t, st, `SELECT count(*) FROM entities`)
	if n := count(t, st, `SELECT count(*) FROM chunk_fts`); n != e {
		t.Errorf("chunk_fts rows %d, entities %d", n, e)
	}
	if n := count(t, st, `SELECT count(*) FROM id_fts`); n != e {
		t.Errorf("id_fts rows %d, entities %d", n, e)
	}
	if n := count(t, st, `SELECT count(*) FROM chunks`); n != e {
		t.Errorf("chunks %d, entities %d", n, e)
	}
}

func TestRemoveSourceRemovesItsDocuments(t *testing.T) {
	root, st := newWorkspace(t)
	AddSource(st, root, "docs")
	AddSource(st, root, "features")
	runIdx(t, st, root)
	if err := RemoveSource(st, root, "features"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT count(*) FROM documents WHERE path LIKE 'features/%'`); n != 0 {
		t.Fatalf("feature documents remain: %d", n)
	}
	if n := count(t, st, `SELECT count(*) FROM documents WHERE path LIKE 'docs/%'`); n != 3 {
		t.Fatalf("docs documents = %d, want 3", n)
	}
	assertNoRowsFor(t, st, "features/")
	srcs, err := ListSources(st)
	if err != nil || len(srcs) != 1 || srcs[0].Path != "docs" {
		t.Fatalf("sources = %+v, %v", srcs, err)
	}
	// removing the file's source when still covered by a directory source keeps the file
	AddSource(st, root, "docs/brd.md")
	RemoveSource(st, root, "docs/brd.md")
	if n := count(t, st, `SELECT count(*) FROM documents WHERE path='docs/brd.md'`); n != 1 {
		t.Fatalf("brd.md dropped though docs still covers it")
	}
}

func TestAddRefusesOutsideRoot(t *testing.T) {
	root := evalDir(t, t.TempDir())
	write(t, filepath.Join(root, "ok.md"), "## BRD-001 X\n")
	outside := evalDir(t, t.TempDir())
	write(t, filepath.Join(outside, "o.md"), "## BRD-002 Y\n")
	st := openStore(t, root)
	cases := map[string]string{"dotdot": "..", "dotdot file": "../" + filepath.Base(outside), "absolute": outside}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		cases["symlink"] = "link"
	}
	for name, p := range cases {
		if err := AddSource(st, root, p); err == nil {
			t.Errorf("%s: AddSource(%q) accepted", name, p)
		}
	}
	if srcs, _ := ListSources(st); len(srcs) != 0 {
		t.Errorf("sources stored: %+v", srcs)
	}
	if err := AddSource(st, root, "missing.md"); err == nil {
		t.Errorf("missing path accepted")
	}
	if err := AddSource(st, root, "ok.md"); err != nil {
		t.Fatal(err)
	}
	if err := AddSource(st, root, filepath.Join(root, "ok.md")); err != nil {
		t.Fatalf("re-adding same source should be idempotent: %v", err)
	}
	srcs, _ := ListSources(st)
	if len(srcs) != 1 || srcs[0].Path != "ok.md" || srcs[0].Kind != "file" {
		t.Fatalf("sources = %+v", srcs)
	}
}

func TestSymlinkLeavingRootIsSkippedAndCounted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	root := evalDir(t, t.TempDir())
	outside := evalDir(t, t.TempDir())
	write(t, filepath.Join(outside, "secret.md"), "## BRD-900 Secret\n")
	write(t, filepath.Join(root, "in.md"), "## BRD-001 In\n")
	os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "filelink.md"))
	os.Symlink(outside, filepath.Join(root, "dirlink"))
	st := openStore(t, root)
	if err := AddSource(st, root, "."); err != nil {
		t.Fatal(err)
	}
	rep := runIdx(t, st, root)
	if rep.Failed != 2 || len(rep.Failures) != 2 {
		t.Fatalf("report: %+v", rep)
	}
	if n := count(t, st, `SELECT count(*) FROM entities WHERE key='BRD-900'`); n != 0 {
		t.Fatalf("outside file was read")
	}
	if n := count(t, st, `SELECT count(*) FROM entities WHERE key='BRD-001'`); n != 1 {
		t.Fatalf("inside file missing")
	}
}

func TestUnreadableAndBinaryFilesSkipped(t *testing.T) {
	root := evalDir(t, t.TempDir())
	write(t, filepath.Join(root, "good.md"), "## BRD-001 Good\n")
	write(t, filepath.Join(root, "nul.md"), "## BRD-002 Nul\n\x00\x01")
	write(t, filepath.Join(root, "latin1.md"), "## BRD-003 caf\xe9\n")
	st := openStore(t, root)
	AddSource(st, root, ".")
	var unreadable bool
	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		write(t, filepath.Join(root, "locked.md"), "## BRD-004 Locked\n")
		if err := os.Chmod(filepath.Join(root, "locked.md"), 0); err != nil {
			t.Fatal(err)
		}
		unreadable = true
	}
	rep := runIdx(t, st, root)
	wantFailed := 2
	if unreadable {
		wantFailed = 3
	}
	if rep.Failed != wantFailed || len(rep.Failures) != wantFailed || rep.Parsed != 1 {
		t.Fatalf("report: %+v", rep)
	}
	if n := count(t, st, `SELECT count(*) FROM entities`); n != 1 {
		t.Fatalf("entities = %d, want 1", n)
	}
	var status, reason string
	if err := st.DB().QueryRow(`SELECT status, fail_reason FROM documents WHERE path='nul.md'`).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || reason == "" {
		t.Fatalf("status=%q reason=%q", status, reason)
	}
	// a failed file is retried and a fixed file replaces the failed row
	write(t, filepath.Join(root, "nul.md"), "## BRD-002 Fixed\n")
	rep = runIdx(t, st, root)
	if rep.Parsed != 1 || rep.Failed != wantFailed-1 {
		t.Fatalf("after fix: %+v", rep)
	}
	if n := count(t, st, `SELECT count(*) FROM entities WHERE key='BRD-002'`); n != 1 {
		t.Fatalf("fixed file not indexed")
	}
	// a previously good file that turns binary leaves the index
	write(t, filepath.Join(root, "good.md"), "\x00\x00")
	runIdx(t, st, root)
	if n := count(t, st, `SELECT count(*) FROM entities WHERE key='BRD-001'`); n != 0 {
		t.Fatalf("stale rows kept for failed file")
	}
	assertNoRowsFor(t, st, "absent.md")
}

func TestDuplicateIDAcrossFiles(t *testing.T) {
	root := evalDir(t, t.TempDir())
	write(t, filepath.Join(root, "a.md"), "## BRD-001 First owner\n\nBody a.\n")
	write(t, filepath.Join(root, "b.md"), "## BRD-001 Second\n\nBody b.\n\nSee BRD-002.\n")
	st := openStore(t, root)
	AddSource(st, root, ".")
	owner := func() string {
		var p sql.NullString
		st.DB().QueryRow(`SELECT d.path FROM entities e JOIN documents d ON d.id=e.doc_id WHERE e.key='BRD-001'`).Scan(&p)
		return p.String
	}
	nFind := func() int {
		return count(t, st, `SELECT count(*) FROM findings WHERE kind='duplicate' AND key='BRD-001'`)
	}
	runIdx(t, st, root)
	if n := count(t, st, `SELECT count(*) FROM entities WHERE key='BRD-001'`); n != 1 || owner() != "a.md" {
		t.Fatalf("first run: entities=%d owner=%q", n, owner())
	}
	var docPath, detail string
	var line int
	if err := st.DB().QueryRow(`SELECT d.path, f.line, f.detail FROM findings f JOIN documents d ON d.id=f.doc_id WHERE f.kind='duplicate'`).Scan(&docPath, &line, &detail); err != nil {
		t.Fatalf("finding: %v", err)
	}
	if docPath != "b.md" || line != 1 || !strings.Contains(detail, "a.md") {
		t.Fatalf("finding = %q line %d %q", docPath, line, detail)
	}
	if n := count(t, st, `SELECT count(*) FROM edges WHERE src_key='BRD-001' AND dst_key='BRD-002'`); n != 1 {
		t.Fatalf("edge from duplicate document not stored: %d", n)
	}

	rep := runIdx(t, st, root)
	if rep.Parsed != 0 || nFind() != 1 {
		t.Fatalf("second run: parsed=%d findings=%d", rep.Parsed, nFind())
	}

	// deleting the owner promotes the other definition and clears the finding
	os.Remove(filepath.Join(root, "a.md"))
	runIdx(t, st, root)
	if owner() != "b.md" || nFind() != 0 {
		t.Fatalf("after delete: owner=%q findings=%d", owner(), nFind())
	}
	var title string
	st.DB().QueryRow(`SELECT title FROM entities WHERE key='BRD-001'`).Scan(&title)
	if title != "Second" {
		t.Fatalf("title = %q", title)
	}
	assertNoRowsFor(t, st, "a.md")

	// a new earlier file takes ownership from the later one
	write(t, filepath.Join(root, "0.md"), "## BRD-001 Zero\n")
	runIdx(t, st, root)
	if owner() != "0.md" || nFind() != 1 {
		t.Fatalf("after earlier add: owner=%q findings=%d", owner(), nFind())
	}
	// owner edited so it no longer defines the key: next definition is promoted
	write(t, filepath.Join(root, "0.md"), "## BRD-077 Other\n")
	runIdx(t, st, root)
	if owner() != "b.md" || nFind() != 0 {
		t.Fatalf("after owner edit: owner=%q findings=%d", owner(), nFind())
	}
}

func TestDuplicateIDThreeWayPromotion(t *testing.T) {
	root := evalDir(t, t.TempDir())
	for _, n := range []string{"a", "b", "c"} {
		write(t, filepath.Join(root, n+".md"), "## BRD-001 From "+n+"\n")
	}
	st := openStore(t, root)
	AddSource(st, root, ".")
	runIdx(t, st, root)
	if n := count(t, st, `SELECT count(*) FROM findings`); n != 2 {
		t.Fatalf("findings = %d", n)
	}
	os.Remove(filepath.Join(root, "a.md"))
	runIdx(t, st, root)
	var title string
	st.DB().QueryRow(`SELECT title FROM entities WHERE key='BRD-001'`).Scan(&title)
	if title != "From b" || count(t, st, `SELECT count(*) FROM findings`) != 1 {
		t.Fatalf("title=%q findings=%d", title, count(t, st, `SELECT count(*) FROM findings`))
	}
}

func TestWammyDirExcludedAndNeverIndexed(t *testing.T) {
	root := evalDir(t, t.TempDir())
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	write(t, filepath.Join(root, "docs", "a.md"), "## BRD-001 A\n")
	write(t, filepath.Join(root, ".Wammy", "note.md"), "## BRD-998 Hidden\n")
	write(t, filepath.Join(root, ".git", "x.md"), "## BRD-999 Hidden\n")
	write(t, filepath.Join(root, "sub", ".Wammy", "n.md"), "## BRD-997 Hidden\n")
	st := openStore(t, root)
	if err := AddSource(st, root, "."); err != nil {
		t.Fatal(err)
	}
	rep := runIdx(t, st, root)
	if rep.Scanned != 1 {
		t.Fatalf("scanned %d, want 1", rep.Scanned)
	}
	if n := count(t, st, `SELECT count(*) FROM documents WHERE path LIKE '.Wammy%' OR path LIKE '.git%' OR path LIKE '%/.Wammy/%'`); n != 0 {
		t.Fatalf("internal dirs indexed")
	}
	// an explicit source inside .Wammy is also never walked
	AddSource(st, root, ".Wammy")
	rep = runIdx(t, st, root)
	if n := count(t, st, `SELECT count(*) FROM entities WHERE key IN ('BRD-998','BRD-999','BRD-997')`); n != 0 {
		t.Fatalf("hidden entities indexed: %+v", rep)
	}
}

func TestSourcesSortedAndKinds(t *testing.T) {
	root, st := newWorkspace(t)
	AddSource(st, root, "features")
	AddSource(st, root, "docs/brd.md")
	srcs, err := ListSources(st)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, s := range srcs {
		got = append(got, fmt.Sprintf("%s:%s", s.Path, s.Kind))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "docs/brd.md:file,features:dir" {
		t.Fatalf("sources = %v", got)
	}
}

// interrupted runs Run and cancels it after the first document of the main
// pass, as Ctrl-C would.
func interrupted(t *testing.T, st *store.Store, root string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	afterDoc = cancel
	defer func() { afterDoc = nil }()
	if _, err := Run(ctx, st, root); err == nil {
		t.Fatal("expected the interrupted run to fail")
	}
}

func ownerAndFindings(t *testing.T, st *store.Store, key string) (string, int) {
	t.Helper()
	var p sql.NullString
	st.DB().QueryRow(`SELECT d.path FROM entities e JOIN documents d ON d.id=e.doc_id WHERE e.key=?`, key).Scan(&p)
	return p.String, count(t, st, `SELECT count(*) FROM findings WHERE kind='duplicate' AND key=?`, key)
}

func TestInterruptedRunAfterOwnerDeletedRecovers(t *testing.T) {
	root := evalDir(t, t.TempDir())
	write(t, filepath.Join(root, "a.md"), "## BRD-001 A\n")
	write(t, filepath.Join(root, "b.md"), "## BRD-001 B\n")
	st := openStore(t, root)
	AddSource(st, root, ".")
	runIdx(t, st, root)
	os.Remove(filepath.Join(root, "a.md"))
	interrupted(t, st, root)
	runIdx(t, st, root)
	if o, f := ownerAndFindings(t, st, "BRD-001"); o != "b.md" || f != 0 {
		t.Fatalf("owner=%q findings=%d", o, f)
	}
}

func TestInterruptedRunAfterTakeoverRecovers(t *testing.T) {
	root := evalDir(t, t.TempDir())
	write(t, filepath.Join(root, "b.md"), "## BRD-001 B\n")
	st := openStore(t, root)
	AddSource(st, root, ".")
	runIdx(t, st, root)
	write(t, filepath.Join(root, "a.md"), "## BRD-001 A\n")
	interrupted(t, st, root)
	runIdx(t, st, root)
	if o, f := ownerAndFindings(t, st, "BRD-001"); o != "a.md" || f != 1 {
		t.Fatalf("owner=%q findings=%d", o, f)
	}
	if n := count(t, st, `SELECT count(*) FROM entities`); n != 1 {
		t.Fatalf("entities=%d", n)
	}
}

func TestCaseVariantInternalDirsNotWalked(t *testing.T) {
	root := evalDir(t, t.TempDir())
	write(t, filepath.Join(root, "a.md"), "## BRD-001 A\n")
	write(t, filepath.Join(root, ".wammy", "x.md"), "## BRD-998 H\n")
	write(t, filepath.Join(root, ".GIT", "y.md"), "## BRD-999 H\n")
	st := openStore(t, root)
	AddSource(st, root, ".")
	rep := runIdx(t, st, root)
	if rep.Scanned != 1 {
		t.Fatalf("scanned %d, want 1", rep.Scanned)
	}
}

func TestSymlinkToDirectlyFoundFileIndexedOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	root := evalDir(t, t.TempDir())
	write(t, filepath.Join(root, "real", "a.md"), "## BRD-001 A\n")
	if err := os.Symlink(filepath.Join(root, "real", "a.md"), filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}
	st := openStore(t, root)
	AddSource(st, root, ".")
	rep := runIdx(t, st, root)
	if rep.Scanned != 1 || rep.Failed != 0 {
		t.Fatalf("report: %+v", rep)
	}
	if n := count(t, st, `SELECT count(*) FROM findings`); n != 0 {
		t.Fatalf("spurious findings: %d", n)
	}
	if n := count(t, st, `SELECT count(*) FROM documents WHERE path='real/a.md'`); n != 1 {
		t.Fatalf("real file not indexed")
	}
	// when the target is not covered by any source, the link path is indexed
	root2 := evalDir(t, t.TempDir())
	write(t, filepath.Join(root2, "real", "a.md"), "## BRD-001 A\n")
	os.MkdirAll(filepath.Join(root2, "links"), 0o755)
	os.Symlink(filepath.Join(root2, "real", "a.md"), filepath.Join(root2, "links", "link.md"))
	st2 := openStore(t, root2)
	if err := AddSource(st2, root2, "links"); err != nil {
		t.Fatal(err)
	}
	rep = runIdx(t, st2, root2)
	if n := count(t, st2, `SELECT count(*) FROM documents WHERE path='links/link.md'`); n != 1 {
		t.Fatalf("link not indexed: %+v", rep)
	}
}

func TestScenarioChunkHasEachStepOnce(t *testing.T) {
	root, st := newWorkspace(t)
	for _, d := range []string{"docs", "features"} {
		if err := AddSource(st, root, d); err != nil {
			t.Fatal(err)
		}
	}
	runIdx(t, st, root)
	var text string
	if err := st.DB().QueryRow(`SELECT c.text FROM chunks c JOIN entities e ON e.id = c.entity_id
		WHERE e.key = 'features/search.feature#Exact title wins over text matches'`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(text, `When I search for "Budget"`); n != 1 {
		t.Fatalf("step appears %d times in the chunk text:\n%s", n, text)
	}
}
