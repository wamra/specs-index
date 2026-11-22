---
name: specs-search
description: Search, trace and check the coverage of a project's requirement documents (BRD), design documents (FDS), acceptance criteria and Gherkin .feature scenarios with specs-index. Use when asked which requirement covers something, what implements or verifies a requirement, which requirements have no design or no scenario, what a scenario verifies, or to find a requirement, design or scenario by ID, topic, status or tag, before falling back to grep.
---

# specs-search

`specs-index` keeps one SQLite index per project at `<project>/.Wammy/specs.sqlite`, built from Markdown requirement/design documents and Gherkin `.feature` files. Every answer names the source file and lines. The same tools are available as MCP tools (`add_source`, `remove_source`, `index`, `search`, `show`, `trace`, `coverage`, `status`) and as slash commands (`/specs-index:add`, `:index`, `:search`, `:trace`, `:coverage`, `:status`). The project root is the nearest ancestor of the working directory that has a `.git`; for the MCP server it is `CLAUDE_PROJECT_DIR`.

**Source files are never edited.** The tool only reads them; its only writes are under `.Wammy/` (and one `.Wammy/` line in the repository's `info/exclude`). Change requirements by editing the documents yourself, then run `index`.

## Is there an index?

Run `status`. If it says there is no index, tell the user and offer:

```
specs-index add docs features    # register the directories that hold the documents (inside the project root)
specs-index index                # build the index
```

Run `index` again whenever files changed. It is incremental: unchanged files are skipped.

**When a hit is marked `stale`, run `index` first.** Stale means the file changed after it was indexed, so line numbers and text may be out of date. Read the file before trusting them.

## Which tool

| Question | Use |
| --- | --- |
| Where is X described? Which requirement is about Y? List by status, priority, tag or type. | `search` |
| Show me BRD-004 with its lines and direct links. | `show KEY` |
| What designs and scenarios cover this requirement? What does this scenario verify? What breaks if I change this? | `trace KEY` (`--direction down` from a requirement, `up` from a scenario, `both`) |
| What is missing across the whole project? | `coverage` |

`search` finds entities; `show` reads one; `trace` follows links from one entity; `coverage` reports gaps for all of them at once. Only explicit links (written in the documents or tags) count towards coverage, `has:` filters and the default `trace`. Inferred links (from `index --infer`, with an embedder) are shown only by `trace --include-inferred` and never count.

## Search query grammar

```
specs-index search "<query>" [--mode auto|exact|fts|vector|hybrid] [--k 10] [--json]
```

- Free text: words, `"quoted phrases"`, `AND` / `OR` / `NOT`, and `prefix*`.
- A bare ID such as `BRD-004` is an exact key lookup; exact hits come first.
- Filters, combined with AND, matched case-insensitively: `type:` (`requirement`, `design`, `acceptance`, `feature`, `scenario`), `status:`, `priority:`, `tag:` (a Gherkin tag without the `@`, for example `tag:smoke`), `path:` (a substring of the file path). A leading `-` negates a filter: `-status:draft`.
- Relationship filters: `has:scenario` (a `verified_by` link to a scenario), `has:fds` (an `implements` link from a design), and their negations `-has:scenario`, `-has:fds`.
- Exclusion: `-word` and `-"phrase"` drop hits containing it.
- A query made only of filters lists the matching entities. A query starting with `-` needs `--` before it: `specs-index search --root . -- '-tag:smoke type:scenario'`.
- Modes: `auto` (default) is exact IDs plus full text, plus meaning (vector) search when an embedder is configured and has embedded the index; `exact` matches IDs only; `fts` is full text only; `vector` and `hybrid` need an embedder and fail with an error without one.

Examples:

```
specs-index search "type:requirement status:draft -has:scenario"
specs-index search "upload resume"
specs-index search "path:features tag:wip"
specs-index search BRD-004
```

## Trace

```
specs-index trace KEY [--direction down|up|both] [--depth 5] [--kinds parent_of,implements,verified_by] [--include-inferred] [--json]
```

Edge kinds: `parent_of`, `implements`, `verified_by`, `depends_on`, `conflicts_with`, `references`. The default follows `parent_of`, `implements` and `verified_by`. `references` (an ID merely mentioned in text) is followed only when listed in `--kinds`. Each line shows how it was reached and the source lines.

## Coverage

`specs-index coverage` lists: requirements without an implementing design, designs without a verifying scenario or criterion, scenarios verifying nothing, dangling IDs (referenced but never defined), duplicate IDs, conflicting requirements, and, when an embedder is configured, candidate conflicts from vector similarity (skipped above 5,000 embedded chunks or with `--no-candidates`; a note says so).

## Embeddings are optional

With no embedder, everything except `--mode vector` / `--mode hybrid` works. The default provider is a local Ollama (`embeddinggemma`). Cloud providers are used only when `allow_cloud` is true in the config or `--allow-cloud` is passed to `index`; never enable that without the user asking. `specs-index doctor` checks the index and the embedder; `specs-index config` shows the effective configuration.
