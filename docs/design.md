# specs-index — design

Status: draft written by the agent on 2026-11-21 from the owner's spec
(`specs-index_BRD_FDS_v0.2.docx`) and conversation. Lines marked **(owner)**
are the owner's decisions; **(spec)** lines come from the spec; everything else
is the agent's call and can be overruled. "Not verified" means exactly that.

## Intent

Index Markdown BRD/FDS documents and Gherkin `.feature` files in a project so
Claude Code (and a person at a terminal) can search them, trace
BRD ↔ FDS ↔ scenario links in both directions, and report coverage gaps, with
every answer pointing at the source file and lines.

- **(owner)** Built "just like session-index", listed in `wammy-plugins`.
- **(owner)** Go core with the SQLite team's `vec1` vector extension.
- **(owner)** specs-index indexes feature files, scenarios, tags and step text
  now. Linking steps to step-definition code is a later `code-index` project.
- **(owner)** The agent writes the sample BRD/FDS/feature documents.

## Goals

1. `add` / `index` register sources and build or refresh the index; unchanged
   files are not re-parsed or re-embedded, deleted files leave the index.
2. Search: exact ID first, then full text and vectors; Boolean, exclusion and
   metadata filters (type, status, tag, priority, path).
3. Trace: bounded forward and reverse traversal over typed edges, many-to-many.
4. Coverage: requirements with no FDS, FDS with no scenario, dangling IDs,
   candidate conflicts.
5. Works with no model (IDs, full text, trace, coverage); better with one.
6. Ships as prebuilt binaries inside the plugin: no npm, Python, Docker, WSL.

## Non-goals (v1)

- Writing back to source files (spec section 9: read-only).
- Step → step-definition code linking (later, in code-index).
- A shipped Orama adapter. Orama is a JavaScript library; it appears only as a
  Node benchmark harness (config C).
- Binary signing. The spec asks for signed artifacts; session-index does not
  sign. Listed under Owner decisions.
- Word/PDF sources. Markdown and `.feature` only.

## Anti-goals

- A trace graph that mixes guesses with facts. Explicit edges are
  authoritative; inferred ones are stored and shown as suggestions only and
  never count toward coverage.
- Mixing vectors from different models. The index pins one embedder
  fingerprint; a change means re-embed.
- Silent cloud calls. Bedrock runs only when explicitly enabled.

## Approaches considered

| Approach | Trade-off | Outcome |
|---|---|---|
| Go + vec1 | Reuses session-index patterns and packaging; vec1 is pre-release | **Picked (owner)** |
| Go + sqlite-vec `vec0` | Proven in session-index; not the spec's baseline; pins the old driver and the slow Windows journal | Fallback behind the vector-store interface |
| TypeScript per spec | In-process Orama; per-platform executables with a separate native vector library; nothing shared | Rejected (owner) |

Feature files: specs-index now, code-index later **(owner)**; rejected were
both-at-once, code-index owning features, and never linking to code.

## Source format (the parser contract)

The owner has no corpus, so this format is the agent's call. The fixtures under
`testdata/corpus/` follow it and are the test ground truth and benchmark corpus.

**IDs.** `[A-Z][A-Z0-9]*-\d+` (e.g. `BRD-001`, `FDS-003`, `AC-007`). The prefix
gives the type: `BRD` → requirement, `FDS` → design, `AC` → acceptance
criterion; other prefixes are configurable.

**Markdown, two shapes, both parsed:**

```markdown
---
type: brd
version: 0.2
---
## BRD-004 Hybrid retrieval
Status: approved    Priority: P0
Parent: BRD-001
Depends on: BRD-003

Search must combine exact-ID, full-text and semantic ranking.
```

```markdown
| FDS ID  | Functional design            | Mapped BRDs      |
|---------|------------------------------|------------------|
| FDS-003 | Hybrid retrieval with ...    | BRD-004, BRD-005 |
```

