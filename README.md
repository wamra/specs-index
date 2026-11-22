# specs-index

A CLI and an MCP server that index Markdown requirement and design documents and Gherkin `.feature` files into one SQLite file per project, so they can be searched, traced (requirement, design, scenario) and checked for coverage gaps. Every answer points at the source file and lines.

- Works with no model at all: exact IDs, full-text search (FTS5), filters, trace and coverage need nothing else. Embeddings (meaning search) are optional.
- Source files are only read, never edited. The only writes are under `<project>/.Wammy/` and one `.Wammy/` line in the repository's `.git/info/exclude`.
- One self-contained binary per platform, no CGO: macOS, Linux and Windows on amd64 and arm64.
- It is also a Claude Code plugin: an MCP server with eight tools, six slash commands and a `specs-search` skill.

Full command, flag, query and configuration reference: [docs/REFERENCE.md](docs/REFERENCE.md).

## Status

Work in progress. The repository `github.com/wamra/specs-index` is **not published yet**: there is no remote, no `plugin` branch and no release, so the install paths below that need them do not work yet.

What was verified (macOS arm64, Claude Code 2.1.330, staged build of the working tree on top of commit `54a2e0e`):

- The six binaries cross-compile with `CGO_ENABLED=0` (`scripts/build-plugin.sh`); `scripts/check-plugin.sh` reports `check-plugin: ok`; `claude plugin validate` accepts the staged tree (one warning, that no `version` is pinned, which is intended).
- Claude Code loaded the staged tree with `claude --plugin-dir` and `claude mcp list` reported the server `plugin:specs-index:specs-index` as connected, started as `sh <plugin>/bin/specs-index mcp`.
- `scripts/mcp-smoke.js` started the server through `sh` and the launcher and ran `initialize`, `tools/list` (the eight tools) and `tools/call` on macOS.
- `scripts/build-release.sh` produced the six archives and `checksums.txt`; `scripts/install.sh --binary` installed a binary and wrote a starter config (into a temporary home).
- Every CLI command and every command line in the slash command files ran against a copy of `testdata/corpus` with no embedder (`SPECS_INDEX_EMBED_PROVIDER=none`).

What was **not** verified:

- Linux and Windows at runtime: those binaries were only cross-compiled, not run.
- That Claude Code on Windows finds `sh` to start the MCP server (`.mcp.json` uses `command: sh`). The `mcp-windows` CI job exists to prove it; it has never run.
- CI as a whole (`.github/workflows/ci.yml`): never run, there is no remote.
- A model session that calls the MCP tools or the slash commands (the isolated Claude Code profile used for testing was not logged in), including `${CLAUDE_PLUGIN_ROOT}` substitution inside the command files.
- Embedding providers beyond one local model: the benchmark (configuration B in [BENCHMARKS.md](BENCHMARKS.md)) indexed and ran hybrid search with a local Ollama serving `embeddinggemma`; `--mode vector`, `--infer`, Bedrock, OpenAI-compatible endpoints and other models were not run against a real provider.
- `scripts/install.sh` beyond the `--binary` form; installing from a marketplace.
- Signing and notarisation of the binaries: not done.

## Quick start

```sh
cd my-project                     # a directory with .git, or any directory
specs-index add docs features     # register directories (or files) inside the project
specs-index index                 # parse and index; embeds only if an embedder is configured
specs-index search "type:requirement status:draft -has:scenario"
specs-index trace BRD-004
specs-index coverage
```

The project root is the nearest ancestor of the working directory that has a `.git`, else the working directory itself; every command accepts `--root DIR` to set it and `--json` to print JSON. The index is `<root>/.Wammy/specs.sqlite` (override: `SPECS_INDEX_DB`).

## What it indexes

Registered directories are walked for `.md` and `.feature` files (`.git` and `.Wammy` are skipped).

