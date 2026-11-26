---
description: Show the registered sources, index counts and the configured embedder
---

Call the specs-index MCP tool `status`, or run the CLI with the Bash tool:

```
sh "${CLAUDE_PLUGIN_ROOT}/bin/specs-index" status
```

This command takes no arguments: ignore any text the user added. If the output says there is no index yet, suggest /specs-index:add and /specs-index:index.
