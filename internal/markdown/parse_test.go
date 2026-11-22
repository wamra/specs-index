package markdown

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/wamra/specs-index/internal/corpus"
	"github.com/wamra/specs-index/internal/model"
)

var corpusDocs = []string{"docs/brd.md", "docs/fds.md", "docs/acceptance.md"}

func parseCorpus(t *testing.T, rel string) model.Doc {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(corpus.Dir(), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	d, err := Parse(rel, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Path != rel {
		t.Fatalf("Doc.Path = %q, want %q", d.Path, rel)
	}
	return d
}

func parseStr(t *testing.T, s string) model.Doc {
	t.Helper()
	d, err := Parse("x.md", []byte(s), nil)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func entity(t *testing.T, d model.Doc, key string) model.Entity {
	t.Helper()
	var found []model.Entity
	for _, e := range d.Entities {
		if e.Key == key {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one entity %s, got %d", key, len(found))
	}
	return found[0]
}

type edgeKey struct{ src, dst, kind string }

func edgeSet(edges []model.Edge) map[edgeKey]model.Edge {
	m := map[edgeKey]model.Edge{}
	for _, e := range edges {
		m[edgeKey{e.Src, e.Dst, e.Kind}] = e
	}
	return m
}

func TestCorpusEntitiesMatchTruth(t *testing.T) {
	truth, err := corpus.LoadTruth()
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		path, key, typ, title string
		start, end            int
	}
	var want, got []row
	for _, e := range truth.Entities {
		if slices.Contains(corpusDocs, e.Path) {
			want = append(want, row{e.Path, e.Key, e.Type, e.Title, e.StartLine, e.EndLine})
		}
	}
	for _, rel := range corpusDocs {
		for _, e := range parseCorpus(t, rel).Entities {
			got = append(got, row{rel, e.Key, e.Type, e.Title, e.StartLine, e.EndLine})
		}
	}
	less := func(a, b row) int {
		if c := strings.Compare(a.path, b.path); c != 0 {
			return c
		}
		return strings.Compare(a.key, b.key)
	}
	slices.SortFunc(want, less)
	slices.SortFunc(got, less)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entities differ: got %d, want %d", len(got), len(want))
		for i := 0; i < len(got) && i < len(want); i++ {
			if got[i] != want[i] {
				t.Errorf("first diff:\n got  %+v\n want %+v", got[i], want[i])
				break
			}
		}
	}
}

func TestCorpusEdgesMatchTruth(t *testing.T) {
	truth, err := corpus.LoadTruth()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range corpusDocs {
		d := parseCorpus(t, rel)
		want := map[edgeKey]string{}
		for _, e := range truth.Edges {
			if strings.HasSuffix(e.Path, ".md") && e.Path == rel {
				want[edgeKey{e.Src, e.Dst, e.Kind}] = e.Origin
			}
		}
		got := map[edgeKey]string{}
		for k, e := range edgeSet(d.Edges) {
			got[k] = e.Origin
			if !e.Explicit || e.Line == 0 {
				t.Errorf("%s: edge %+v must be explicit with a line", rel, e)
			}
		}
		for k, o := range want {
			if g, ok := got[k]; !ok {
				t.Errorf("%s: missing edge %+v", rel, k)
			} else if g != o {
				t.Errorf("%s: edge %+v origin %q, want %q", rel, k, g, o)
			}
		}
		for k := range got {
			if _, ok := want[k]; !ok {
				t.Errorf("%s: unexpected edge %+v", rel, k)
			}
		}
	}
}

func TestCorpusFrontMatterAndFields(t *testing.T) {
	d := parseCorpus(t, "docs/brd.md")
	if d.Type != "brd" || d.Version != "0.3" {
		t.Errorf("front matter = %q %q", d.Type, d.Version)
	}
	e := entity(t, d, "BRD-023")
	if e.Status != "deprecated" || e.Priority != "P3" {
		t.Errorf("BRD-023 status/priority = %q/%q", e.Status, e.Priority)
	}
	if !strings.HasPrefix(e.Body, "Paper documents") || strings.Contains(e.Body, "Status:") {
		t.Errorf("BRD-023 body = %q", e.Body)
	}
}

func TestHeadingAndTableRowMerge(t *testing.T) {
	d := parseStr(t, strings.Join([]string{
		"| FDS ID | Design | Mapped BRDs |",
		"|---|---|---|",
		"| FDS-003 | Row title | BRD-004, BRD-005 |",
		"",
		"### FDS-003 Heading title",
		"Status: approved",
		"Body from the heading, see `FDS-005`.",
	}, "\n"))
	if n := len(d.Entities); n != 1 {
		t.Fatalf("entities = %d, want 1", n)
	}
	e := d.Entities[0]
	if e.Title != "Heading title" || e.StartLine != 5 || e.EndLine != 7 {
		t.Errorf("heading must win: %+v", e)
	}
	if e.Body != "Body from the heading, see `FDS-005`." {
		t.Errorf("body = %q", e.Body)
	}
	if e.Status != "approved" {
		t.Errorf("status = %q", e.Status)
	}
	set := edgeSet(d.Edges)
	for _, k := range []edgeKey{
		{"FDS-003", "BRD-004", "implements"}, {"FDS-003", "BRD-005", "implements"},
		{"FDS-003", "FDS-005", "references"},
	} {
		if _, ok := set[k]; !ok {
			t.Errorf("missing edge %+v", k)
		}
	}
}

func TestNestedIDHeadingIsSeparateEntity(t *testing.T) {
	d := parseStr(t, strings.Join([]string{
		"## BRD-001 Parent",
		"Parent text.",
		"### BRD-002 Child",
		"Child text.",
		"### FDS-001 Other prefix",
		"Other text.",
		"## BRD-003 Sibling",
	}, "\n"))
	p, c := entity(t, d, "BRD-001"), entity(t, d, "BRD-002")
	if p.Body != "Parent text." {
		t.Errorf("parent body = %q", p.Body)
	}
	if p.StartLine != 1 || p.EndLine != 6 {
		t.Errorf("parent span = %d-%d, want 1-6", p.StartLine, p.EndLine)
	}
	if c.Body != "Child text." || c.StartLine != 3 || c.EndLine != 4 {
		t.Errorf("child = %+v", c)
	}
	set := edgeSet(d.Edges)
	if e, ok := set[edgeKey{"BRD-001", "BRD-002", "parent_of"}]; !ok || !e.Explicit {
		t.Errorf("missing parent_of BRD-001 -> BRD-002")
	}
	if _, ok := set[edgeKey{"BRD-001", "FDS-001", "parent_of"}]; ok {
		t.Errorf("parent_of across prefixes must not exist")
	}
	if _, ok := set[edgeKey{"BRD-001", "BRD-003", "parent_of"}]; ok {
		t.Errorf("siblings must not be linked")
	}
}

func TestCodeFenceIgnoredInlineCodeReferenced(t *testing.T) {
	d := parseStr(t, strings.Join([]string{
		"## BRD-001 Thing",
		"See `BRD-002` and BRD-003.",
		"```",
		"BRD-777 FDS-777",
		"## BRD-888 not a heading",
		"```",
		"~~~go",
		"BRD-999",
		"~~~",
	}, "\n"))
	if len(d.Entities) != 1 {
		t.Fatalf("entities = %+v", d.Entities)
	}
	set := edgeSet(d.Edges)
	if len(set) != 2 {
		t.Errorf("edges = %+v", d.Edges)
	}
	for _, dst := range []string{"BRD-002", "BRD-003"} {
		if e, ok := set[edgeKey{"BRD-001", dst, "references"}]; !ok || e.Origin != "mention" {
			t.Errorf("missing references -> %s", dst)
		}
	}
}

func TestFrontMatterTypeAndVersion(t *testing.T) {
	d := parseStr(t, "---\ntype: \"fds\"\nversion: 0.2\n---\n## BRD-001 A\ntext\n")
	if d.Type != "fds" || d.Version != "0.2" {
		t.Errorf("type/version = %q/%q", d.Type, d.Version)
	}
	if e := entity(t, d, "BRD-001"); e.StartLine != 5 || e.EndLine != 6 {
		t.Errorf("span = %d-%d, want 5-6", e.StartLine, e.EndLine)
	}
	if d := parseStr(t, "## BRD-001 A\n"); d.Type != "" || d.Version != "" {
		t.Errorf("no front matter: %q/%q", d.Type, d.Version)
	}
}

func TestCustomPrefixTypes(t *testing.T) {
	d, err := Parse("x.md", []byte("## NFR-001 Fast\n## ZZ-001 Other\n"), map[string]string{"NFR": "design"})
	if err != nil {
		t.Fatal(err)
	}
	if got := entity(t, d, "NFR-001").Type; got != "design" {
		t.Errorf("NFR type = %q", got)
	}
	if got := entity(t, d, "ZZ-001").Type; got != "requirement" {
		t.Errorf("unknown prefix type = %q", got)
	}
}

func TestACTableFDSColumnDirection(t *testing.T) {
	d := parseStr(t, "| AC ID | Criterion | FDS |\n|---|---|---|\n| AC-001 | Works | FDS-001, FDS-002 |\n")
	set := edgeSet(d.Edges)
	for _, src := range []string{"FDS-001", "FDS-002"} {
		if e, ok := set[edgeKey{src, "AC-001", "verified_by"}]; !ok || e.Origin != "table" || e.Line != 3 {
			t.Errorf("missing %s verified_by AC-001: %+v", src, e)
		}
	}
}

func TestCRLFAndBOM(t *testing.T) {
	for _, rel := range corpusDocs {
		b, err := os.ReadFile(filepath.Join(corpus.Dir(), filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		lf, err := Parse(rel, b, nil)
		if err != nil {
			t.Fatal(err)
		}
		crlf := "\xEF\xBB\xBF" + strings.ReplaceAll(string(b), "\n", "\r\n")
		got, err := Parse(rel, []byte(crlf), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, lf) {
			t.Errorf("%s: CRLF+BOM parse differs from LF parse", rel)
		}
		if len(got.Entities) == 0 {
			t.Errorf("%s: no entities", rel)
		}
	}
}

func TestFieldLineSegmentWithoutKeyContinuesValue(t *testing.T) {
	d := parseStr(t, "## BRD-001 A\nDepends on: BRD-003,  BRD-004\nStatus: in  review    Priority: P1\n\nBody.\n")
	set := edgeSet(d.Edges)
	for _, dst := range []string{"BRD-003", "BRD-004"} {
		if e, ok := set[edgeKey{"BRD-001", dst, "depends_on"}]; !ok || e.Origin != "field" {
			t.Errorf("missing depends_on -> %s (edges %+v)", dst, d.Edges)
		}
	}
	for k := range set {
		if k.kind == "references" {
			t.Errorf("unexpected references edge %+v", k)
		}
	}
	e := entity(t, d, "BRD-001")
	if e.Status != "in review" || e.Priority != "P1" {
		t.Errorf("status/priority = %q/%q", e.Status, e.Priority)
	}
	if e.Body != "Body." {
		t.Errorf("body = %q", e.Body)
	}
}

func TestCustomTypesMergeOverDefaults(t *testing.T) {
	d, err := Parse("x.md", []byte("## NFR-001 Fast\n## FDS-001 D\n## BRD-001 R\n"), map[string]string{"NFR": "design"})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"NFR-001": "design", "FDS-001": "design", "BRD-001": "requirement"} {
		if got := entity(t, d, key).Type; got != want {
			t.Errorf("%s type = %q, want %q", key, got, want)
		}
	}
	d, err = Parse("x.md", []byte("## AC-001 X\n"), map[string]string{"AC": "requirement"})
	if err != nil {
		t.Fatal(err)
	}
	if got := entity(t, d, "AC-001").Type; got != "requirement" {
		t.Errorf("override of default prefix: %q", got)
	}
}
