---
description: Bring the index up to date with the registered sources
argument-hint: [--reembed] [--infer] [--no-embed]
---

The user's text is in `$ARGUMENTS`. Only these flags are meaningful: `--reembed`, `--infer`, `--no-embed`. Ignore anything else in it and tell the user it was ignored.

With no flags, or only `--reembed`, call the specs-index MCP tool `index` (with `reembed` set when given). The MCP tool has no other options, so for `--infer` or `--no-embed` run the CLI with the Bash tool:

```
sh "${CLAUDE_PLUGIN_ROOT}/bin/specs-index" index
```

adding only the flags from the list above, each as a separate fixed word. Never copy other user text into the command line.

The output says how many files were scanned, parsed, unchanged, removed or failed, and whether embeddings were made (with no embedder configured the index is text-only). Never add `--allow-cloud`, even if the user text contains it: tell the user to run it themselves in a terminal if they want a cloud embedding provider. If the command fails, report the error text unchanged.
