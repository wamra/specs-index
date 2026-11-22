// Command bench measures retrieval quality and speed of the three benchmark
// configurations (spec section 7) plus a no-embedding baseline, and writes one
// results JSON per run. Nothing here touches a real index: every run builds
// its own index in a temporary directory.
//
//	go run ./bench --config baseline --out bench/results/baseline-fts.json
//	go run ./bench --config B        --out bench/results/B.json
//	go run ./bench --config C        --out bench/results/C.json
//	go run ./bench --config A --allow-cloud --out bench/results/A.json   (owner only)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wamra/specs-index/internal/api"
	"github.com/wamra/specs-index/internal/config"
	"github.com/wamra/specs-index/internal/corpus"
	"github.com/wamra/specs-index/internal/store"
)

// Result statuses.
const (
	StatusOK     = "ok"
	StatusNotRun = "not_run"
)

// Machine describes where the run happened. Description is free text from
// --machine; nothing else identifies the host.
type Machine struct {
	Description string `json:"description"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	CPUs        int    `json:"cpus"`
	GoVersion   string `json:"go_version"`
	NodeVersion string `json:"node_version,omitempty"`
}

// CorpusInfo counts what was indexed.
type CorpusInfo struct {
	Documents int `json:"documents"`
	Entities  int `json:"entities"`
	Chunks    int `json:"chunks"`
	Queries   int `json:"queries"`
}

// EmbedderInfo names the embedding model of a run.
type EmbedderInfo struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Dims        int    `json:"dims,omitempty"`
}

// IndexInfo describes the built index. SizeBytes is the SQLite file for the Go
// configurations and the JSON size of Orama's save() for config C.
type IndexInfo struct {
	SizeBytes      int64   `json:"size_bytes"`
	SizeNote       string  `json:"size_note,omitempty"`
	ParseSeconds   float64 `json:"parse_seconds,omitempty"`
	EmbedSeconds   float64 `json:"embed_seconds,omitempty"`
	ChunksEmbedded int     `json:"chunks_embedded,omitempty"`
}

// MappingRecall is the share of truth edges that trace finds one hop from the
// edge's source (either direction, explicit edges only).
type MappingRecall struct {
	Found  int     `json:"found"`
	Total  int     `json:"total"`
	Recall float64 `json:"recall"`
	Note   string  `json:"note,omitempty"`
}

// Cost is the embedding cost. Local models cost 0; a cloud cost needs a token
// count and a price, and is otherwise "not computed" with the reason in Note.
type Cost struct {
	USD  *float64 `json:"usd,omitempty"`
	Note string   `json:"note"`
}

// QueryResult is the ranking returned for one query (entity keys, best first).
type QueryResult struct {
	Q        string   `json:"q"`
	Category string   `json:"category"`
	Skipped  string   `json:"skipped,omitempty"`
	Ranking  []string `json:"ranking"`
}

// Result is one results file.
type Result struct {
	Config      string        `json:"config"`
	Label       string        `json:"label"`
	Status      string        `json:"status"`
	Reason      string        `json:"reason,omitempty"`
	Date        string        `json:"date"`
	Machine     Machine       `json:"machine"`
	Corpus      *CorpusInfo   `json:"corpus,omitempty"`
	Embedder    *EmbedderInfo `json:"embedder,omitempty"`
	SearchMode  string        `json:"search_mode,omitempty"`
	Repeats     int           `json:"repeats,omitempty"`
	Warmup      int           `json:"warmup_passes,omitempty"`
	LatencyNote string        `json:"latency_note,omitempty"`
	// MetricsAll covers every query; MetricsUnfiltered excludes the "filtered"
	// category (config C has no filter language, so only this one is comparable).
	MetricsAll        *Metrics `json:"metrics_all,omitempty"`
	MetricsUnfiltered *Metrics `json:"metrics_unfiltered,omitempty"`
	// MetricsNonID covers only the keyword and semantic queries: no filtered
	// query and no query that is an entity key verbatim.
	MetricsNonID *Metrics `json:"metrics_non_id,omitempty"`
	Latency      *Latency `json:"latency,omitempty"`
	// Mapping uses all six edge kinds; MappingDefaultKinds uses trace's default
	// kinds (parent_of, implements, verified_by).
	Mapping             *MappingRecall `json:"mapping_recall,omitempty"`
	MappingDefaultKinds *MappingRecall `json:"mapping_recall_default_kinds,omitempty"`
	Index               *IndexInfo     `json:"index,omitempty"`
	PeakRSSBytes        *int64         `json:"peak_rss_bytes,omitempty"`
	PeakRSSNote         string         `json:"peak_rss_note,omitempty"`
	EmbeddingCost       *Cost          `json:"embedding_cost,omitempty"`
	PerQuery            []QueryResult  `json:"per_query,omitempty"`
	Notes               []string       `json:"notes,omitempty"`
}

type options struct {
	config, corpus, queries, out string
	repeats, warmup              int
	allowCloud                   bool
	price                        string
	machine, mode                string
	ollama, model                string
	region, profile              string
	child                        bool
}

const (
	titanModel  = "amazon.titan-embed-text-v2:0"
	gemmaModel  = "embeddinggemma"
	qwenModel   = "qwen3-embedding:0.6b"
	defaultOll  = "http://127.0.0.1:11434"
	searchK     = 10
	oramaScript = "bench/orama/run.mjs"
)

func parseFlags(args []string, errw io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	fs.SetOutput(errw)
	fs.StringVar(&o.config, "config", "", "A, B, C or baseline")
	fs.StringVar(&o.corpus, "corpus", "testdata/corpus", "corpus directory (docs/ and features/ are indexed)")
	fs.StringVar(&o.queries, "queries", "bench/queries.json", "labelled queries")
	fs.StringVar(&o.out, "out", "", "results file to write (required)")
	fs.IntVar(&o.repeats, "repeats", 20, "timed passes over all queries (config A defaults to 1: each query pass is a paid call per query)")
	fs.IntVar(&o.warmup, "warmup", 1, "untimed passes over all queries before timing")
	fs.BoolVar(&o.allowCloud, "allow-cloud", false, "permit config A to call Bedrock (costs money)")
	fs.StringVar(&o.price, "price-per-1k-tokens", "", "USD per 1000 tokens for config A; no default")
	fs.StringVar(&o.machine, "machine", "", "generic machine description for the results file")
	fs.StringVar(&o.mode, "mode", "", "search mode (default: hybrid for A/B, fts for baseline)")
	fs.StringVar(&o.ollama, "ollama-url", defaultOll, "Ollama base URL (loopback)")
	fs.StringVar(&o.model, "model", "", "override the embedding model")
	fs.StringVar(&o.region, "region", "", "AWS region for config A")
	fs.StringVar(&o.profile, "profile", "", "AWS profile for config A")
	fs.BoolVar(&o.child, "child", false, "internal: run the measured workload in this process")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	repeatsSet := false
	fs.Visit(func(f *flag.Flag) { repeatsSet = repeatsSet || f.Name == "repeats" })
	if o.config == "A" && !repeatsSet {
		o.repeats = 1 // every timed query pass is a paid Bedrock call per query
	}
	if !slices.Contains([]string{"A", "B", "C", "baseline"}, o.config) {
		return o, fmt.Errorf("--config must be A, B, C or baseline")
	}
	if o.out == "" {
		return o, errors.New("--out is required")
	}
	if o.repeats < 1 || o.warmup < 0 {
		return o, errors.New("--repeats must be >= 1 and --warmup >= 0")
	}
	if o.price != "" {
		if p, err := strconv.ParseFloat(o.price, 64); err != nil || p < 0 {
			return o, fmt.Errorf("--price-per-1k-tokens %q: want a non-negative number", o.price)
		}
	}
	return o, nil
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errw io.Writer) int {
	o, err := parseFlags(args, errw)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(errw, "bench:", err)
		}
		return 2
	}
	if err := checkCorpus(o); err != nil {
		fmt.Fprintln(errw, "bench:", err)
		return 2
	}
	var res Result
	switch {
	case o.child:
		res, err = runGoConfig(context.Background(), o)
	case o.config == "C":
		res, err = runOrama(o)
	default:
		res, err = runViaChild(o, args)
	}
	if err != nil {
		fmt.Fprintln(errw, "bench:", err)
		return 1
	}
	if err := writeResult(o.out, res); err != nil {
		fmt.Fprintln(errw, "bench:", err)
		return 1
	}
	if !o.child {
		fmt.Fprintf(out, "%s: %s%s (wrote %s)\n", res.Config, res.Status, ifNotEmpty(": ", res.Reason), o.out)
	}
	return 0
}

func ifNotEmpty(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}

func writeResult(path string, r Result) error {
	b, err := json.MarshalIndent(r, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func newResult(o options) Result {
	label := map[string]string{
		"A":        "A: SQLite + FTS5 + vec1, Amazon Titan Text V2 (Bedrock)",
		"B":        "B: SQLite + FTS5 + vec1, EmbeddingGemma (Ollama)",
		"C":        "C: Orama, Qwen3-Embedding 0.6B (Ollama)",
		"baseline": "baseline: SQLite + FTS5, no embedding",
	}[o.config]
	return Result{
		Config: o.config, Label: label, Status: StatusOK, Date: time.Now().UTC().Format("2006-01-02"),
		Machine: Machine{Description: o.machine, OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), GoVersion: runtime.Version()},
	}
}

func notRun(o options, reason string) Result {
	r := newResult(o)
	r.Status, r.Reason = StatusNotRun, reason
	return r
}

// searchMode picks the mode: hybrid when an embedder is used, fts for the baseline.
func searchMode(o options) string {
	if o.mode != "" {
		return o.mode
	}
	if o.config == "baseline" {
		return "fts"
	}
	return "hybrid"
}

// ---- Ollama probing -------------------------------------------------------

// ollamaModels lists the model names the Ollama server at base reports.
func ollamaModels(base string) ([]string, error) {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(strings.TrimRight(base, "/") + "/api/tags")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /api/tags: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// hasModel reports whether names holds want (an exact name, or want without a
// tag matching a name with any tag).
func hasModel(names []string, want string) bool {
	for _, n := range names {
		if n == want || (!strings.Contains(want, ":") && strings.TrimSuffix(n, ":latest") == want) {
			return true
		}
	}
	return false
}

// ---- parent: run the workload in a child to read its peak RSS -------------

// newCommand starts the measured child; a variable so tests can prove that a
// refused configuration starts nothing.
var newCommand = exec.Command

func runViaChild(o options, args []string) (Result, error) {
	switch o.config {
	case "A":
		if !o.allowCloud {
			return notRun(o, "not run: needs the owner's approval for a cloud call (--allow-cloud); this implementation never calls Bedrock without it"), nil
		}
	case "B":
		model := orDefault(o.model, gemmaModel)
		names, err := ollamaModels(o.ollama)
		if err != nil {
			return notRun(o, fmt.Sprintf("not run: Ollama at %s did not answer /api/tags: %v", o.ollama, err)), nil
		}
		if !hasModel(names, model) {
			return notRun(o, fmt.Sprintf("not run: Ollama at %s is up but model %q is not listed (listed: %s); no model is pulled by this tool", o.ollama, model, strings.Join(names, ", "))), nil
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return Result{}, err
	}
	// The child writes the results file itself; the parent adds the RSS.
	cmd := newCommand(exe, append(slices.Clone(args), "--child")...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("measured child failed: %v\n%s", err, stderr.String())
	}
	b, err := os.ReadFile(o.out)
	if err != nil {
		return Result{}, err
	}
	var res Result
	if err := json.Unmarshal(b, &res); err != nil {
		return Result{}, err
	}
	setRSS(&res, cmd.ProcessState, "the whole measured process: index build, embedding and all queries")
	return res, nil
}

func setRSS(res *Result, ps *os.ProcessState, scope string) {
	if v, ok := peakRSS(ps); ok {
		res.PeakRSSBytes = &v
		res.PeakRSSNote = "ru_maxrss of " + scope
		return
	}
	res.PeakRSSNote = "not measured: process resource usage is unavailable on " + runtime.GOOS
}

func orDefault(s, d string) string {
	if s != "" {
		return s
	}
	return d
}

// ---- index building (shared by the Go configurations and the Orama export) --

type built struct {
	root, dbPath string
	cfg          config.Config
	docs         int
	entities     int
	chunks       int
	parseSecs    float64
	embedSecs    float64
	embedded     int
	fingerprint  string
	sizeBytes    int64
}

// copyCorpus copies docs/ and features/ of src into dst.
func copyCorpus(src, dst string) error {
	for _, d := range []string{"docs", "features"} {
		err := filepath.WalkDir(filepath.Join(src, d), func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(src, p)
			if e.IsDir() {
				return os.MkdirAll(filepath.Join(dst, rel), 0o755)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// embedderConfig returns the explicit configuration of a Go configuration. It
// never reads the user's config file or environment.
func embedderConfig(o options) config.Config {
	switch o.config {
	case "A":
		return config.Config{Provider: config.Providers{Embed: config.Bedrock}, Model: orDefault(o.model, titanModel), Region: o.region, Profile: o.profile, AllowCloud: o.allowCloud}
	case "B":
		return config.Config{Provider: config.Providers{Embed: config.Ollama}, Model: orDefault(o.model, gemmaModel), BaseURL: o.ollama}
	}
	return config.Config{Provider: config.Providers{Embed: config.None}}
}

// buildIndex builds the index of the corpus in tmp (a fresh directory), with
// SPECS_INDEX_DB pointing inside tmp. embedding says whether to embed.
func buildIndex(ctx context.Context, o options, tmp string, embedding bool) (*built, error) {
	root := filepath.Join(tmp, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	if err := copyCorpus(o.corpus, root); err != nil {
		return nil, fmt.Errorf("copy corpus: %w", err)
	}
	dbPath := filepath.Join(tmp, "bench-index.sqlite")
	if err := os.Setenv("SPECS_INDEX_DB", dbPath); err != nil {
		return nil, err
	}
	root, err := api.ResolveRoot(root)
	if err != nil {
		return nil, err
	}
	b := &built{root: root, dbPath: dbPath, cfg: embedderConfig(o)}
	st, err := api.OpenForWrite(root)
	if err != nil {
		return nil, err
	}
	if _, err := api.Add(st, root, root, []string{"docs", "features"}); err != nil {
		st.Close()
		return nil, err
	}
	t0 := time.Now()
	ir, err := api.Index(ctx, st, root, b.cfg, api.IndexOptions{NoEmbed: true})
	if err != nil {
		st.Close()
		return nil, err
	}
	if ir.Failed > 0 {
		st.Close()
		return nil, fmt.Errorf("%d files failed to index: %+v", ir.Failed, ir.Failures)
	}
	b.parseSecs = time.Since(t0).Seconds()
	if embedding {
		t1 := time.Now()
		ir, err = api.Index(ctx, st, root, b.cfg, api.IndexOptions{AllowCloud: o.allowCloud})
		if err != nil {
			st.Close()
			return nil, err
		}
		if ir.Embedding.State != api.EmbedOK {
			st.Close()
			return nil, fmt.Errorf("embedding did not run: state %q: %s", ir.Embedding.State, ir.Embedding.Note)
		}
		b.embedSecs, b.embedded = time.Since(t1).Seconds(), ir.Embedding.Embedded
		b.fingerprint, _ = st.Meta("embed_fingerprint")
	}
	for q, dst := range map[string]*int{
		`SELECT count(*) FROM documents`: &b.docs, `SELECT count(*) FROM entities`: &b.entities, `SELECT count(*) FROM chunks`: &b.chunks,
	} {
		if err := st.DB().QueryRowContext(ctx, q).Scan(dst); err != nil {
			st.Close()
			return nil, err
		}
	}
	if err := st.Close(); err != nil { // checkpoints the WAL, so the file size is complete
		return nil, err
	}
	fi, err := os.Stat(dbPath)
	if err != nil {
		return nil, err
	}
	b.sizeBytes = fi.Size()
	return b, nil
}

// ---- the measured Go workload (A, B, baseline) ----------------------------

func runGoConfig(ctx context.Context, o options) (Result, error) {
	res := newResult(o)
	qs, err := LoadQueries(o.queries)
	if err != nil {
		return res, err
	}
	truth, err := corpus.LoadTruth()
	if err != nil {
		return res, err
	}
	tmp, err := os.MkdirTemp("", "specs-index-bench-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(tmp)
	embedding := o.config != "baseline"
	b, err := buildIndex(ctx, o, tmp, embedding)
	if err != nil {
		return res, err
	}
	res.Corpus = &CorpusInfo{Documents: b.docs, Entities: b.entities, Chunks: b.chunks, Queries: len(qs)}
	res.Index = &IndexInfo{SizeBytes: b.sizeBytes, SizeNote: "SQLite file after WAL checkpoint, includes FTS5 and vec1 tables", ParseSeconds: round6(b.parseSecs), EmbedSeconds: round6(b.embedSecs), ChunksEmbedded: b.embedded}
	if embedding {
		res.Embedder = &EmbedderInfo{Provider: b.cfg.Provider.Embed, Model: b.cfg.Model, Fingerprint: b.fingerprint}
	}
	res.SearchMode, res.Repeats, res.Warmup = searchMode(o), o.repeats, o.warmup
	res.LatencyNote = "wall time of api.Search per query in-process, including the query-embedding call for embedding configurations"

	st, err := api.OpenExisting(b.root)
	if err != nil {
		return res, err
	}
	defer st.Close()
	search := func(q string) ([]string, error) {
		r, err := api.Search(ctx, st, b.root, q, b.cfg, api.SearchOptions{Mode: res.SearchMode, K: searchK, AllowCloud: o.allowCloud})
		if err != nil {
			return nil, err
		}
		if embedding && (r.Notice != "" || len(r.Notes) > 0) {
			return nil, fmt.Errorf("query %q degraded (notice %q, notes %v): refusing to report it as an embedding result", q, r.Notice, r.Notes)
		}
		keys := make([]string, 0, len(r.Hits))
		for _, h := range r.Hits {
			keys = append(keys, h.Key)
		}
		return keys, nil
	}
	rankings := make([][]string, len(qs))
	var samples []float64
	for pass := 0; pass < o.warmup+o.repeats; pass++ {
		for i, q := range qs {
			t0 := time.Now()
			keys, err := search(q.Q)
			d := time.Since(t0)
			if err != nil {
				return res, err
			}
			if pass == 0 {
				rankings[i] = keys
			}
			if pass >= o.warmup {
				samples = append(samples, float64(d.Microseconds())/1000)
			}
		}
	}
	lat := summarizeLatency(samples)
	res.Latency = &lat
	fillMetrics(&res, qs, rankings, nil)

	res.Mapping = mappingRecall(ctx, st, truth.Edges, allEdgeKinds)
	res.Mapping.Note = "trace with all six edge kinds, depth 1, both directions, edge direction checked; compared with the parser's own explicit edges of the synthetic corpus; independent of the embedder"
	res.MappingDefaultKinds = mappingRecall(ctx, st, truth.Edges, nil)
	res.MappingDefaultKinds.Note = "same check with trace's default edge kinds (parent_of, implements, verified_by)"
	res.EmbeddingCost = costFor(o)
	return res, nil
}

// fillMetrics sets the per-query rankings and both metric sets. skipped[i]
// non-empty means query i was not answerable by the configuration.
func fillMetrics(res *Result, qs []Query, rankings [][]string, skipped []string) {
	var allQ, unfQ, nidQ []Query
	var allR, unfR, nidR [][]string
	for i, q := range qs {
		qr := QueryResult{Q: q.Q, Category: q.Category, Ranking: nonNilKeys(rankings[i])}
		if skipped != nil && skipped[i] != "" {
			qr.Skipped = skipped[i]
			res.PerQuery = append(res.PerQuery, qr)
			continue
		}
		res.PerQuery = append(res.PerQuery, qr)
		allQ, allR = append(allQ, q), append(allR, rankings[i])
		if q.Category != "filtered" {
			unfQ, unfR = append(unfQ, q), append(unfR, rankings[i])
			if q.Category != "exact_id" {
				nidQ, nidR = append(nidQ, q), append(nidR, rankings[i])
			}
		}
	}
	if len(allQ) == len(qs) {
		m := Summarize(allQ, allR)
		res.MetricsAll = &m
	}
	m := Summarize(unfQ, unfR)
	res.MetricsUnfiltered = &m
	n := Summarize(nidQ, nidR)
	res.MetricsNonID = &n
}

func nonNilKeys(k []string) []string {
	if k == nil {
		return []string{}
	}
	return k
}

func costFor(o options) *Cost {
	switch o.config {
	case "A":
		if o.price == "" {
			return &Cost{Note: "not computed: no --price-per-1k-tokens given (the price is a parameter with no default)"}
		}
		return &Cost{Note: "not computed: price given, but internal/provider does not report the tokens the embedder consumed, and none is estimated"}
	case "baseline":
		zero := 0.0
		return &Cost{USD: &zero, Note: "no embedding"}
	}
	zero := 0.0
	return &Cost{USD: &zero, Note: "local model, no per-token cost"}
}

// allEdgeKinds are the six spec edge kinds; trace follows only parent_of,
// implements and verified_by unless told otherwise.
var allEdgeKinds = []string{"parent_of", "implements", "verified_by", "depends_on", "conflicts_with", "references"}

// mappingRecall counts the truth edges that api.Trace (depth 1, both
// directions, the given kinds; nil means trace's defaults) reports as a direct
// neighbour of the edge's source with the same kind and the same direction
// (Via "<kind> forward": the source is the edge's source).
func mappingRecall(ctx context.Context, st *store.Store, edges []corpus.TruthEdge, kinds []string) *MappingRecall {
	m := &MappingRecall{Total: len(edges)}
	for _, e := range edges {
		n, err := api.Trace(ctx, st, e.Src, api.TraceOptions{Direction: "both", Depth: 1, Kinds: kinds})
		if err != nil {
			continue // an unknown source counts as not found
		}
		for _, c := range n.Children {
			if c.Key == e.Dst && c.Via == e.Kind+" forward" {
				m.Found++
				break
			}
		}
	}
	if m.Total > 0 {
		m.Recall = round6(float64(m.Found) / float64(m.Total))
	}
	return m
}

// exportChunks writes every chunk as {key, text} (the exact text the Go
// configurations embed, minus the model's document prefix) for the Orama harness.
func exportChunks(dbPath, out string) error {
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	rows, err := st.DB().Query(`SELECT e.key, c.text FROM chunks c JOIN entities e ON e.id = c.entity_id ORDER BY c.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type chunk struct {
		Key  string `json:"key"`
		Text string `json:"text"`
	}
	list := []chunk{}
	for rows.Next() {
		var c chunk
		if err := rows.Scan(&c.Key, &c.Text); err != nil {
			return err
		}
		list = append(list, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return os.WriteFile(out, b, 0o644)
}

// checkCorpus refuses a corpus other than the fixture: the truth edges come
// from the embedded fixture truth.json and queries.json is labelled against
// its keys, so another corpus would silently mismatch both.
func checkCorpus(o options) error {
	a, err1 := filepath.Abs(o.corpus)
	b, err2 := filepath.Abs(corpus.Dir())
	if err1 != nil || err2 != nil {
		return errors.New("cannot resolve the corpus path")
	}
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	if a != b {
		return fmt.Errorf("--corpus %s is not the fixture corpus: queries.json and the mapping truth are labelled against testdata/corpus, so no other corpus is supported", o.corpus)
	}
	return nil
}
