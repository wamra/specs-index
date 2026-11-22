package gherkin

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

func parseStr(t *testing.T, s string) model.Doc {
	t.Helper()
	d, err := Parse("x.feature", []byte(s))
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
		t.Fatalf("want exactly one entity %q, got %d (entities: %v)", key, len(found), keys(d))
	}
	return found[0]
}

func keys(d model.Doc) []string {
	var k []string
	for _, e := range d.Entities {
		k = append(k, e.Key)
	}
	return k
}

type edgeKey struct{ src, dst, kind string }

func edgeSet(edges []model.Edge) map[edgeKey]model.Edge {
	m := map[edgeKey]model.Edge{}
	for _, e := range edges {
		m[edgeKey{e.Src, e.Dst, e.Kind}] = e
	}
	return m
}

func TestCorpusScenariosMatchTruth(t *testing.T) {
	truth, err := corpus.LoadTruth()
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(corpus.Dir(), "features", "*.feature"))
	if err != nil || len(files) != 8 {
		t.Fatalf("want 8 feature files, got %d (%v)", len(files), err)
	}
	scenarios := 0
	for _, f := range files {
		rel := "features/" + filepath.Base(f)
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		d, err := Parse(rel, b)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if d.Path != rel {
			t.Fatalf("Doc.Path = %q, want %q", d.Path, rel)
		}
		type row struct {
			key, typ, title string
			start, end      int
			steps           []int
		}
		var want, got []row
		for _, e := range truth.Entities {
			if e.Path == rel {
				want = append(want, row{e.Key, e.Type, e.Title, e.StartLine, e.EndLine, e.StepLines})
				if e.Type == "scenario" {
					scenarios++
				}
			}
		}
		for _, e := range d.Entities {
			var lines []int
			for i, s := range e.Steps {
				if s.Seq != i+1 {
					t.Errorf("%s step %d has Seq %d", e.Key, i, s.Seq)
				}
				lines = append(lines, s.Line)
			}
			got = append(got, row{e.Key, e.Type, e.Title, e.StartLine, e.EndLine, lines})
		}
		less := func(a, b row) int { return strings.Compare(a.key, b.key) }
		slices.SortFunc(want, less)
		slices.SortFunc(got, less)
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s entities differ\nwant %+v\n got %+v", rel, want, got)
		}

		type e3 struct{ src, dst, kind string }
		wantE := map[e3]bool{}
		for _, e := range truth.Edges {
			if e.Path == rel {
				wantE[e3{e.Src, e.Dst, e.Kind}] = true
			}
		}
		gotE := map[e3]bool{}
		for _, e := range d.Edges {
			if e.Origin != "tag" || !e.Explicit {
				t.Errorf("%s edge %+v: want Origin tag, Explicit true", rel, e)
			}
			gotE[e3{e.Src, e.Dst, e.Kind}] = true
		}
		if !reflect.DeepEqual(wantE, gotE) {
			t.Errorf("%s edges differ\nwant %v\n got %v", rel, wantE, gotE)
		}
		if len(d.Edges) != len(gotE) {
			t.Errorf("%s has duplicate edges", rel)
		}
	}
	if scenarios != 30 {
		t.Errorf("truth scenarios = %d, want 30", scenarios)
	}
}