- A heading starting with an ID defines an entity; its body runs to the next
  heading of the same or higher level. Source span = those lines.
- A table whose first column holds IDs defines one entity per row; span = the
  row. A heading entity and a table row with the same ID merge (heading wins
  for body).
- `Key: value` lines directly under the heading, and table columns, become
  fields or edges by name:

| Field line / column header | Result |
|---|---|
| `Status`, `Priority`, `Owner` | metadata |
| `Parent` | `parent_of` (parent → this) |
| `Implements`, `Mapped BRDs`, `BRD`/`BRDs` | `implements` (this → target) |
| `Verified by`, `Scenarios` | `verified_by` (this → target) |
| `FDS` (in an AC table) | `verified_by` (FDS → this AC) |
| `Depends on` | `depends_on` |
| `Conflicts with` | `conflicts_with` |

- Any other ID mentioned in body text becomes a `references` edge.
- An edge to an ID that no document defines is kept and reported as dangling.

**Gherkin:**

```gherkin
@FDS-003
Feature: Hybrid search

  @BRD-004 @smoke
  Scenario: Exact ID wins over semantic matches
    Given an index containing "BRD-042"
    When I search for "BRD-042"
    Then the first result is "BRD-042"
```

- A tag shaped like an ID is a link: the tagged requirement/design is
  `verified_by` the scenario. Feature-level tags apply to every scenario in it.
  Other tags are plain tags (filterable, `-tag:wip` excludes).
- Scenario ID: `<relative path>#<scenario name>`, e.g.
  `features/search.feature#Exact ID wins over semantic matches`. A tag
  `@id:SCN-012` overrides it with a stable explicit ID. Duplicate names in one
  file get `~2`, `~3`.
- Indexed: Feature, Rule, Background, Scenario, Scenario Outline (with Examples
  tables as text), every step with keyword, text and line number. Steps are
  stored one row each with spans, for the later code-index join.
- Parser: hand-written in Go for English keywords plus the `# language:`
  header rejected with a clear note. Using the official `cucumber/gherkin` Go
  module instead is the builder's choice if it builds CGO-free (not verified).

## Architecture

```
project root
  docs/**/*.md, features/**/*.feature   (registered sources, read-only)
        │ parse (markdown, gherkin)
        ▼
  .Wammy/specs.sqlite
     documents, entities, steps, edges, chunks,
     chunk_fts + id_fts (FTS5), chunk_vec (vec1), meta
        ▲                    ▲
   embedder (provider)   search router / graph / coverage
        ▼
  CLI: add, remove, sources, index, search, show, trace, coverage,
       status, doctor, config, mcp, version
```

### Storage

`<root>/.Wammy/specs.sqlite`, same convention as code-index
(`.Wammy/code.sqlite`). Root = nearest ancestor with `.git`, else the working
directory; `SPECS_INDEX_DB` overrides. `.Wammy/` goes into
`<gitdir>/info/exclude`. WAL is checkpointed on close.

Tables (sketch; builder owns the DDL):

- `sources(id, path, kind)` — registered files/directories, relative to root.
- `documents(id, path, type, version, sha256, mtime, size, indexed_at, status,
  fail_reason)`
- `entities(id, doc_id, key, type, title, body, status, priority, tags,
  start_line, end_line, body_sha256)` — `key` is the stable ID (unique);
  `type` ∈ requirement, design, acceptance, feature, scenario.
- `steps(id, entity_id, seq, keyword, text, line)`
- `edges(id, src_key, dst_key, kind, doc_id, line, explicit, confidence,
  origin)` — `kind` ∈ the six spec edges; `dst_key` may be undefined
  (dangling). `explicit=0` rows are inferred suggestions.
- `chunks(id, entity_id, text, text_sha256, embed_fingerprint)`, `chunk_vec`,
  `chunk_fts` (unicode61 over title, body, steps, tags), `id_fts` (trigram over
  key and path).
