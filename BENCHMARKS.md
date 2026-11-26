# Benchmarks

Measured by `go run ./bench` (and `bench/orama/run.mjs` for config C); every figure below is copied from a results file in `bench/results/`. Nothing here was estimated.

- Date: 2026-11-22 (UTC)
- Machine: Apple Silicon desktop, 128 GB RAM (darwin/arm64, 20 logical CPUs, go1.26.0, Node v23.6.1)
- Corpus: the small synthetic fixture `testdata/corpus` (11 documents, 88 entities, 88 chunks), 53 labelled queries in `bench/queries.json` (14 exact-ID, 16 keyword, 14 paraphrase/semantic, 9 filtered). **The corpus is tiny and invented, so the results are indicative only.**
- Embedding models served by a local Ollama (embeddinggemma; qwen3-embedding:0.6b). Orama is @orama/orama 3.1.18.
- Latency: p50 / p95 of every query over 1 warm-up pass and 20 timed passes (B and baselines: 1060 samples; C: 880 samples because filtered queries are not run). In-process, includes the live query-embedding call to Ollama for B and C.
- Peak RSS: `ru_maxrss` of the child process that ran the workload. For B and baselines it covers index build, embedding and all queries; for C it is the Node process only.

## What these results can and cannot show

- **B and C differ in three things at once**: the engine (SQLite + FTS5 + vec1 vs Orama), the embedding model (EmbeddingGemma vs Qwen3-Embedding 0.6B) and exact-ID handling. specs-index has a built-in exact-ID lookup; the Orama harness has none. So Acc@1 and the exact_id row are **not** a like-for-like comparison of models, and a B-versus-C difference cannot be attributed to the model or to the engine.
- **14 of the 44 headline queries are the entity key verbatim** (for example `BRD-006`), which any ID lookup solves. The second table below drops them and the filtered queries: it covers the 30 keyword and semantic queries only.
- **The semantic labels were written by hand by the tool's author**, not by independent annotators.
- **What cannot be concluded:** the corpus is 88 synthetic chunks, so a Recall@5 of 1.000 is a ceiling that cannot separate good retrievers from better ones; nothing here says how any configuration behaves on a real corpus, and nothing says which model is better in general.

## Results: 44 unfiltered queries (the set every configuration can answer)

| Configuration | Recall@5 | MRR@10 | Exact-ID Acc@1 | Mapping recall (all six edge kinds) | Latency p50 / p95 (ms) | Peak RSS | Index size | Embedding cost (USD) |
|---|---|---|---|---|---|---|---|---|
| Baseline: SQLite + FTS5 only (`--mode fts`, no embedding) | 0.341 | 0.341 | 0.000 | 129/129 = 1.000 | 0.2 / 0.4 | 25.4 MiB | 276 KiB | 0 |
| Baseline: no embedder, `auto` mode (exact-ID + FTS5) | 0.659 | 0.659 | 1.000 | 129/129 = 1.000 | 0.2 / 0.4 | 25.1 MiB | 276 KiB | 0 |
| A: SQLite + FTS5 + vec1, Titan Text V2 | not run | not run | not run | not run | not run | not run | not run | not computed |
| B: SQLite + FTS5 + vec1, EmbeddingGemma (hybrid) | 1.000 | 0.951 | 1.000 | 129/129 = 1.000 | 58.8 / 118.7 | 28.2 MiB | 572 KiB | 0 |
| C: Orama, Qwen3-Embedding 0.6B (hybrid) | 0.818 | 0.762 | 0.929 | n/a | 49.6 / 68.9 | 119.4 MiB | 3099 KiB | 0 |

## Results: 30 keyword and semantic queries (no exact-ID, no filtered)

| Configuration | Recall@5 | MRR@10 |
|---|---|---|
| Baseline fts | 0.500 | 0.500 |
| Baseline auto | 0.500 | 0.500 |
| A | not run | not run |
| B | 1.000 | 0.928 |
| C | 0.733 | 0.668 |