func TestIDTagsBecomeVerifiedBy(t *testing.T) {
	d := parseStr(t, `@FDS-003
Feature: Search

  @BRD-004 @smoke
  Scenario: One
    Given a

  @BRD-009
  Rule: R

    Scenario: Two
      Given b

    @BRD-010
    Scenario Outline: Three
      Given <x>

      @BRD-011
      Examples:
        | x |
        | 1 |
`)
	es := edgeSet(d.Edges)
	one, two, three := "x.feature#One", "x.feature#Two", "x.feature#Three"
	want := []edgeKey{
		{"FDS-003", one, "verified_by"}, {"BRD-004", one, "verified_by"},
		{"FDS-003", two, "verified_by"}, {"BRD-009", two, "verified_by"},
		{"FDS-003", three, "verified_by"}, {"BRD-009", three, "verified_by"},
		{"BRD-010", three, "verified_by"}, {"BRD-011", three, "verified_by"},
	}
	for _, k := range want {
		e, ok := es[k]
		if !ok {
			t.Errorf("missing edge %+v", k)
			continue
		}
		if e.Origin != "tag" || !e.Explicit {
			t.Errorf("edge %+v: origin %q explicit %v", k, e.Origin, e.Explicit)
		}
	}
	if len(d.Edges) != len(want) {
		t.Errorf("got %d edges, want %d: %+v", len(d.Edges), len(want), d.Edges)
	}
	if es[edgeKey{"BRD-004", one, "verified_by"}].Line != 4 {
		t.Errorf("edge line = %d, want 4", es[edgeKey{"BRD-004", one, "verified_by"}].Line)
	}
}

func TestPlainTagsKept(t *testing.T) {
	d := parseStr(t, "@FDS-001 @area\nFeature: F\n\n  @smoke @BRD-001 @wip\n  Scenario: S\n    Given a\n")
	s := entity(t, d, "x.feature#S")
	want := []string{"area", "smoke", "wip"}
	if !reflect.DeepEqual(s.Tags, want) {
		t.Errorf("scenario tags = %v, want %v", s.Tags, want)
	}
	f := entity(t, d, "x.feature")
	if !reflect.DeepEqual(f.Tags, []string{"area"}) {
		t.Errorf("feature tags = %v", f.Tags)
	}
}

func TestIDOverrideTag(t *testing.T) {
	d := parseStr(t, "Feature: F\n\n  @id:SCN-012 @BRD-001\n  Scenario: S\n    Given a\n")
	s := entity(t, d, "SCN-012")
	if s.Title != "S" || len(s.Tags) != 0 {
		t.Errorf("got title %q tags %v", s.Title, s.Tags)
	}
	if _, ok := edgeSet(d.Edges)[edgeKey{"BRD-001", "SCN-012", "verified_by"}]; !ok {
		t.Errorf("edge to SCN-012 missing: %+v", d.Edges)
	}
}

func TestDuplicateScenarioNames(t *testing.T) {
	d := parseStr(t, "Feature: F\n\n  Scenario: Same\n    Given a\n\n  Scenario: Same\n    Given b\n\n  Scenario: Same\n    Given c\n")
	for _, k := range []string{"x.feature#Same", "x.feature#Same~2", "x.feature#Same~3"} {
		entity(t, d, k)
	}
}

func TestOutlineExamplesInBody(t *testing.T) {
	d := parseStr(t, `Feature: F

  Scenario Outline: O
    Given <x>
    Then "<y>"

    Examples:
      | x | y |
      | 1 | a |
`)
	o := entity(t, d, "x.feature#O")
	if o.EndLine != 9 {
		t.Errorf("EndLine = %d, want 9", o.EndLine)
	}
	if len(o.Steps) != 2 {
		t.Errorf("steps = %d, want 2 (Examples rows are not steps)", len(o.Steps))
	}
	for _, w := range []string{"Examples:", "| x | y |", "| 1 | a |"} {
		if !strings.Contains(o.Body, w) {
			t.Errorf("body %q lacks %q", o.Body, w)
		}
	}
}

func TestBackgroundStepsNotAScenario(t *testing.T) {
	d := parseStr(t, `Feature: F

  Background:
    Given bg step

  Scenario: S
    When own step
`)
	if len(d.Entities) != 2 {
		t.Fatalf("entities = %v, want feature + one scenario", keys(d))
	}
	s := entity(t, d, "x.feature#S")
	if len(s.Steps) != 1 || s.Steps[0].Text != "own step" || s.Steps[0].Keyword != "When" || s.Steps[0].Line != 7 {
		t.Errorf("steps = %+v", s.Steps)
	}
	if s.StartLine != 6 {
		t.Errorf("StartLine = %d, want 6", s.StartLine)
	}
}

