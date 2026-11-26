# specs-index reference

## Contents

- [Commands](#commands)
- [Search query grammar](#search-query-grammar)
- [Index location and project root](#index-location-and-project-root)
- [Configuration](#configuration)
- [Environment variables](#environment-variables)
- [MCP server](#mcp-server)
- [Slash commands and skill](#slash-commands-and-skill)
- [Scripts](#scripts)
- [Exit codes](#exit-codes)

## Commands

```
specs-index <command> [flags]     (every command except version and mcp takes --root DIR and --json)
```

| Command | What it does |
| --- | --- |
| `add PATH...` | Register files or directories (inside the project root) as sources. Does not index. |
| `remove PATH...` | Unregister sources and drop the documents they alone covered. Files on disk are untouched. |
| `sources` | List the registered sources. |
| `index [--reembed] [--infer] [--allow-cloud] [--no-embed]` | Bring the index up to date. Incremental: unchanged files are skipped. |
| `search QUERY [--mode auto\|exact\|fts\|vector\|hybrid] [--k 10]` | Search; see the grammar below. Use `--` before a query that starts with `-`. |
| `show KEY` | One entity with its source lines and its direct incoming and outgoing links. An unknown key is an error. |
| `trace KEY [--direction down\|up\|both] [--depth 5] [--kinds a,b] [--include-inferred]` | Follow links from an entity. |
| `coverage [--no-candidates]` | Gaps and ID problems (below). |
| `status` | The registered sources, index counts, last index time, the configured embedder and the environment variables. |
| `doctor [--allow-cloud]` | Check the index (integrity, FTS5, vector table) and the embedder; exits 1 on a failure. Never contacts a cloud embedder unless `--allow-cloud`. |
| `config [init]` | Show the effective configuration, or write an example config file (never overwrites an existing one). |
| `version` | Print the version (`-ldflags "-X main.version=..."` sets it at build time). |
| `mcp` | Serve the tools over stdio; takes no arguments. See [MCP server](#mcp-server). |

Common flags (every command except `version` and `mcp`): `--root DIR` (project root), `--json` (print the result as JSON).

### index

- `--reembed`: discard every vector and embed all chunks again (after changing the model).
- `--infer`: after embedding, store suggested `implements` / `verified_by` links. They are inferred (`explicit=0`) and never count as coverage.
- `--allow-cloud`: allow a cloud embedding provider for this run.
- `--no-embed`: index text only.

Only one `index` of a project runs at a time (CLI or MCP): it holds a lock on the file `<index>.lock` next to the index database (`.Wammy/specs.sqlite.lock` by default; the file stays in place; `index` refuses a lock path that is a symbolic link). A second `index` prints one line to stderr saying that another index of the project is running, waits up to two minutes for the lock, and then either runs as usual (what the first run indexed is unchanged and skipped) or fails with exit 1. Read commands (`search`, `show`, `trace`, `coverage`, `status`, `sources`, `doctor`) never take the lock; `add` and `remove` do not take it either.

The first lines report how many files were scanned, parsed, unchanged, removed or failed, then the entity and edge counts, then whether embedding happened (`no embedder configured; the index is text-only`). A cloud embedder that is not allowed, an unreachable embedder, or an embedder that differs from the one the stored vectors came from (a changed model or vector size) does not fail `index`: the text index is updated and the `embedding:` line says why. Nothing is embedded for a blocked cloud embedder or a mismatch; an embedder that stops answering part-way leaves the chunks embedded so far (`embedding.embedded` counts them) and the rest for the next `index`. In JSON, `embedding.state` is `none`, `ok`, `cloud_blocked`, `unreachable` or `mismatch`, and `embedding.note` holds the explanation; after a mismatch the note names `index --reembed`. Explicit `search --mode vector` and `--mode hybrid` keep refusing until then.

### trace

Direction `down` (default) follows links away from the key (requirement to designs and scenarios), `up` follows them back, `both` does both. `--depth` is how many links to follow. `--kinds` is a comma-separated list; the default is `parent_of,implements,verified_by`, and exactly the listed kinds are followed. `--depth` must be at least 1. `--include-inferred` also follows inferred links. Output is an indented tree; each line shows how it was reached (`[via implements reversed]`), `[repeat]` for an entity already shown, and `file:line`: for the root its definition, for every other line the place where the link that reached it is written (the `Parent:` line, the table row, the `@BRD-003` tag line, ...), or the definition when it was reached over an inferred link, which no document holds. In JSON each node has `path` and `line` (the definition) and `link_path` and `link_line` (where the link is written; empty on the root and for inferred links).

### coverage

Reports, with source lines: requirements without an implementing design; designs without a verifying scenario or criterion; scenarios verifying nothing; dangling IDs (used as the target of a link but never defined); duplicate IDs (an ID defined in more than one file, or by two headings in one file); explicit conflicts (`Conflicts with`); and candidate conflicts from vector similarity (an empty list without embeddings). Only explicit links count.

An ID-shaped word in prose (a `references` mention such as `UTF-8` or `SHA-256`) is reported as dangling only when its prefix is used by a defined entity or appears in the prefix mapping (`types` plus the defaults `BRD`, `FDS`, `AC`); IDs in link fields and tags are always checked.

The candidate scan runs one nearest-neighbour query per embedded chunk, so its time grows with the square of the number of chunks. It is skipped above 5,000 embedded chunks (`coverage.CandidateMaxVectors`), or always with `--no-candidates`; the candidate list is then empty and a `note:` line (`candidates_note` in JSON) says why.

## Search query grammar

Implemented in `internal/search/query.go`.

| Part | Meaning |
| --- | --- |
| words | full-text terms, all required; `prefix*` matches a prefix; `AND`, `OR`, `NOT` are operators |
| `"a phrase"` | phrase match |
| `BRD-004` | a bare ID (`[A-Z][A-Z0-9]*-<digits>`): exact key lookup, exact hits come first. The same word in another letter case (`brd-004`) is also looked up upper-cased, and stays a full-text word |
| `type:` | entity type: `requirement`, `design`, `acceptance`, `feature`, `scenario` |
| `status:`, `priority:` | the value of the `Status` / `Priority` field (case-insensitive equality) |
| `tag:` | a Gherkin tag without the `@`, for example `tag:smoke` |
| `path:` | case-insensitive substring of the root-relative file path |
| `-type:x`, `-status:x`, ... | negated filter |
| `has:scenario`, `has:fds` | the entity has an explicit `verified_by` link to a scenario / an explicit `implements` link from a design |
| `-has:scenario`, `-has:fds` | the entity has none |
| `-word`, `-"phrase"` | exclude hits containing it (exact-ID hits too) |

A query of filters only lists the matching entities (ordered by key). `has:` accepts only `scenario` and `fds`; any other value is an error. An unbalanced quote is an error.

Modes (`--mode`, default `auto`):

- `auto`: exact IDs first, then full text; with an embedder that has embedded the index it also merges in meaning (vector) results. If the embedder is down, a different model was used for the stored vectors, or nothing is embedded yet, it falls back to exact + full text and prints a notice.
- `exact`: ID lookup only (filters apply).
- `fts`: full text only.
- `vector`, `hybrid`: need an embedder; without one they fail (`vector search needs an embedder, but no embedder is configured`).

Each hit shows key, type, title, file and lines, status, and `(stale: ...)` when the file changed since it was indexed (run `index`). `--k` sets the number of hits (default 10, at least 1). In JSON, hits carry `key`, `type`, `title`, `path`, `start_line`, `end_line`, `status`, `snippet`, `score`, `stale`, `source` (`exact`, `fts`, `vector` or `filter`), and the result has `query`, `mode`, `notes`, and `notice` when `auto` fell back to exact + full text (the same one-line message the text output prints to stderr; the key is absent otherwise).

## Index location and project root

- Root: `--root DIR`; else the nearest ancestor of the working directory that contains `.git`; else the working directory. (The MCP server uses `CLAUDE_PROJECT_DIR`, else the working directory, resolved the same way.)
- Index: `<root>/.Wammy/specs.sqlite`; `SPECS_INDEX_DB` overrides the path. The tool refuses to open an index when `.Wammy/` (with the default location) or the database file, its `-wal`, `-shm` or `.lock` file is a symbolic link.
- Paths stored and printed are root-relative with forward slashes. `add` rejects a path outside the root.
- `.Wammy/` is never indexed. In a git work tree `add` appends `.Wammy/` to `<gitdir>/info/exclude` (never `.gitignore`).
- Files with the extensions `.md` and `.feature` are indexed; others are ignored.

## Configuration

`~/.config/specs-index/config.json`, merged over the defaults; environment variables override it. `specs-index config` prints the effective values; `specs-index config init` writes an example (it refuses to overwrite).

```json
{
  "provider": {"embed": "ollama"},
  "model": "embeddinggemma",
  "base_url": "",
  "allow_cloud": false,
  "types": {}
}
```

Fields: `provider.embed` (`ollama`, `openai`, `bedrock` or `none`; default `ollama`), `model` (default `embeddinggemma`, only for Ollama), `base_url`, `allow_cloud`, and the optional `dims` (vector size override), `api_key_env` (the name of the variable holding an OpenAI-compatible API key; the key is never stored), `region` and `profile` (AWS region and shared-config profile for Bedrock). An unknown provider or a malformed file is an error.

`types` maps a Markdown ID prefix to an entity type, `requirement`, `design` or `acceptance`, and is merged over the defaults `BRD` = requirement, `FDS` = design, `AC` = acceptance; a prefix with no mapping is a requirement. Example: `"types": {"SRS": "requirement", "SDD": "design", "FDS": "acceptance"}`. A prefix must be upper-case letters and digits starting with a letter; any other type is an error. It is read from the config file only (there is no environment variable for it) and applies to Markdown documents only (Gherkin scenarios are always `scenario`). `index` remembers the mapping it parsed with: when the effective mapping changes, the next `index` parses every Markdown document again, including unchanged files. `config` prints the effective mapping (`types` in JSON).

A non-local provider is contacted only when `allow_cloud` is true (or `SPECS_INDEX_ALLOW_CLOUD=true`, or `--allow-cloud` on `index` / `doctor`). Without any embedder, everything except vector and hybrid search works.

## Environment variables

From `config.EnvVars()`:

| Variable | Meaning |
| --- | --- |
| `SPECS_INDEX_EMBED_PROVIDER` | embedding provider: `ollama`, `openai`, `bedrock` or `none` (default `ollama`) |
| `SPECS_INDEX_EMBED_MODEL` | embedding model name (default `embeddinggemma` for Ollama) |
| `SPECS_INDEX_EMBED_BASE_URL` | base URL of the Ollama or OpenAI-compatible server |
| `SPECS_INDEX_EMBED_API_KEY_ENV` | name of the variable holding the OpenAI-compatible API key |
| `SPECS_INDEX_EMBED_REGION` | AWS region for the `bedrock` provider |
| `SPECS_INDEX_EMBED_PROFILE` | AWS shared-config profile for the `bedrock` provider |
| `SPECS_INDEX_EMBED_DIMS` | vector size override, a positive integer |
| `SPECS_INDEX_ALLOW_CLOUD` | `true` allows non-local embedding providers (same as `--allow-cloud`) |
| `SPECS_INDEX_DB` | path of the index database |

`status` and `config` (and the MCP `status` tool) list these variables with their current values. A base URL is shown with any password replaced by `xxxxx`; `SPECS_INDEX_DB` is shown root-relative, or as `(outside project)`, like the `db` field of `status`. The `base_url` field of `status` and `config` is redacted the same way.

`CLAUDE_PROJECT_DIR` is read by `specs-index mcp` only.

## MCP server

`specs-index mcp` speaks newline-delimited JSON-RPC 2.0 over stdio and implements the `initialize` handshake revisions 2024-11-05 through 2025-11-25 and the stateless 2026-07-28 revision. stdout carries protocol messages only. The project root is fixed at start; tool arguments cannot change it.

| Tool | Arguments | Read-only |
| --- | --- | --- |
| `add_source` | `path` (required) | no |
| `remove_source` | `path` (required) | no |
| `index` | `reembed` (boolean) | no |
| `search` | `query` (required), `mode` (`auto`, `exact`, `fts`, `vector`, `hybrid`), `k` (1 to 100, default 10) | yes |
| `show` | `key` (required) | yes |
| `trace` | `key` (required), `direction`, `depth` (1 to 50), `kinds` (array), `include_inferred` | yes |
| `coverage` | `no_candidates` (boolean) | yes |
| `status` | none | yes |

`index` has no cloud option: a request for `allow_cloud` is refused as an unknown argument. Results are the same JSON as the CLI's `--json`, as text content; failures are results with `isError` set.

Plugin start line (`.mcp.json`): `sh "${CLAUDE_PLUGIN_ROOT}/bin/specs-index" mcp`.

## Slash commands and skill

Plugin name `specs-index`. Each command passes the user's text to the matching MCP tool as structured arguments, so no shell is involved; if the MCP server is unavailable it runs `sh "${CLAUDE_PLUGIN_ROOT}/bin/specs-index" <command>` with every user-supplied value single-quoted, flags first and the value after `--`. `/specs-index:index` never passes `--allow-cloud`.

| Command | Runs |
| --- | --- |
| `/specs-index:add PATH...` | `add` |
| `/specs-index:index [flags]` | `index` |
| `/specs-index:search QUERY [flags]` | `search` |
| `/specs-index:trace KEY [flags]` | `trace` |
| `/specs-index:coverage` | `coverage` |
| `/specs-index:status` | `status` |

Commands run in Claude Code's working directory, so the project root is found as for the CLI. The `specs-search` skill (`skills/specs-search/SKILL.md`) tells Claude when to use search, trace or coverage, the query grammar, to run `index` when results are marked stale, and that source files are never edited.

## Scripts

| Script | Purpose |
| --- | --- |
| `scripts/plugin-launcher.sh` | Becomes `bin/specs-index` in the plugin: runs the bundled binary for this OS and CPU, else `specs-index` from `PATH`, else exits 127. |
| `scripts/build-plugin.sh OUT [VERSION]` | Cross-builds the six binaries (darwin, linux, windows on amd64 and arm64, `CGO_ENABLED=0`) and stages the plugin tree into OUT, which it deletes first; it refuses an OUT that is `/`, `$HOME`, the repository or an ancestor of it (exit 2). |
| `scripts/check-plugin.sh OUT [VERSION_TEXT]` | Checks a staged tree: six binaries over 5 MB, launcher, executable bits, six command files, the skill, `LICENSE` and `THIRD_PARTY_NOTICES.md`, `plugin.json` without `version`, `.mcp.json` pointing at the launcher, tree under 120 MB, launcher exits 127 with no binary, `version` runs. Prints `check-plugin: ok (...)`. |
| `scripts/build-release.sh [VERSION]` | Release archives and `checksums.txt` into `dist/`. |
| `scripts/install.sh [--binary FILE] [--bin-dir DIR]` | Install the binary (macOS, Linux) and write a starter config if none exists. |
| `scripts/gen-notices.sh` | Regenerates `THIRD_PARTY_NOTICES.md` from the Go module cache (offline once modules are downloaded). |
| `scripts/mcp-smoke.js PLUGIN_DIR PROJECT_DIR` | Start the server through `sh` and the launcher; run `initialize`, `tools/list`, `tools/call status` (needs Node). |

## Exit codes

`0` success; `1` failure (including a failed `doctor` check, an unknown key, a path outside the root); `2` usage error.
