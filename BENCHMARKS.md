# Benchmarks

Measured by `go run ./bench` (and `bench/orama/run.mjs` for config C); every figure below is copied from a results file in `bench/results/`. Nothing here was estimated.

- Date: 2026-11-22
- Machine: Apple Silicon desktop, 128 GB RAM (darwin/arm64, 20 logical CPUs, go1.26.0, Node v23.6.1)
- Corpus: the small synthetic fixture `testdata/corpus` (11 documents, 88 entities, 88 chunks), 53 labelled queries in `bench/queries.json` (14 exact-ID, 16 keyword, 14 paraphrase/semantic, 9 filtered). **The corpus is tiny and invented, and the semantic queries were labelled by hand, so the results are indicative only.** Recall@5 near 1.0 on 88 chunks says little about a real corpus.
- Embedding models served by a local Ollama (embeddinggemma; qwen3-embedding:0.6b). Orama is @orama/orama 3.1.18.
- Latency: p50 / p95 of every query over 1 warm-up pass and 20 timed passes (B and baselines: 1060 samples; C: 880 samples because filtered queries are not run). In-process, includes the live query-embedding call to Ollama for B and C.
- Peak RSS: `ru_maxrss` of the child process that ran the workload. For B and baselines it covers index build, embedding and all queries; for C it is the Node process only.

## Results (the 44 unfiltered queries, the only set all configurations can answer)

| Configuration | Recall@5 | MRR@10 | Exact-ID Acc@1 | Mapping recall | Latency p50 / p95 (ms) | Peak RSS | Index size | Embedding cost (USD) |
|---|---|---|---|---|---|---|---|---|
| Baseline: SQLite + FTS5 only (`--mode fts`, no embedding) | 0.341 | 0.341 | 0.000 | 129/129 = 1.000 | 0.2 / 0.4 | 24.8 MiB | 276 KiB | 0 |
| Baseline: no embedder, `auto` mode (exact-ID + FTS5) | 0.659 | 0.659 | 1.000 | 129/129 = 1.000 | 0.2 / 0.4 | 24.7 MiB | 276 KiB | 0 |
| A: SQLite + FTS5 + vec1, Titan Text V2 | not run | not run | not run | not run | not run | not run | not run | not computed |
| B: SQLite + FTS5 + vec1, EmbeddingGemma (hybrid) | 1.000 | 0.951 | 1.000 | 129/129 = 1.000 | 58.4 / 104.1 | 28.1 MiB | 572 KiB | 0 |
| C: Orama, Qwen3-Embedding 0.6B (hybrid) | 0.818 | 0.762 | 0.929 | n/a | 46.8 / 83.5 | 117.9 MiB | 3098 KiB | 0 |

Recall@5 / MRR@10 per category:

| Configuration | exact_id (14) | keyword (16) | semantic (14) | filtered (9) |
|---|---|---|---|---|
| Baseline fts | 0.000 / 0.000 | 0.938 / 0.938 | 0.000 / 0.000 | 1.000 / 0.944 |
| Baseline auto | 1.000 / 1.000 | 0.938 / 0.938 | 0.000 / 0.000 | 1.000 / 0.944 |
| B | 1.000 / 1.000 | 1.000 / 0.958 | 1.000 / 0.893 | 1.000 / 0.926 |
| C | 1.000 / 0.964 | 1.000 / 0.953 | 0.429 / 0.341 | not run (no filter language) |

Notes on reading the table:

- **Config A: not run, needs the owner's approval for a cloud call (`--allow-cloud`).** The implementation exists (`go run ./bench --config A --allow-cloud --out bench/results/A.json`, optionally `--region`, `--profile`); `bench/results/A.json` records the refusal. Embedding cost: not computed. The price is a parameter (`--price-per-1k-tokens`, no default), and `internal/provider` does not report the tokens consumed, so even with a price the cost stays "not computed" rather than being estimated.
- **Mapping recall** is the share of the 129 truth edges that `trace` returns one hop from the edge's source (explicit edges, both directions, all six kinds). It does not depend on the embedder, so it is the same for every Go configuration. Orama has no graph, so C is n/a.
- **Index size** of C is the byte length of the JSON from Orama's `save()`; the others are SQLite files after a WAL checkpoint. They are not comparable.
- **Filtered queries** (`status:`, `type:`, `tag:`, `has:`) need the product's query language; the Orama harness has none, so C skips them and the headline table excludes them for every configuration. Baseline and B scores on filtered queries are in the last column.
- The baseline in `fts` mode skips the exact-ID lookup, so its exact-ID accuracy is plain full-text behaviour. B runs `hybrid`, which includes the exact-ID lookup, so the difference between the baseline rows and B mixes the embeddings and that lookup; the `auto` baseline row is the like-for-like comparison for ID handling.
- C embeds chunk and query text without any model prefix (the Go tool applies the model's prefixes where it knows them); Orama's hybrid search ran with `similarity: 0`, `limit: 10` and otherwise default settings. Different defaults would move C's numbers.
- Embedding time for the 88 chunks: B 2.8 s, C 1.8 s (local Ollama, includes model load if cold).

## Reproduce

```sh
go run ./bench --config baseline --machine "<description>" --out bench/results/baseline-fts.json
go run ./bench --config baseline --mode auto --machine "<description>" --out bench/results/baseline-auto.json
go run ./bench --config B --machine "<description>" --out bench/results/B.json     # needs Ollama + embeddinggemma
(cd bench/orama && npm install)
go run ./bench --config C --machine "<description>" --out bench/results/C.json     # needs Node, Ollama + qwen3-embedding:0.6b
go run ./bench --config A --allow-cloud --out bench/results/A.json                 # cloud call: owner only
```

Run from the repository root. Each run builds its index in a temporary directory with `SPECS_INDEX_DB` set there and never touches a real index.