- `meta(key, value)` — schema version, embedder fingerprint, vector dimension,
  indexed timestamp, provider config.

One chunk per entity (requirements and scenarios are short); a body over the
embedder's limit is split by paragraph.

### Incremental indexing (FDS-005)

Per file: size+mtime shortcut, then sha256. Changed file → re-parse; entities
compared by `body_sha256`, only changed chunks are re-embedded. Files gone from
disk or from the source list → their documents, entities, steps, edges, chunks
and vectors are deleted. Edges are rebuilt per document.

### Search (FDS-003)

Query grammar: free text plus `field:value` filters and exclusions —
`type:`, `status:`, `priority:`, `tag:`, `path:`, `-tag:wip`, `-"phrase"`,
`AND`/`OR`/`NOT` (passed to FTS5), and relation predicates `has:scenario`,
`-has:scenario`, `has:fds`, `-has:fds`.

Order: (1) a query that is an ID → exact hit first, always; (2) FTS5 and
vector results merged with reciprocal-rank fusion; (3) filters applied to both
sides. `--mode auto|exact|fts|vector|hybrid`, `--k`, `--json`. Without an
embedder, `auto` is FTS. Each hit: key, type, title, `path:start-end`, status,
snippet, stale marker if the file changed since indexing.

Example from the spec: `oauth status:approved type:requirement -has:scenario`.

### Trace (FDS-004)

`trace KEY [--direction down|up|both] [--depth N] [--kinds ...]
[--include-inferred] [--json]`. Down follows parent_of, implements (reversed:
BRD → the FDS that implement it), verified_by; up is the reverse. Default depth
5, cycle-safe. Output is a tree with edge kind, explicit/inferred, and the
`path:line` where the link is written.

### Coverage (FDS-007)

Reports: requirements with no implementing design; designs with no verifying
scenario or AC; scenarios linked to nothing; dangling IDs; duplicate IDs;
explicit `conflicts_with` pairs. "Candidate conflict flags" in v1 = explicit
`conflicts_with` plus pairs of same-type entities with very high vector
similarity and different status — labelled candidates. Model-judged
contradiction detection is out of v1 (the spec evaluates it separately).

Inferred links: optional `index --infer` proposes `implements`/`verified_by`
suggestions from vector similarity above a threshold; stored `explicit=0`.

### Embeddings (FDS-002, FDS-009)

Provider code is copied from session-index `internal/provider` (code-index
already carries the same ten files; `internal/` packages cannot be imported).
Default: Ollama `embeddinggemma`. Bedrock (Titan Text V2) only when
`provider.embed` names it **and** `allow_cloud: true` is set (or
`--allow-cloud` for one run); otherwise the call is refused before any network
use. `meta` pins fingerprint and dimension; a mismatch refuses to mix and asks
for `index --reembed`.

### Vector store and vec1 risk

`VectorStore` interface (`Upsert`, `Delete`, `KNN(vec, k, candidateIDs)`,
`Info`) with a vec1 implementation.

Verified: `github.com/ncruces/go-sqlite3/ext/vec1` exists with
`func Register(db *sqlite3.Conn) error`; the module is CGO-free; its latest
tag seen is v0.38.0 (2026-11-21); `main`'s go.mod says `go 1.26.0`; it now
translates SQLite's Wasm build to Go (wasm2go) instead of running it in wazero.

Verified from sqlite.org (`vec1.html`, `vec1doc.html`, read 2026-11-21, pages
dated 2026-10-01) and the driver source at tag v0.38.0:

- vec1's current release is 0.8. Its roadmap still lists "more and better
  testing" and "add support for SIMD on wasm". The Go driver runs SQLite's
  Wasm build, so vec1 runs without SIMD there.
- `CREATE VIRTUAL TABLE t USING vec1(vector[, metadata columns])`. Rows take
  ordinary INSERT / UPDATE / DELETE; the vector is a blob of 32-bit floats;
  the dimension is fixed by the first insert (no declared limit).
