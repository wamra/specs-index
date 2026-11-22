---
description: Follow links from a requirement, design or scenario down to what implements and verifies it, or up to what it verifies
argument-hint: KEY [--direction down|up|both] [--depth 5] [--kinds a,b] [--include-inferred]
---

Run this command with the Bash tool and show its output to the user as it is printed:

```
sh "${CLAUDE_PLUGIN_ROOT}/bin/specs-index" trace $ARGUMENTS
```

KEY is an entity key such as `BRD-004`, as printed by search. By default only explicit `parent_of`, `implements` and `verified_by` links are followed; inferred links are shown only with `--include-inferred` and never count as coverage.
