package main

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/wamra/specs-index/internal/api"
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

type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("network call to %s", r.URL)
	return nil, errors.New("network disabled in test")
}

func TestConfigARefusedWithoutAllowCloud(t *testing.T) {
	oldCmd, oldTr := newCommand, http.DefaultTransport
	t.Cleanup(func() { newCommand, http.DefaultTransport = oldCmd, oldTr })
	newCommand = func(name string, arg ...string) *exec.Cmd {
		t.Errorf("a process was started: %s %v", name, arg)
		return exec.Command("true")
	}
	http.DefaultTransport = failingTransport{t}
	o, err := parseFlags([]string{"--config", "A", "--out", filepath.Join(t.TempDir(), "A.json")}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	res, err := runViaChild(o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusNotRun || res.Metrics() != nil || res.Config != "A" {
		t.Fatalf("result = %+v", res)
	}
	if res.Reason == "" || !regexp.MustCompile(`--allow-cloud`).MatchString(res.Reason) {
		t.Errorf("reason does not name --allow-cloud: %q", res.Reason)
	}
}

func (r Result) Metrics() *Metrics {
	if r.MetricsAll != nil {
		return r.MetricsAll
	}
	return r.MetricsUnfiltered
}

func TestParseFlags(t *testing.T) {
	out := filepath.Join(t.TempDir(), "x.json")
	bad := [][]string{
		{"--out", out},                  // no config
		{"--config", "D", "--out", out}, // unknown config
		{"--config", "B"},               // no out
		{"--config", "B", "--out", out, "--repeats", "0"}, // repeats < 1
		{"--config", "A", "--out", out, "--price-per-1k-tokens", "abc"},
		{"--config", "A", "--out", out, "--price-per-1k-tokens", "-1"},
	}
	for _, a := range bad {
		if _, err := parseFlags(a, io.Discard); err == nil {
			t.Errorf("parseFlags(%v) accepted", a)
		}
	}
	o, err := parseFlags([]string{"--config", "B", "--out", out}, io.Discard)
	if err != nil || o.repeats != 20 || o.warmup != 1 || o.allowCloud || o.ollama != defaultOll {
		t.Errorf("defaults: %+v %v", o, err)
	}
	if o, _ := parseFlags([]string{"--config", "A", "--out", out}, io.Discard); o.repeats != 1 {
		t.Errorf("config A default repeats = %d, want 1 (paid calls)", o.repeats)
	}
	if o, _ := parseFlags([]string{"--config", "A", "--out", out, "--repeats", "5"}, io.Discard); o.repeats != 5 {
		t.Errorf("explicit repeats ignored: %d", o.repeats)
	}
	if o, _ := parseFlags([]string{"--config", "A", "--out", out, "--allow-cloud", "--price-per-1k-tokens", "0.5"}, io.Discard); !o.allowCloud || o.price != "0.5" {
		t.Errorf("flags not read: %+v", o)
	}
}

func TestHasModel(t *testing.T) {
	names := []string{"mxbai-embed-large:latest", "embeddinggemma:latest", "qwen3-embedding:0.6b"}
	cases := []struct {
		want string
		ok   bool
	}{
		{"embeddinggemma", true},
		{"embeddinggemma:latest", true},
		{"qwen3-embedding:0.6b", true},
		{"qwen3-embedding", false}, // untagged name only matches :latest
		{"qwen3-embedding:4b", false},
		{"embedding", false},
		{"", false},
	}
	for _, c := range cases {
		if got := hasModel(names, c.want); got != c.ok {
			t.Errorf("hasModel(%q) = %v, want %v", c.want, got, c.ok)
		}
	}
	if hasModel(nil, "embeddinggemma") {
		t.Error("empty list matched")
	}
}

func TestCheckCorpusRefusesOtherCorpus(t *testing.T) {
	if err := checkCorpus(options{corpus: corpus.Dir()}); err != nil {
		t.Errorf("fixture refused: %v", err)
	}
	if err := checkCorpus(options{corpus: t.TempDir()}); err == nil {
		t.Error("a non-fixture corpus was accepted")
	}
}

func TestMappingRecallKnownEdges(t *testing.T) {
	ctx := context.Background()
	b, err := buildIndex(ctx, options{config: "baseline", corpus: corpus.Dir()}, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := api.OpenExisting(b.root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	edges := []corpus.TruthEdge{
		{Src: "BRD-002", Dst: "BRD-003", Kind: "parent_of"},      // real, default kind: found
		{Src: "BRD-003", Dst: "BRD-002", Kind: "parent_of"},      // direction reversed: not found
		{Src: "BRD-002", Dst: "BRD-003", Kind: "implements"},     // wrong kind: not found
		{Src: "NOPE-1", Dst: "BRD-003", Kind: "parent_of"},       // unknown source: not found
		{Src: "BRD-013", Dst: "BRD-014", Kind: "conflicts_with"}, // real, only with all kinds
		{Src: "BRD-025", Dst: "BRD-099", Kind: "depends_on"},     // real, dangling end, only with all kinds
	}
	all := mappingRecall(ctx, st, edges, allEdgeKinds)
	if all.Total != 6 || all.Found != 3 || !near(all.Recall, 0.5) {
		t.Errorf("all kinds: %+v, want 3/6", all)
	}
	def := mappingRecall(ctx, st, edges, nil)
	if def.Total != 6 || def.Found != 1 || !near(def.Recall, round6(1.0/6)) {
		t.Errorf("default kinds: %+v, want 1/6", def)
	}
	truth, _ := corpus.LoadTruth()
	if full := mappingRecall(ctx, st, truth.Edges, allEdgeKinds); full.Found != full.Total || full.Total != len(truth.Edges) {
		t.Errorf("truth edges, all kinds: %+v", full)
	}
}

func TestNonIDMetricsExcludeExactIDAndFiltered(t *testing.T) {
	qs := []Query{
		{Q: "BRD-1", Category: "exact_id", ExpectTop: []string{"BRD-1"}},
		{Q: "w", Category: "keyword", ExpectTop: []string{"K"}},
		{Q: "p", Category: "semantic", ExpectTop: []string{"S"}},
		{Q: "status:x", Category: "filtered", ExpectTop: []string{"F"}},
	}
	rk := [][]string{{"BRD-1"}, {"K"}, {"x", "S"}, {"F"}}
	var res Result
	fillMetrics(&res, qs, rk, nil)
	if res.MetricsNonID.Queries != 2 || !near(res.MetricsNonID.MRRAt10, 0.75) || res.MetricsNonID.ExactIDQueries != 0 {
		t.Errorf("non-ID = %+v", res.MetricsNonID)
	}
	if res.MetricsUnfiltered.Queries != 3 || res.MetricsAll.Queries != 4 {
		t.Errorf("unfiltered %d all %d", res.MetricsUnfiltered.Queries, res.MetricsAll.Queries)
	}
}

func TestQueriesRunnerSkippedNotCounted(t *testing.T) {
	qs := []Query{{Q: "a", Category: "keyword", ExpectTop: []string{"A"}}, {Q: "f", Category: "filtered", ExpectTop: []string{"F"}}}
	var res Result
	fillMetrics(&res, qs, [][]string{{"A"}, nil}, []string{"", "no filter language"})
	if res.MetricsAll != nil || res.MetricsUnfiltered.Queries != 1 || res.PerQuery[1].Skipped == "" {
		t.Errorf("%+v", res)
	}
}
