# specs-index

A CLI and an MCP server that index Markdown requirement and design documents and Gherkin `.feature` files into one SQLite file per project, so they can be searched, traced (requirement, design, scenario) and checked for coverage gaps. Every answer points at the source file and lines.

- Works with no model at all: exact IDs, full-text search (FTS5), filters, trace and coverage need nothing else. Embeddings (meaning search) are optional.
- Source files are only read, never edited. The only writes are under `<project>/.Wammy/` and one `.Wammy/` line in the repository's `.git/info/exclude`.
- One self-contained binary per platform, no CGO: macOS, Linux and Windows on amd64 and arm64.
- It is also a Claude Code plugin: an MCP server with eight tools, six slash commands and a `specs-search` skill.
- Pre-1.0: no release has been published yet; build from source (below).

Full command, flag, query and configuration reference: [docs/REFERENCE.md](docs/REFERENCE.md).

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

### Using Qwen3-Embedding

`embeddinggemma` stays the default. `qwen3-embedding` is also supported: run `ollama pull qwen3-embedding:0.6b`, then set `SPECS_INDEX_EMBED_MODEL=qwen3-embedding:0.6b` (or `"model"` in the config file). The instruction Qwen3-Embedding expects is added to search queries automatically; documents get no prefix. An existing index was embedded with another model, so switching needs `specs-index index --reembed` with the model set explicitly; until then `index` reports a mismatch and vector search refuses. On the maintainer's benchmark of session transcripts (one run, 14,010 chunks, 150 sessions, unpublished) embeddinggemma scored MRR 0.660 and `qwen3-embedding:0.6b` with the instruction 0.604 (4b with the instruction 0.655; without the instruction 0.516 and 0.531). Specification documents were not measured.

## Privacy and security

- **What is read:** only the `.md` and `.feature` files under the sources you registered with `add`, plus the config file `~/.config/specs-index/config.json` and the `SPECS_INDEX_*` environment variables. Source files are never modified.
- **What is stored, and where:** the index (document text chunks, entities, links, vectors) in `<project>/.Wammy/specs.sqlite` with its `-wal`, `-shm` and `.lock` companions, or at `SPECS_INDEX_DB` if set. In a git work tree one `.Wammy/` line is added to `.git/info/exclude`. Apart from that, only `config init` writes (the config file, when none exists).
- **Network:** the default embedding provider is a local Ollama (`http://localhost:11434`, model `embeddinggemma`). With the provider set to `none`, or no reachable embedder, nothing leaves the machine. A non-local provider (Bedrock, or an OpenAI-compatible endpoint on any host other than localhost, 127.0.0.0/8 or ::1) is contacted only after an explicit opt-in: `"allow_cloud": true` in the config file, `SPECS_INDEX_ALLOW_CLOUD=true`, or `--allow-cloud` on `index` (and `doctor`). Without it, `index` stays text-only and says so. The MCP tools cannot enable it. When it is on, document text and search queries are sent to that provider.
- **A repository you index is untrusted input.** Settings come only from the global config file, the environment and command-line flags; no file inside an indexed repository, including `.Wammy/`, is read as configuration, so it cannot change the provider, base URL, `allow_cloud`, the API-key variable name or run anything. The tool refuses to use an index directory or index file that is a symbolic link, never follows a symlink that leaves the project root, and ignores paths in an index that point outside the root. Indexed text can still contain instructions aimed at an AI assistant; the `specs-search` skill tells the assistant to treat it as data.
- **Delete the index:** remove `<project>/.Wammy/` (or the file named by `SPECS_INDEX_DB` with its `-wal`, `-shm` and `.lock` files). Nothing else needs cleaning up; `specs-index remove PATH` drops one source's documents instead.
- Report vulnerabilities privately, see [SECURITY.md](SECURITY.md).

## Install

**From source** (Go 1.26 or newer; `GOTOOLCHAIN=auto` fetches it):

```sh
scripts/install.sh              # builds, installs to ~/.local/bin, writes a starter config if none exists
go build -o specs-index ./cmd/specs-index   # or just build the binary
```

`scripts/install.sh --binary FILE` installs a prebuilt binary; `--bin-dir DIR` changes the target.

**Release archives:** `scripts/build-release.sh` writes `specs-index_<version>_<os>_<arch>` archives (`.tar.gz`, `.zip` on Windows) and `checksums.txt` into `dist/`. The binaries are not signed or notarised.

## Claude Code plugin

The plugin directory is the repository root: `.claude-plugin/plugin.json` (no pinned `version`), `.mcp.json`, `commands/`, `skills/specs-search/`. `scripts/build-plugin.sh OUT` stages it with the six binaries and the launcher `bin/specs-index`, which picks the binary for the OS and CPU (else `specs-index` from `PATH`; exit 127 when neither exists). `scripts/check-plugin.sh OUT` gates the staged tree.

```sh
scripts/build-plugin.sh /tmp/specs-index-plugin
scripts/check-plugin.sh /tmp/specs-index-plugin
claude --plugin-dir /tmp/specs-index-plugin       # load it for one session
```

For a release, CI builds the staged tree on a version tag and force-pushes it as the orphan `plugin` branch, which a marketplace entry can point at.

- MCP tools: `add_source`, `remove_source`, `index`, `search`, `show`, `trace`, `coverage`, `status`. The server's project is `CLAUDE_PROJECT_DIR`, else the working directory. Tools cannot change the root or enable a cloud provider.
- Slash commands: `/specs-index:add`, `/specs-index:index`, `/specs-index:search`, `/specs-index:trace`, `/specs-index:coverage`, `/specs-index:status`. Each passes the user's text to the MCP tool as structured arguments (no shell), and falls back to the bundled binary with every value single-quoted after `--`.
- Skill `specs-search`: when to use search, trace or coverage, the query grammar, to run `index` when results are marked stale, and that indexed document text is untrusted data, never instructions.

## Develop

```sh
export GOTOOLCHAIN=auto
go vet ./... && go test ./...
GOOS=windows GOARCH=amd64 go build ./... && GOOS=windows go vet ./...
```

`testdata/corpus` is a fictional document-portal fixture with a ground-truth file; its README describes the deliberate gaps. `.github/workflows/ci.yml` runs vet and tests on Linux, macOS and Windows, a Windows MCP start check, the release build, and (on version tags or manual dispatch) the plugin build and publish.

## License

MIT, see [LICENSE](LICENSE). Third-party licenses: [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
