package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
)

// Categories are the query kinds in queries.json.
var Categories = []string{"exact_id", "keyword", "semantic", "filtered"}

// Query is one labelled query. ExpectTop lists the entity keys that are
// relevant; ExpectAbsent lists keys that must not be returned at all.
type Query struct {
	Q            string   `json:"q"`
	Category     string   `json:"category"`
	ExpectTop    []string `json:"expect_top"`
	ExpectAbsent []string `json:"expect_absent,omitempty"`
}

// LoadQueries reads queries.json ({"queries": [...]}).
func LoadQueries(path string) ([]Query, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		Queries []Query `json:"queries"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Queries, nil
}

// Metrics are the retrieval metrics over a set of queries. Recall@5 is the
// mean over queries of (relevant keys in the top 5) / (relevant keys). MRR@10
// is the mean reciprocal rank of the first relevant key within the top 10.
// ExactIDAccuracyAt1 is the share of exact_id queries whose first result is the
// expected key. AbsentViolations counts queries that returned an expect_absent
// key anywhere in their ranking.
type Metrics struct {
	Queries            int                `json:"queries"`
	RecallAt5          float64            `json:"recall_at_5"`
	MRRAt10            float64            `json:"mrr_at_10"`
	ExactIDQueries     int                `json:"exact_id_queries"`
	ExactIDAccuracyAt1 float64            `json:"exact_id_accuracy_at_1"`
	AbsentViolations   int                `json:"absent_violations"`
	PerCategory        map[string]Metrics `json:"per_category,omitempty"`
}

// dedupe drops repeated keys, keeping the first occurrence (an entity can have
// more than one chunk or hit).
func dedupe(keys []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func recallAtK(expect, ranked []string, k int) float64 {
	if len(expect) == 0 {
		return 0
	}
	if len(ranked) > k {
		ranked = ranked[:k]
	}
	hit := 0
	for _, e := range expect {
		for _, r := range ranked {
			if r == e {
				hit++
				break
			}
		}
	}
	return float64(hit) / float64(len(expect))
}

func reciprocalRank(expect, ranked []string, k int) float64 {
	if len(ranked) > k {
		ranked = ranked[:k]
	}
	for i, r := range ranked {
		for _, e := range expect {
			if r == e {
				return 1 / float64(i+1)
			}
		}
	}
	return 0
}

func summarizeOne(qs []Query, rankings [][]string) Metrics {
	var m Metrics
	m.Queries = len(qs)
	var exactHits int
	for i, q := range qs {
		r := dedupe(rankings[i])
		m.RecallAt5 += recallAtK(q.ExpectTop, r, 5)
		m.MRRAt10 += reciprocalRank(q.ExpectTop, r, 10)
		if q.Category == "exact_id" {
			m.ExactIDQueries++
			if len(r) > 0 && len(q.ExpectTop) > 0 && r[0] == q.ExpectTop[0] {
				exactHits++
			}
		}
		for _, a := range q.ExpectAbsent {
			if contains(r, a) {
				m.AbsentViolations++
				break
			}
		}
	}
	if m.Queries > 0 {
		m.RecallAt5 /= float64(m.Queries)
		m.MRRAt10 /= float64(m.Queries)
	}
	if m.ExactIDQueries > 0 {
		m.ExactIDAccuracyAt1 = float64(exactHits) / float64(m.ExactIDQueries)
	}
	m.RecallAt5, m.MRRAt10, m.ExactIDAccuracyAt1 = round6(m.RecallAt5), round6(m.MRRAt10), round6(m.ExactIDAccuracyAt1)
	return m
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func round6(f float64) float64 { return math.Round(f*1e6) / 1e6 }

// Summarize computes the metrics of rankings (one per query, same order),
// overall and per category.
func Summarize(qs []Query, rankings [][]string) Metrics {
	m := summarizeOne(qs, rankings)
	m.PerCategory = map[string]Metrics{}
	for _, c := range Categories {
		var cq []Query
		var cr [][]string
		for i, q := range qs {
			if q.Category == c {
				cq, cr = append(cq, q), append(cr, rankings[i])
			}
		}
		if len(cq) > 0 {
			m.PerCategory[c] = summarizeOne(cq, cr)
		}
	}
	return m
}

// Percentile returns the nearest-rank p-th percentile (0 < p <= 100) of the
// ascending-sorted samples, or 0 when there are none.
func Percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// Latency summarizes per-query timings in milliseconds.
type Latency struct {
	Samples int     `json:"samples"`
	P50     float64 `json:"p50_ms"`
	P95     float64 `json:"p95_ms"`
}

func summarizeLatency(ms []float64) Latency {
	s := append([]float64(nil), ms...)
	sort.Float64s(s)
	return Latency{Samples: len(s), P50: round6(Percentile(s, 50)), P95: round6(Percentile(s, 95))}
}
