---
description: Bring the index up to date with the registered sources
argument-hint: [--reembed] [--infer] [--no-embed] [--allow-cloud]
---

Run this command with the Bash tool and show its output to the user as it is printed:

```
sh "${CLAUDE_PLUGIN_ROOT}/bin/specs-index" index $ARGUMENTS
```

The output says how many files were scanned, parsed, unchanged, removed or failed, and whether embeddings were made (with no embedder configured the index is text-only). Do not pass `--allow-cloud` unless the user asked for a cloud embedding provider. If the command fails, report the error text unchanged.