- **Markdown**: entities have IDs of the form `[A-Z][A-Z0-9]*-<digits>` (`BRD-004`, `FDS-012`, `AC-003`). An ID heading (`## BRD-004 Upload resume`) or the first column of a pipe table row is an entity. The prefixes `BRD`, `FDS` and `AC` mean requirement, design and acceptance; the `types` map in the config file maps other prefixes (for example `"types": {"SDD": "design"}`), and a prefix with no mapping is a requirement. Field lines under a heading (`Status`, `Priority`, `Parent`, `Depends on`, `Conflicts with`, `Mapped BRDs`, ...) and table columns become metadata and links; an `FDS:` field under an acceptance criterion is a `verified_by` link, under any other heading a `references` link. IDs inside fenced code blocks are ignored; an ID in inline code or prose is a `references` link.
- **Gherkin**: one entity per feature and per scenario. A doc string that is never closed makes the file fail to index (it is listed as failed with the opening line). ID tags (`@BRD-003`) on a feature, rule or scenario link the tagged ID to the scenarios; `@id:SCN-101` sets a scenario's key.

Link kinds: `parent_of`, `implements`, `verified_by`, `depends_on`, `conflicts_with`, `references`. Explicit links (written in the documents) are authoritative; inferred links (`index --infer`, needs an embedder) never count towards coverage.

## Embeddings (optional)

The default provider is a local Ollama with the `embeddinggemma` model; `openai` (any OpenAI-compatible endpoint) and `bedrock` are the others, and `none` turns embeddings off. Document text is sent to a non-local provider (Bedrock, or an endpoint on another host) only when `allow_cloud` is true or `--allow-cloud` is passed. Without a reachable embedder `index` is text-only and everything except `--mode vector` and `--mode hybrid` works. Settings: `~/.config/specs-index/config.json` (`specs-index config init` writes an example) and the `SPECS_INDEX_*` environment variables, listed in the reference.

## Install

**From source** (Go 1.26 or newer; `GOTOOLCHAIN=auto` fetches it):

```sh
scripts/install.sh              # builds, installs to ~/.local/bin, writes a starter config if none exists
go build -o specs-index ./cmd/specs-index   # or just build the binary
```

`scripts/install.sh --binary FILE` installs a prebuilt binary; `--bin-dir DIR` changes the target.

**Release archives:** `scripts/build-release.sh` writes `specs-index_<version>_<os>_<arch>` archives (`.tar.gz`, `.zip` on Windows) and `checksums.txt` into `dist/`. Nothing has been released.

## Claude Code plugin

The plugin directory is the repository root: `.claude-plugin/plugin.json` (no pinned `version`), `.mcp.json`, `commands/`, `skills/specs-search/`. `scripts/build-plugin.sh OUT` stages it with the six binaries and the launcher `bin/specs-index`, which picks the binary for the OS and CPU (else `specs-index` from `PATH`; exit 127 when neither exists). `scripts/check-plugin.sh OUT` gates the staged tree.

```sh
scripts/build-plugin.sh /tmp/specs-index-plugin
scripts/check-plugin.sh /tmp/specs-index-plugin
claude --plugin-dir /tmp/specs-index-plugin       # load it for one session
```

CI is written to publish the staged tree as the orphan `plugin` branch on pushes to `main`, for a marketplace entry that points at it. That has not happened: the repository is not published.

- MCP tools: `add_source`, `remove_source`, `index`, `search`, `show`, `trace`, `coverage`, `status`. The server's project is `CLAUDE_PROJECT_DIR`, else the working directory. Tools cannot change the root or enable a cloud provider.
- Slash commands: `/specs-index:add`, `/specs-index:index`, `/specs-index:search`, `/specs-index:trace`, `/specs-index:coverage`, `/specs-index:status`. Each runs the bundled binary and shows its output.
- Skill `specs-search`: when to use search, trace or coverage, the query grammar, and to run `index` when results are marked stale.

## Develop

```sh
export GOTOOLCHAIN=auto
go vet ./... && go test ./...
GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows go vet ./...
```

`testdata/corpus` is a fictional document-portal fixture with a ground-truth file; its README describes the deliberate gaps. `.github/workflows/ci.yml` runs vet and tests on Linux and Windows, the Windows MCP start check, the release build, and the plugin build and check.

## License

MIT, see [LICENSE](LICENSE).
