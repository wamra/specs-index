// Package corpus gives tests access to the fixture corpus under
// testdata/corpus and its ground-truth file.
package corpus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// Dir returns the absolute path of testdata/corpus.
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "corpus")
}

// TruthEntity is one entity expected from the corpus. Paths are relative to
// the corpus root with forward slashes; lines are 1-based. StepLines lists the
// lines of a scenario's own steps (scenarios only).
type TruthEntity struct {
	Key       string `json:"key"`
	Type      string `json:"type"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Title     string `json:"title"`
	StepLines []int  `json:"step_lines,omitempty"`
}

// TruthEdge is one expected explicit edge.
type TruthEdge struct {
	Src  string `json:"src"`
	Dst  string `json:"dst"`
	Kind string `json:"kind"`
}

// TruthUncovered lists the deliberate gaps in the corpus.
type TruthUncovered struct {
	RequirementsWithoutDesign []string `json:"requirements_without_design"`
	DesignsWithoutScenario    []string `json:"designs_without_scenario"`
	Dangling                  []string `json:"dangling"`
	Duplicates                []string `json:"duplicates"`
}

// TruthQuery is a search expectation: every ExpectTop key is in the top
// results, no ExpectAbsent key is in the results.
type TruthQuery struct {
	Q            string   `json:"q"`
	ExpectTop    []string `json:"expect_top"`
	ExpectAbsent []string `json:"expect_absent"`
}

// Truth mirrors testdata/corpus/truth.json.
type Truth struct {
	Entities  []TruthEntity  `json:"entities"`
	Edges     []TruthEdge    `json:"edges"`
	Uncovered TruthUncovered `json:"uncovered"`
	Queries   []TruthQuery   `json:"queries"`
}

// LoadTruth reads and decodes truth.json.
func LoadTruth() (Truth, error) {
	var t Truth
	b, err := os.ReadFile(filepath.Join(Dir(), "truth.json"))
	if err != nil {
		return t, err
	}
	err = json.Unmarshal(b, &t)
	return t, err
}