func TestDocStringsAndTablesAndComments(t *testing.T) {
	d := parseStr(t, "Feature: F\n\n  Scenario: S\n    Given a doc\n      \"\"\"\n      Scenario: not one\n      @BRD-001\n      \"\"\"\n    # a comment\n    And a table\n      | a |\n      | b |\n    * star step\n    # trailing\n")
	if len(d.Entities) != 2 || len(d.Edges) != 0 {
		t.Fatalf("entities %v edges %v", keys(d), d.Edges)
	}
	s := entity(t, d, "x.feature#S")
	var lines []int
	for _, st := range s.Steps {
		lines = append(lines, st.Line)
	}
	if !reflect.DeepEqual(lines, []int{4, 10, 13}) {
		t.Errorf("step lines = %v", lines)
	}
	if s.Steps[2].Keyword != "*" {
		t.Errorf("keyword = %q", s.Steps[2].Keyword)
	}
	if s.EndLine != 13 {
		t.Errorf("EndLine = %d, want 13 (trailing comment excluded)", s.EndLine)
	}
}

func TestExampleAndScenarioTemplateKeywords(t *testing.T) {
	d := parseStr(t, "Feature: F\n\n  Example: A\n    Given a\n\n  Scenario Template: B\n    Given <x>\n\n    Examples:\n      | x |\n      | 1 |\n")
	entity(t, d, "x.feature#A")
	entity(t, d, "x.feature#B")
}

func TestNonEnglishLanguageHeaderRejected(t *testing.T) {
	_, err := Parse("x.feature", []byte("# language: fr\nFonctionnalité: F\n"))
	if err == nil || !strings.Contains(err.Error(), "language") {
		t.Fatalf("err = %v, want text containing 'language'", err)
	}
	if _, err := Parse("x.feature", []byte("# language: en\nFeature: F\n")); err != nil {
		t.Errorf("en header rejected: %v", err)
	}
}

func TestCRLFAndBOM(t *testing.T) {
	lf := "@FDS-001\nFeature: F\n\n  @BRD-001\n  Scenario: S\n    Given a\n    Then b\n"
	want := parseStr(t, lf)
	got := parseStr(t, "\xef\xbb\xbf"+strings.ReplaceAll(lf, "\n", "\r\n"))
	if !reflect.DeepEqual(want, got) {
		t.Errorf("CRLF+BOM result differs\nwant %+v\n got %+v", want, got)
	}
	if entity(t, got, "x.feature").Title != "F" {
		t.Error("feature title wrong")
	}
}

func TestDocTypeAndPath(t *testing.T) {
	d := parseStr(t, "Feature: F\n")
	if d.Path != "x.feature" || d.Type != "feature" {
		t.Errorf("Path %q Type %q", d.Path, d.Type)
	}
}

func TestBackgroundAndRuleTextInFeatureBody(t *testing.T) {
	d := parseStr(t, `Feature: F
  Feature description.

  Background: Shared setup
    Given the shared step
    And a second shared step

  Rule: Only editors restore
    Rule description line.

    Scenario: S
      When own step
`)
	if len(d.Entities) != 2 {
		t.Fatalf("entities = %v, want feature + one scenario", keys(d))
	}
	b := entity(t, d, "x.feature").Body
	want := []string{
		"Feature description.",
		"Background: Shared setup",
		"Given the shared step",
		"And a second shared step",
		"Rule: Only editors restore",
		"Rule description line.",
	}
	last := -1
	for _, w := range want {
		i := strings.Index(b, w)
		if i < 0 || i < last {
			t.Fatalf("body %q: %q missing or out of order", b, w)
		}
		last = i
	}
	s := entity(t, d, "x.feature#S")
	if len(s.Steps) != 1 || s.Steps[0].Text != "own step" {
		t.Errorf("scenario steps = %+v", s.Steps)
	}
	if strings.Contains(s.Body, "shared") {
		t.Errorf("scenario body leaked background: %q", s.Body)
	}
}
