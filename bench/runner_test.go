package main

import (
	"encoding/json"
	"math"
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/wamra/specs-index/internal/corpus"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestMetricsOnKnownRanking(t *testing.T) {
	ranked10 := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "E"} // E at rank 10
	qs := []Query{
		{Q: "q1", Category: "exact_id", ExpectTop: []string{"A"}},
		{Q: "q2", Category: "keyword", ExpectTop: []string{"B", "C"}},
		{Q: "q3", Category: "keyword", ExpectTop: []string{"D"}},
		{Q: "q4", Category: "semantic", ExpectTop: []string{"E"}},
		{Q: "q5", Category: "exact_id", ExpectTop: []string{"F"}, ExpectAbsent: []string{"Z"}},
	}
	rankings := [][]string{
		{"X", "A", "Y"},                // A at rank 2: recall 1, RR 0.5, acc@1 miss
		{"B", "x", "y", "z", "w", "C"}, // B rank 1, C rank 6: recall 0.5, RR 1
		{},                             // nothing returned: 0, 0
		ranked10,                       // E at rank 10: recall@5 0, RR 0.1
		{"F", "Z"},                     // F at 1: recall 1, RR 1, acc@1 hit, absent violated
	}
	m := Summarize(qs, rankings)
	if m.Queries != 5 {
		t.Fatalf("queries = %d", m.Queries)
	}
	if want := (1 + 0.5 + 0 + 0 + 1) / 5.0; !near(m.RecallAt5, want) {
		t.Errorf("Recall@5 = %v, want %v", m.RecallAt5, want)
	}
	if want := (0.5 + 1 + 0 + 0.1 + 1) / 5.0; !near(m.MRRAt10, want) {
		t.Errorf("MRR@10 = %v, want %v", m.MRRAt10, want)
	}
	if m.ExactIDQueries != 2 || !near(m.ExactIDAccuracyAt1, 0.5) {
		t.Errorf("exact-ID accuracy = %v over %d, want 0.5 over 2", m.ExactIDAccuracyAt1, m.ExactIDQueries)
	}
	if m.AbsentViolations != 1 {
		t.Errorf("absent violations = %d, want 1", m.AbsentViolations)
	}
	if kw := m.PerCategory["keyword"]; kw.Queries != 2 || !near(kw.RecallAt5, 0.25) || !near(kw.MRRAt10, 0.5) {
		t.Errorf("keyword category = %+v", kw)
	}
}

func TestMRRIgnoresRanksBeyondTen(t *testing.T) {
	ranked := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "T"}
	if got := reciprocalRank([]string{"T"}, ranked, 10); got != 0 {
		t.Errorf("RR for rank 11 = %v, want 0", got)
	}
	if got := reciprocalRank([]string{"T", "3"}, ranked, 10); !near(got, 1.0/3) {
		t.Errorf("RR with two relevant = %v, want 1/3", got)
	}
}

func TestPercentileNearestRank(t *testing.T) {
	s := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	cases := []struct {
		p, want float64
	}{{50, 50}, {95, 100}, {90, 90}, {10, 10}, {100, 100}}
	for _, c := range cases {
		if got := Percentile(s, c.p); got != c.want {
			t.Errorf("p%v = %v, want %v", c.p, got, c.want)
		}
	}
	if got := Percentile(nil, 50); got != 0 {
		t.Errorf("empty = %v", got)
	}
}

func TestDedupeKeepsFirstOccurrence(t *testing.T) {
	got := dedupe([]string{"a", "b", "a", "c", "b"})
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("dedupe = %v", got)
	}
}

func TestQueriesJSONConsistentWithCorpus(t *testing.T) {
	truth, err := corpus.LoadTruth()
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, e := range truth.Entities {
		keys[e.Key] = true
	}
	qs, err := LoadQueries("queries.json")
	if err != nil {
		t.Fatal(err)
	}
	cats := map[string]int{}
	seen := map[string]bool{}
	idRe := regexp.MustCompile(`^[A-Z][A-Z0-9]*-\d+$`)
	for _, q := range qs {
		if q.Q == "" {
			t.Error("empty query text")
		}
		if seen[q.Q] {
			t.Errorf("duplicate query %q", q.Q)
		}
		seen[q.Q] = true
		if !slices.Contains(Categories, q.Category) {
			t.Errorf("query %q: category %q not one of %v", q.Q, q.Category, Categories)
		}
		cats[q.Category]++
		if len(q.ExpectTop) == 0 {
			t.Errorf("query %q has no expect_top", q.Q)
		}
		for _, k := range append(slices.Clone(q.ExpectTop), q.ExpectAbsent...) {
			if !keys[k] {
				t.Errorf("query %q: expected key %q does not exist in the corpus", q.Q, k)
			}
		}
		if q.Category == "exact_id" && (len(q.ExpectTop) != 1 || q.ExpectTop[0] != q.Q) {
			t.Errorf("exact_id query %q must expect exactly itself, got %v", q.Q, q.ExpectTop)
		}
		if q.Category == "exact_id" && !idRe.MatchString(q.Q) && !keys[q.Q] {
			t.Errorf("exact_id query %q is not an entity key", q.Q)
		}
	}
	for _, c := range Categories {
		if cats[c] < 5 {
			t.Errorf("category %s has %d queries, want at least 5", c, cats[c])
		}
	}
	// every truth query must be present, with the same expectations
	byQ := map[string]Query{}
	for _, q := range qs {
		byQ[q.Q] = q
	}
	for _, tq := range truth.Queries {
		q, ok := byQ[tq.Q]
		if !ok {
			t.Errorf("truth query %q missing from queries.json", tq.Q)
			continue
		}
		if !slices.Equal(q.ExpectTop, tq.ExpectTop) || !slices.Equal(nonNil(q.ExpectAbsent), nonNil(tq.ExpectAbsent)) {
			t.Errorf("truth query %q differs from queries.json", tq.Q)
		}
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func TestResultJSONRoundTrip(t *testing.T) {
	r := Result{Config: "X", Status: StatusNotRun, Reason: "because"}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back Result
	if err := json.Unmarshal(b, &back); err != nil || back.Reason != "because" || back.Status != StatusNotRun {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	if _, err := os.Stat("queries.json"); err != nil {
		t.Fatal(err)
	}
}
