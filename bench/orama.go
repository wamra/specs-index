package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// runOrama runs config C: Orama through bench/orama/run.mjs. It checks Node,
// the installed dependency and the Ollama model first; anything missing gives
// a "not run" result with what was observed. Nothing is installed or pulled.
func runOrama(o options) (Result, error) {
	model := orDefault(o.model, qwenModel)
	node, err := exec.LookPath("node")
	if err != nil {
		return notRun(o, "not run: node is not on PATH"), nil
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(oramaScript), "node_modules", "@orama", "orama", "package.json")); err != nil {
		return notRun(o, "not run: @orama/orama is not installed; run `npm install` in bench/orama"), nil
	}
	names, err := ollamaModels(o.ollama)
	if err != nil {
		return notRun(o, fmt.Sprintf("not run: Ollama at %s did not answer /api/tags: %v", o.ollama, err)), nil
	}
	if !hasModel(names, model) {
		return notRun(o, fmt.Sprintf("not run: Ollama at %s is up but model %q is not listed (listed: %s); no model is pulled by this tool", o.ollama, model, strings.Join(names, ", "))), nil
	}
	qs, err := LoadQueries(o.queries)
	if err != nil {
		return Result{}, err
	}
	tmp, err := os.MkdirTemp("", "specs-index-bench-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(tmp)
	b, err := buildIndex(context.Background(), o, tmp, false)
	if err != nil {
		return Result{}, err
	}
	chunksPath := filepath.Join(tmp, "chunks.json")
	if err := exportChunks(b.dbPath, chunksPath); err != nil {
		return Result{}, err
	}
	outPath := filepath.Join(tmp, "orama-out.json")
	cmd := exec.Command(node, "run.mjs", "--chunks", chunksPath, "--queries", mustAbs(o.queries), "--out", outPath,
		"--model", model, "--ollama", o.ollama, "--repeats", fmt.Sprint(o.repeats), "--warmup", fmt.Sprint(o.warmup))
	cmd.Dir = filepath.Dir(oramaScript)
	var combined bytes.Buffer
	cmd.Stdout, cmd.Stderr = &combined, &combined
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("node run.mjs failed: %v\n%s", err, combined.String())
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		return Result{}, err
	}
	var no struct {
		OramaVersion    string              `json:"orama_version"`
		NodeVersion     string              `json:"node_version"`
		Dims            int                 `json:"dims"`
		EmbedSeconds    float64             `json:"embed_seconds"`
		IndexSeconds    float64             `json:"index_seconds"`
		SerializedBytes int64               `json:"serialized_bytes"`
		Latencies       []float64           `json:"latencies_ms"`
		Rankings        map[string][]string `json:"rankings"`
		Skipped         map[string]string   `json:"skipped"`
	}
	if err := json.Unmarshal(raw, &no); err != nil {
		return Result{}, err
	}
	res := newResult(o)
	res.Machine.NodeVersion = no.NodeVersion
	res.Corpus = &CorpusInfo{Documents: b.docs, Entities: b.entities, Chunks: b.chunks, Queries: len(qs)}
	res.Embedder = &EmbedderInfo{Provider: "ollama", Model: model, Dims: no.Dims}
	res.SearchMode, res.Repeats, res.Warmup = "orama hybrid (similarity 0, limit 10)", o.repeats, o.warmup
	res.LatencyNote = "wall time inside Node of query embedding via Ollama plus Orama search, per query; filtered queries are not run"
	res.Index = &IndexInfo{SizeBytes: no.SerializedBytes, SizeNote: "byte length of JSON.stringify(save(db)) of the Orama index (not comparable to an SQLite file)", ParseSeconds: round6(b.parseSecs), EmbedSeconds: round6(no.EmbedSeconds), ChunksEmbedded: b.chunks}
	lat := summarizeLatency(no.Latencies)
	res.Latency = &lat
	rankings := make([][]string, len(qs))
	skipped := make([]string, len(qs))
	for i, q := range qs {
		if why, ok := no.Skipped[q.Q]; ok {
			skipped[i] = why
			continue
		}
		r, ok := no.Rankings[q.Q]
		if !ok {
			return Result{}, fmt.Errorf("node output has no ranking for query %q", q.Q)
		}
		rankings[i] = r
	}
	fillMetrics(&res, qs, rankings, skipped)
	res.Mapping = &MappingRecall{Note: "not applicable: the Orama harness stores no edges and has no trace"}
	zero := 0.0
	res.EmbeddingCost = &Cost{USD: &zero, Note: "local model, no per-token cost"}
	setRSS(&res, cmd.ProcessState, "the Node process only (embedding, Orama index and all queries); the Go side that exports the chunks is not included")
	res.Notes = append(res.Notes, "orama "+no.OramaVersion+"; chunk texts embedded and queries embedded without any model prefix")
	return res, nil
}

func mustAbs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}