- Exact search needs no training. The manual recommends exact mode for under
  about 5,000 vectors. `INSERT INTO t(cmd,arg) VALUES('rebuild',
  '{index:"flat", distance:"cos"}')` sets flat storage and cosine distance
  (Euclidean and cosine are the two metrics; no dot product).
- Query: `SELECT rowid FROM t(:v, '{K: 10}')`. Filters: metadata columns
  compared inside the search, or `streaming:1` to keep producing rows while
  joined tables filter them.
- Binding: `vec1.Register(db *sqlite3.Conn)` in
  `github.com/ncruces/go-sqlite3/ext/vec1`; the v0.38.0 go.mod says
  `go 1.26.0`; supported Windows targets are amd64 and arm64.

Design consequences: specs-index uses vec1 in **flat exact mode with cosine
distance** (a spec corpus is hundreds to a few thousand entities), with
`type` and `status` as metadata columns and streaming for other filters. No
training in v1; approximate mode is a later option for very large corpora.

Measured on an Apple M1 Ultra on 2026-11-21 with a throwaway program on
driver v0.38.0, Go 1.26.0 (SQLite 3.54.1, vec1 "0.8 (Scalar,
single-threaded)"), 768-dim random vectors, K=10, cosine, 40 queries each:

| Vectors | Flat index p50 / p95 | With metadata filter p50 / p95 | No index p50 / p95 |
|---|---|---|---|
| 2,000 | 3.5 / 3.8 ms | 1.4 / 1.6 ms | 5.2 / 6.2 ms |
| 5,000 | 9.1 / 10.0 ms | 3.8 / 4.3 ms | 13.7 / 14.9 ms |
| 20,000 | 36.5 / 37.8 ms | 15.7 / 18.0 ms | 55.0 / 57.8 ms |

Also confirmed: FTS5 is **not** in the default build but is a package of the
same driver (`ext/fts5`, `fts5.Register(db)`); Boolean `NOT` and the trigram
tokenizer work. A stored vector's nearest neighbour is itself and is gone
after DELETE. All 10 filtered results satisfied the filter. The six targets
cross-compile with `CGO_ENABLED=0` (8.5–9.1 MB each, stripped). 20,000
vectors made an 83 MB file; peak memory of the run was 24 MB.

Not verified: running the non-macOS binaries (CI only), and WAL behaviour on
Windows with this driver.

**Plan task 1** is therefore reduced to turning this proof into a kept test
in the repo (FTS5 + vec1 registered on every connection, the same
correctness checks). Fallback triggers during the build: a target fails in
CI, or Windows shows a storage defect. Fallback = `vec0` on the session-index driver (v0.21.3 through the patched fork `wamra/go-sqlite3`), behind
the same interface, and the owner is told before the switch. Local Go is
1.24.1; the newer driver needs a toolchain download (`GOTOOLCHAIN=auto`).

### Filesystem scope (FDS-009)

`add` accepts only paths inside the project root (symlinks resolved first);
anything else is refused. `index` reads only registered sources. The MCP
server's root is `CLAUDE_PROJECT_DIR`, else its working directory.

## Relationship to code-index: two files, one contract (agent's recommendation)

`specs.sqlite` and `code.sqlite` stay separate files in the same `.Wammy/`
directory. They are never merged into one file while the two tools use
different SQLite drivers.

Why not one file:
- Two SQLite builds would write one file. code-index stays on the old driver
  (v0.21.3, now through the patched fork `wamra/go-sqlite3`, which fixes three
  Windows WAL defects; branch `fix/windows-wal`, not merged as of 2026-11-21)
  and specs-index uses v0.38. Each links its own vector module (`vec0` /
  `vec1`), and a defect in either driver's Windows locking would then hit
  both tools' data.
- One file has one writer. A long code-index run would block spec indexing,
  and on Windows also spec searches.
- Two binaries would migrate one schema; deleting a derived index to rebuild
  it would delete the other tool's data.
