---
description: Register files or directories of requirement and design documents as sources of the index
argument-hint: PATH...
---

Run this command with the Bash tool and show its output to the user as it is printed:

```
sh "${CLAUDE_PLUGIN_ROOT}/bin/specs-index" add $ARGUMENTS
```

Each path must be inside the project root (the nearest ancestor of the working directory that has a .git, else the working directory). Registering a source does not index it: run /specs-index:index next. If the command fails, report the error text unchanged. Source files are only read.