Recall@5 / MRR@10 per category (same runs):

| Configuration | exact_id (14) | keyword (16) | semantic (14) | filtered (9) |
|---|---|---|---|---|
| Baseline fts | 0.000 / 0.000 | 0.938 / 0.938 | 0.000 / 0.000 | 1.000 / 0.944 |
| Baseline auto | 1.000 / 1.000 | 0.938 / 0.938 | 0.000 / 0.000 | 1.000 / 0.944 |
| B | 1.000 / 1.000 | 1.000 / 0.958 | 1.000 / 0.893 | 1.000 / 0.926 |
| C | 1.000 / 0.964 | 1.000 / 0.953 | 0.429 / 0.341 | not run (no filter language) |

## Notes

- **Config A: not run, because it makes cloud calls and needs `--allow-cloud`.** `bench/results/A.json` records the refusal; the runner starts no process and makes no network call without the flag (tested). **With `--allow-cloud` it makes paid Amazon Bedrock calls**: Titan takes one text per request, so one call per chunk (88) plus at most one per query embedded per pass; the default for A is 1 warm-up and 1 timed pass (`--repeats`), so at most 88 + 53 x 2 = 194 calls (fewer, since pure-ID and filtered queries skip the vector side). Raising `--repeats` raises the call count in proportion. Embedding cost: not computed. The price is a parameter (`--price-per-1k-tokens`, no default) and `internal/provider` does not report consumed tokens, so even with a price the cost stays "not computed" rather than being estimated.
- **Mapping recall.** Plain `specs-index trace` follows `parent_of`, `implements` and `verified_by` by design. The 129/129 = 1.000 figure in the table uses all six edge kinds; with trace's default kinds the measured result is 119/129 = 0.922 (`mapping_recall_default_kinds` in the results files; the misses are the `depends_on`, `conflicts_with` and `references` edges). The check compares `trace` with the parser's own explicit edges of the synthetic corpus (`truth.json`), so it validates the trace plumbing, not the quality of edge extraction on real documents. Direction is checked: a truth edge counts only when trace reports the far end with the same kind and "forward" from the edge's source. It does not depend on the embedder, so it is the same for every Go configuration. Orama has no graph, so C is n/a.
- **Index size** of C is the byte length of the JSON from Orama's `save()`; the others are SQLite files after a WAL checkpoint. They are not comparable.
- **Filtered queries** (`status:`, `type:`, `tag:`, `has:`) need the product's query language; the Orama harness has none, so C skips them and both headline tables exclude them for every configuration. Baseline and B scores on filtered queries are in the last column of the per-category table.
- The baseline in `fts` mode skips the exact-ID lookup, so its exact-ID accuracy is plain full-text behaviour. B runs `hybrid`, which includes the exact-ID lookup; the `auto` baseline row is the like-for-like comparison for ID handling.
- C embeds chunk and query text without any model prefix (the Go tool applies the model's prefixes where it knows them); Orama's hybrid search ran with `similarity: 0`, `limit: 10` and otherwise default settings. Different defaults would move C's numbers.
- `--corpus` accepts only the fixture corpus: queries and the mapping truth are labelled against it, and the runner refuses any other path.
- Embedding time for the 88 chunks: B 1.2 s, C 4.3 s (local Ollama, includes model load if cold).

## Reproduce

```sh
go run ./bench --config baseline --machine "<description>" --out bench/results/baseline-fts.json
go run ./bench --config baseline --mode auto --machine "<description>" --out bench/results/baseline-auto.json
go run ./bench --config B --machine "<description>" --out bench/results/B.json     # needs Ollama + embeddinggemma
(cd bench/orama && npm install)
go run ./bench --config C --machine "<description>" --out bench/results/C.json     # needs Node, Ollama + qwen3-embedding:0.6b
go run ./bench --config A --allow-cloud --out bench/results/A.json                 # PAID Bedrock calls
```

Run from the repository root. Each run builds its index in a temporary directory with `SPECS_INDEX_DB` set there and never touches a real index.