- Nothing is gained for search: the vector tables differ (`vec0` / `vec1`),
  and ranked lists from separate files merge by rank, as session-index already
  does across archives.

The link is a contract, not a shared schema:
- Join key: the feature file's root-relative forward-slash path plus step
  line (code-index already accepts `path:line` as a symbol argument), and the
  scenario ID. Both tools use the same root rule.
- Each side records the feature file's sha256; a joined answer is marked stale
  when the two differ.
- v1: no database link. `trace` prints `path:line` for scenarios and steps, so
  Claude can pass them to code-index.
- Later (with the code-index Gherkin work): code-index publishes a versioned
  read-only view of step → symbol bindings; specs-index, which has the newer
  driver, attaches `code.sqlite` read-only and extends `trace` into code and
  back. code-index never opens `specs.sqlite`.

Checked with the system `sqlite3` 3.51.0 on a synthetic pair: a file holding a
virtual table whose module is missing still serves its ordinary tables, fails
only when that table is touched, joins through a read-only `ATTACH`, and
passes `integrity_check`. Not checked with the Go drivers.

For v1 this adds only: `meta.contract_version`, root-relative forward-slash
paths, and step line numbers (all already in the tables above).

## Plugin and interface

Repo root, laid out like session-index and code-index:
`cmd/specs-index`, `internal/{config,store,parse,gherkin,graph,search,embed,
provider,coverage,mcp,workspace,filelock}`, `skills/specs-search/SKILL.md`,
`commands/`, `scripts/{build-plugin,check-plugin,build-release,install,
plugin-launcher}.sh`, `testdata/corpus/`, `bench/`, CI as in the siblings.
`build-plugin.sh` stages six binaries plus the launcher; CI checks the tree
and force-pushes the orphan `plugin` branch.

Staged plugin:

```
.claude-plugin/plugin.json      no pinned version
.mcp.json                       { "specs-index": { "command": "sh",
                                  "args": ["${CLAUDE_PLUGIN_ROOT}/bin/specs-index", "mcp"] } }
bin/specs-index                 launcher (sh) + six platform binaries
commands/{add,index,search,trace,coverage,status}.md   → /specs-index:<name>
skills/specs-search/SKILL.md
```

- Commands: the six from the spec, as Markdown command files that run the
  bundled binary. (The plugin docs prefer `skills/` for new plugins; commands
  are used because the spec names `/specs-index:add` etc.)
- MCP **(spec)**: stdio server inside the binary (`specs-index mcp`), typed
  tools `add_source`, `remove_source`, `index`, `search`, `show`, `trace`,
  `coverage`, `status`. Hand-written JSON-RPC, no SDK dependency. This differs
  from the siblings, which expose tools through a mod (`register.ts`); no mod
  here.
- Windows: the server starts through `sh` + launcher, the same way the
  siblings' hooks do, so it needs Git Bash's `sh` on PATH. Whether Claude Code
  on Windows finds `sh` for an MCP `command` is not verified; the plugin docs
  describe no per-platform command. CI on windows-latest must prove it; the
  fallback is a tiny Node shim, since Claude Code itself brings Node.
- No hooks in v1. Freshness: `search`/`trace` print a stale marker; the skill
  tells Claude to run `index` (cheap when nothing changed).

`wammy-plugins`: one new entry in `.claude-plugin/marketplace.json`
(`source: github, repo: wamra/specs-index, ref: plugin`), a README row and
install line, and `specs-index` added to the CI install and binary checks.
The local checkout is on `feat/merged-plugin`, one commit ahead of
`origin/main`; the entry goes on a new branch from it.

## Build order (agent's call)

1. Proof build (driver + FTS5 + vec1, six targets).
2. Repo skeleton, config, store, fixtures corpus with ground-truth mapping file.
3. Markdown and Gherkin parsers (AC-001).
4. Incremental index, sources, scope checks (AC-005, AC-009 filesystem half).
5. Exact + FTS search with the filter grammar; trace; coverage (AC-003 partly,
   AC-004, AC-007). Everything to here needs no model.
