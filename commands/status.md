---
description: Show the registered sources, index counts and the configured embedder
---

Run this command with the Bash tool and show its output to the user as it is printed:

```
sh "${CLAUDE_PLUGIN_ROOT}/bin/specs-index" status $ARGUMENTS
```

If the output says there is no index yet, suggest /specs-index:add and /specs-index:index.
