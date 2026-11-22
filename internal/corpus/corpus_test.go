package corpus

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var stepRe = regexp.MustCompile(`^(Given|When|Then|And|But|\*) `)

func readLines(t *testing.T, rel string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(Dir(), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestTruthIsSelfConsistent(t *testing.T) {
	truth, err := LoadTruth()
	if err != nil {
		t.Fatal(err)
	}
	type pk struct{ key, path string }
	byKey := map[string]string{} // key -> type
	paths := map[string]map[string]bool{}
	seen := map[pk]bool{}
	for _, e := range truth.Entities {
		if seen[pk{e.Key, e.Path}] {
			t.Errorf("entity %q listed twice in %s", e.Key, e.Path)
		}
		seen[pk{e.Key, e.Path}] = true
		byKey[e.Key] = e.Type
		if paths[e.Key] == nil {
			paths[e.Key] = map[string]bool{}
		}
		paths[e.Key][e.Path] = true

		lines := readLines(t, e.Path)
		if e.StartLine < 1 || e.StartLine > e.EndLine || e.EndLine > len(lines) {
			t.Errorf("%s: span %d-%d outside %s (%d lines)", e.Key, e.StartLine, e.EndLine, e.Path, len(lines))
			continue
		}
		at := e.StartLine
		switch e.Type {
		case "scenario", "feature":
			for at < e.EndLine && strings.HasPrefix(strings.TrimSpace(lines[at-1]), "@") {
				at++
			}
			if !strings.Contains(lines[at-1], e.Title) {
				t.Errorf("%s: title %q not on line %d: %q", e.Key, e.Title, at, lines[at-1])
			}
		default:
			if !strings.Contains(lines[at-1], e.Key) {
				t.Errorf("%s: key not on start line %d: %q", e.Key, at, lines[at-1])
			}
		}
		if len(e.StepLines) > 0 && e.Type != "scenario" {
			t.Errorf("%s: step_lines on a %s", e.Key, e.Type)
		}
		for _, sl := range e.StepLines {
			if sl < e.StartLine || sl > e.EndLine {
				t.Errorf("%s: step line %d outside span %d-%d", e.Key, sl, e.StartLine, e.EndLine)
				continue
			}
			if !stepRe.MatchString(strings.TrimSpace(lines[sl-1])) {
				t.Errorf("%s: step line %d is not a step: %q", e.Key, sl, lines[sl-1])
			}
		}
		if e.Type == "scenario" && len(e.StepLines) == 0 {
			t.Errorf("%s: scenario without step_lines", e.Key)
		}
	}

	dangling := map[string]bool{}
	for _, d := range truth.Uncovered.Dangling {
		if _, ok := byKey[d]; ok {
			t.Errorf("dangling %q is also an entity", d)
		}
		dangling[d] = true
	}
	kinds := []string{"parent_of", "implements", "verified_by", "depends_on", "conflicts_with", "references"}
	implemented := map[string]bool{}
	verified := map[string]bool{}
	for _, e := range truth.Edges {
		if !slices.Contains(kinds, e.Kind) {
			t.Errorf("edge %s->%s: unknown kind %q", e.Src, e.Dst, e.Kind)
		}
		if _, ok := byKey[e.Src]; !ok {
			t.Errorf("edge source %q is not an entity", e.Src)
		}
		if _, ok := byKey[e.Dst]; !ok && !dangling[e.Dst] {
			t.Errorf("edge destination %q is neither an entity nor dangling", e.Dst)
		}
		switch e.Kind {
		case "implements":
			implemented[e.Dst] = true
		case "verified_by":
			verified[e.Src] = true
		}
	}

	var wantReq, wantDes, wantDup []string
	for key, typ := range byKey {
		switch {
		case typ == "requirement" && !implemented[key]:
			wantReq = append(wantReq, key)
		case typ == "design" && !verified[key]:
			wantDes = append(wantDes, key)
		}
		if len(paths[key]) > 1 {
			wantDup = append(wantDup, key)
		}
	}
	for name, pair := range map[string][2][]string{
		"requirements_without_design": {wantReq, truth.Uncovered.RequirementsWithoutDesign},
		"designs_without_scenario":    {wantDes, truth.Uncovered.DesignsWithoutScenario},
		"duplicates":                  {wantDup, truth.Uncovered.Duplicates},
	} {
		got, want := slices.Clone(pair[1]), slices.Clone(pair[0])
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("uncovered.%s = %v, derived from entities and edges %v", name, got, want)
		}
	}

	for _, q := range truth.Queries {
		for _, k := range append(slices.Clone(q.ExpectTop), q.ExpectAbsent...) {
			if _, ok := byKey[k]; !ok {
				t.Errorf("query %q names unknown key %q", q.Q, k)
			}
		}
	}
}

func TestCorpusFilesAreCleanText(t *testing.T) {
	err := filepath.WalkDir(Dir(), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(Dir(), p)
		switch {
		case bytes.HasPrefix(b, []byte("\xef\xbb\xbf")):
			t.Errorf("%s: has a BOM", rel)
		case bytes.Contains(b, []byte("\r")):
			t.Errorf("%s: has CR", rel)
		case !bytes.HasSuffix(b, []byte("\n")) || bytes.HasSuffix(b, []byte("\n\n")):
			t.Errorf("%s: must end with exactly one newline", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
