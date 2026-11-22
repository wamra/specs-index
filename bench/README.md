# bench

Benchmark runner for specs-index (spec section 7). See `BENCHMARKS.md` for the measured results and the exact commands.

- `runner.go`, `metrics.go`, `orama.go`, `rss_*.go`: the runner (`go run ./bench --config A|B|C|baseline --out FILE`). Run it from the repository root.
- `queries.json`: labelled queries (`exact_id`, `keyword`, `semantic`, `filtered`); includes every query of `testdata/corpus/truth.json`. `go test ./bench/...` checks that every expected key exists in the corpus.
- `orama/`: Node harness for config C (`npm install` there first; `node_modules/` is git-ignored).
- `results/`: one JSON per run; the numbers in `BENCHMARKS.md` come from these files.

Config A calls Bedrock and needs `--allow-cloud`. `--price-per-1k-tokens` has no default. Runs build their index in a temporary directory and never touch a real index. `go test ./bench/...` needs no Ollama, Node or network.
