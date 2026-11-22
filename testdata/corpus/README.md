# Fixture corpus

A fictional "document portal" product. Every parser, indexer, search, trace and
coverage test is checked against `truth.json`, loaded through
`internal/corpus` (`corpus.Dir()`, `corpus.LoadTruth()`). All content is
invented.

## Layout

- `docs/brd.md`: 25 requirements, heading form (`## BRD-nnn Title`), field
  lines `Status`/`Priority`/`Parent`/`Depends on`/`Conflicts with`.
- `docs/fds.md`: 15 designs as a table (`Mapped BRDs`); FDS-003, 005, 008 and
  010 are also `###` headings with bodies.
- `docs/acceptance.md`: 10 acceptance criteria as a table with an `FDS` column.
- `features/*.feature`: 8 files, 30 scenarios, one Scenario Outline with
  Examples (`upload.feature`), one Rule (`versions.feature`), one Background
  (`bulk_upload.feature`), tags `@smoke` and `@wip`, ID tags at feature, rule
  and scenario level, two `@id:` overrides (`SCN-101`, `SCN-102`).

## truth.json

- `entities`: `key`, `type`, `path` (corpus-relative, forward slashes),
  `start_line`, `end_line` (1-based, inclusive), `title`, and for scenarios
  `step_lines`.
- `edges`: explicit edges `src`, `dst`, `kind`, `origin` (`field`, `table`,
  `tag` or `mention`, as in `model.Edge.Origin`) and `path` (corpus-relative
  file where the edge is written: `.feature` for `tag`, `.md` otherwise), with the design's directions:
  `Parent: X` on Y gives `X parent_of Y`; `Mapped BRDs` gives `FDS implements
  BRD`; an AC table's `FDS` column gives `FDS verified_by AC`; an ID tag on a
  feature, rule or scenario gives `<tagged key> verified_by <scenario>` for
  every scenario it covers; a prose or inline-code ID gives `references` from
  the entity containing it. Feature entities carry no edges of their own.
- `uncovered`: the deliberate gaps (below). `designs_without_scenario` means
  no `verified_by` edge at all; the corpus has no design verified only by an AC.
- `queries`: `expect_top` keys must appear in the top 5 hits, `expect_absent`
  keys must not appear in the hits at all.

## Keys

Requirements, designs and criteria use their ID. A scenario uses
`<corpus-relative path>#<scenario name>`, or the `@id:SCN-nnn` value when the
tag is present. A feature uses its bare corpus-relative path (the feature
name is in `title`). Rule and Background
blocks are structural and have no entity entry.

## Span convention

- Heading entity: the heading line through the line before the next heading of
  the same or higher level (or end of file), trailing blank lines excluded.
  Lines inside fenced code blocks are not headings.
- Table-row entity: the row line.
- A heading and a table row with the same ID in one file merge; heading wins,
  so the entity has one entry with the heading span (FDS-003, 005, 008, 010).
- Scenario: first tag line (the keyword line if untagged) through its last step
  or Examples row. Tag lines of a `Rule` or of the `Feature` are not part of a
  scenario span; `Background` is not part of any scenario.
- Feature: first tag line (or `Feature:` line) through the last non-blank line.
- `step_lines`: lines of the scenario's own `Given/When/Then/And/But/*` steps,
  excluding Background steps, Examples rows and doc strings.

## Deliberate cases

- 3 requirements without a design: BRD-015, BRD-019, BRD-023.
- 2 designs without a scenario or AC: FDS-014, FDS-015.
- 1 dangling ID: BRD-099 (`Depends on` in BRD-025).
- 1 duplicate ID across two files: AC-010 (`docs/acceptance.md` row and a
  `###` heading in `docs/fds.md`); both appear in `entities`.
- 1 explicit `Conflicts with` pair: BRD-013 to BRD-014 (written on BRD-013 only).
- FDS-005 maps 3 BRDs; BRD-022 is mapped by FDS-013 and FDS-014 (BRD-006 and
  BRD-007 are also mapped twice).
- Parent chain BRD-001, BRD-002, BRD-003, BRD-004.
- Code handling: BRD-020 has a fenced block holding BRD-777, FDS-777 and a
  `## BRD-888` heading, all ignored; BRD-018 and FDS-003 mention an ID in
  inline code, which is a `references` edge.
- A scenario linked to nothing: `features/access.feature#Warn before a session times out`.
