---
type: fds
version: 0.3
---
# Document portal functional design

| FDS ID | Functional design | Mapped BRDs |
|--------|-------------------|-------------|
| FDS-001 | Upload endpoint and storage | BRD-001, BRD-002 |
| FDS-002 | Chunked bulk upload with resume | BRD-003, BRD-004 |
| FDS-003 | Full-text index builder | BRD-006 |
| FDS-004 | Search filter parser | BRD-007 |
| FDS-005 | Search results page and ranking | BRD-005, BRD-006, BRD-007 |
| FDS-006 | Version store and restore | BRD-008, BRD-009 |
| FDS-007 | Corporate identity provider connector | BRD-010, BRD-011 |
| FDS-008 | Role permission evaluator | BRD-012 |
| FDS-009 | Share link service | BRD-013, BRD-014 |
| FDS-010 | Preview renderer | BRD-018 |
| FDS-011 | Retention scheduler and holds | BRD-016, BRD-017 |
| FDS-012 | Notification and digest dispatcher | BRD-020, BRD-021 |
| FDS-013 | Folder export to archive | BRD-022 |
| FDS-014 | Folder export to PDF bundle | BRD-022 |
| FDS-015 | Accessibility checker | BRD-024, BRD-025 |

## Design notes

### FDS-003 Full-text index builder
The builder tokenises document text on upload and feeds the ranking in `FDS-005`.

### FDS-005 Search results page and ranking
Results are ordered by relevance and fall back to recency, see BRD-008 for the version timestamps used.

### FDS-008 Role permission evaluator
The evaluator resolves a user's groups and returns the strongest role for a folder.

### FDS-010 Preview renderer
The renderer converts each page to an image and caches the result for a day.

## Appendix

### AC-010 Export archive checksum
The archive must carry a checksum file so a recipient can verify it.