6. Providers, embedding, vector and hybrid search, cloud gate (AC-002, AC-009).
7. CLI polish, MCP server, skill, commands.
8. Plugin staging, check script, CI, marketplace entry (AC-006).
9. Benchmark runner (AC-008): A = SQLite + Titan V2 (paid Bedrock, owner runs
   or approves), B = SQLite + EmbeddingGemma, C = Orama + Qwen3-Embedding 0.6B
   through a Node harness under `bench/orama/`. Metrics as in spec section 7.

Tests first throughout; `go vet ./... && go test ./...`; fake embedding server
as in code-index.

## Owner decisions (not blocking the build, needed before publishing)

- Creating the GitHub repo `wamra/specs-index`, its visibility, and pushing.
  The marketplace entry only works once a `plugin` branch is pushed.
- Signing of artifacts (spec asks; siblings don't).
- Running benchmark config A against Bedrock (costs money).
- Merging the `wammy-plugins` change to `main`.

## Clarifications (agent's calls after a builder read-through)

- **vec1 proof fails → stop.** Task 1 is a gate: on a fallback trigger the
  build stops and the owner decides. Nothing else starts before task 1 returns.
- **One driver for everything.** If vec1 passes, the whole store (FTS5
  included) uses the new driver and the repo, CI and release script move to the
  Go version it requires (toolchain download through `GOTOOLCHAIN=auto`). The
  siblings stay on 1.24.1. A vec0 fallback would mean the old driver for
  everything; `internal/store` is the seam, not only `VectorStore`.
- **Spec.** The spec text is copied to `docs/spec/specs-index_BRD_FDS_v0.2.md`
  in the new repo. It sets no pass thresholds. Benchmark metrics: Recall@5,
  MRR@10, exact-ID Accuracy@1, mapping recall, p50/p95 latency, RAM, index
  size, embedding cost — reported, not gated.
- **Nested headings.** An ID heading under another ID heading is a separate
  entity; the parent's body stops at it, and the parent gets a `parent_of`
  edge to the child only when both IDs share a prefix. Otherwise no edge.
- **Table meaning** is decided per column header (table above), applied to the
  row's own ID in the first column. Heading/table merge: body from the
  heading; metadata fields from the heading when both have one; edges are the
  union.
- **Code.** IDs inside fenced code blocks are ignored; inline-code IDs count
  as `references`.
- **`references`** is one of the six spec edges, stored `explicit=1` with
  `origin=mention`. It never counts toward coverage and is not followed by
  `trace` unless `--kinds references` is given. Dangling-ID reports cover all
  edge kinds, with mentions listed separately.
- **`has:fds` / `has:scenario`** count explicit `implements` / `verified_by`
  edges only. Inferred edges never satisfy them and never change coverage.
- **Feature-level ID tags** do credit every scenario in the feature; that is
  how Gherkin tag inheritance works. Tags on `Rule` are inherited the same
  way; tags on an `Examples:` block attach to its Scenario Outline.
- **Windows MCP shim** is in v1 scope only if CI shows `sh` does not start
  the server; it would then replace `sh` in `.mcp.json` on all platforms.
- **Repo and CI.** All work is local (`git init`, local commits) until the
  owner creates the GitHub repo. CI files are written but run only after that.
- **`.Wammy/` exclude.** Resolve the git dir with `git rev-parse --git-dir`
  when git is present (handles worktrees); with no `.git`, skip the exclude.

## Left to the builder

Exact DDL, RRF constants, chunk text layout, FTS tokenizer details, CLI output
formatting, MCP tool schemas, the fixture corpus content (about 25 BRDs,
15 FDS, 10 ACs, 8 feature files with deliberate gaps, a dangling ID, a
conflict pair and a many-to-many mapping), and the config file shape
(follow session-index: file + env overrides).
