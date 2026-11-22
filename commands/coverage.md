---
description: List requirements without designs, designs without scenarios, orphan scenarios, and dangling, duplicate and conflicting IDs
argument-hint: [--no-candidates]
---

Run this command with the Bash tool and show its output to the user as it is printed:

```
sh "${CLAUDE_PLUGIN_ROOT}/bin/specs-index" coverage $ARGUMENTS
```

Only explicit links count towards coverage. Run /specs-index:index first if files changed since the last index.
