# specs-index

Product Requirements & Functional Design Specification | v0.2 | Draft

A lightweight, production-grade, embeddable indexing and retrieval engine for BRD, FDS and Gherkin specifications, distributed as a Claude Code plugin in the Wammy-plugins marketplace.

## 1. Scope and principles

- Index local Markdown BRD/FDS documents and Gherkin .feature files; preserve source files as authoritative.
- Support exact, keyword, semantic, filtered and graph-traversal queries, with bidirectional traceability.
- Ship a prebuilt runtime to professional consumers: no required npm install, compilation, Python, Docker or WSL.
- Operate offline with a local embedding provider, or use explicitly authorized AWS Bedrock embeddings.
- Maintain a Claude-independent core with a thin Claude Code plugin/MCP interface.

## 2. Business requirements (BRD)

| BRD ID | Business requirement | Priority |
|---|---|---|
| BRD-001 | Add/remove/synchronize specification sources | P0 |
| BRD-002 | Parse Markdown BRD, FDS and Gherkin scenarios | P0 |
| BRD-003 | Persist semantic embeddings | P0 |
| BRD-004 | Hybrid semantic, full-text and exact-ID retrieval | P0 |
| BRD-005 | Complex Boolean, exclusion and metadata queries | P0 |
| BRD-006 | Traverse BRD–FDS–scenario relationships in both directions | P0 |
| BRD-007 | Support hierarchies and many-to-many mappings | P0 |
| BRD-008 | Incremental indexing with change detection | P0 |
| BRD-009 | Support local and Bedrock embedding providers | P0 |
| BRD-010 | Distribute via Wammy-plugins marketplace | P0 |
| BRD-011 | Prepackaged cross-platform minimal runtime | P0 |
| BRD-012 | Claude Code commands, skills and MCP tools | P0 |
| BRD-013 | Source-grounded results with provenance | P0 |
| BRD-014 | Mapping gaps and consistency analysis | P1 |
| BRD-015 | Reproducible performance and quality benchmarks | P1 |

## 3. Functional design and mappings (FDS)

| FDS ID | Functional design | Mapped BRDs |
|---|---|---|
| FDS-001 | Ingest files, parse headings/tables/IDs and Gherkin feature/scenario/steps | BRD-001, BRD-002, BRD-013 |
| FDS-002 | Pluggable embedding adapters; persist model identity and vector dimension | BRD-003, BRD-009 |
| FDS-003 | Hybrid retrieval with exact-ID precedence, full-text and vector ranking | BRD-004, BRD-005 |
| FDS-004 | Typed graph edges and bounded forward/reverse traversal | BRD-006, BRD-007 |
| FDS-005 | Hash-based incremental updates and removal of stale entries | BRD-001, BRD-008 |
| FDS-006 | Prebuilt CLI/MCP runtime and marketplace plugin packaging | BRD-010, BRD-011, BRD-012 |
| FDS-007 | Provenance, coverage reports and candidate conflict flags | BRD-013, BRD-014 |
| FDS-008 | Comparable three-configuration benchmark runner | BRD-015 |
| FDS-009 | Approved-root filesystem access and explicit cloud egress controls | BRD-009, BRD-011, BRD-013 |

## 4. Information model

| Entity | Fields / semantics |
|---|---|
| Document | ID, path, type, version, content hash |
| Requirement | Stable ID, title, body, status, heading, source span |
| Scenario | Feature, scenario/outline ID, tags, steps, source span |
| Relationship | Source ID, target ID, typed edge, provenance, explicit/inferred flag |
| Embedding | Entity ID, model/version, dimension, vector |
| Index | Schema version, indexed timestamp, provider configuration |

Supported edges: parent_of, implements, verified_by, depends_on, conflicts_with and references. Explicit mappings remain authoritative; inferred links are suggestions only.

## 5. Query and plugin interface

| Interface | Behavior |
|---|---|
| /specs-index:add | Register source files or directories |
| /specs-index:index | Build or incrementally refresh index |
| /specs-index:search | Semantic/keyword/exact search with filters and exclusions |
| /specs-index:trace | Traverse mappings, including reverse traversal |
| /specs-index:coverage | Find missing BRD/FDS/scenario links |
| /specs-index:status | Inspect index, model and provider |

Expose equivalent typed MCP operations for agent workflows. Example: approved BRDs about OAuth without linked Gherkin scenarios; all FDS and scenarios reachable from BRD-042.

## 6. Runtime and deployment

- Implementation preference: TypeScript core, with interchangeable SQLite and Orama adapters.
- Default evaluation baseline: SQLite + FTS5 + vec1; benchmark against Orama.
- Ship signed/versioned platform-specific artifacts for Windows, macOS and Linux; no WSL dependency.
- Scope index and filesystem operations to approved project roots; require permission for external embedding calls.
- Persist model metadata; re-embed rather than mixing vectors across incompatible models.

## 7. Benchmark matrix

| Config | Retrieval engine | Embedding |
|---|---|---|
| A | SQLite + FTS5 + vec1 | Amazon Titan Text V2 |
| B | SQLite + FTS5 + vec1 | EmbeddingGemma 2 |
| C | OramaJS | Qwen3-Embedding 0.6B |

Use identical source corpus, query set, mapping ground truth and chunking rules where possible. Report Recall@5, MRR@10, exact-ID Accuracy@1, mapping recall, p50/p95 latency, RAM, index size and embedding cost. Evaluate contradiction detection separately from retrieval.

## 8. Acceptance criteria

| AC ID | Acceptance test | FDS |
|---|---|---|
| AC-001 | Representative BRD/FDS/Gherkin fixtures parse with stable IDs and source spans | FDS-001 |
| AC-002 | Local and Bedrock embeddings can be indexed and searched independently | FDS-002 |
| AC-003 | Exact, semantic, Boolean and excluded-tag queries pass labeled tests | FDS-003 |
| AC-004 | Forward/reverse traversal agrees with mapping ground truth | FDS-004 |
| AC-005 | Unchanged files are not re-embedded; deleted files are removed from index | FDS-005 |
| AC-006 | Clean install and use on supported platforms without build tools | FDS-006 |
| AC-007 | Results contain provenance; unmapped requirements are correctly reported | FDS-007 |
| AC-008 | Benchmark outputs measured results for all three configurations | FDS-008 |
| AC-009 | Unapproved filesystem roots and cloud requests are rejected | FDS-009 |

## 9. Open design decision

Source write-back is not specified for the MVP. Proposed default: read-only toward BRD/FDS/Gherkin source files, while allowing add/remove/index operations in the derived index. Future mapping maintenance may be separately authorized.
