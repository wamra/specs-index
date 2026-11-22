---
description: Search requirements, designs and scenarios by text, meaning, ID or filters
argument-hint: QUERY [--mode auto|exact|fts|vector|hybrid] [--k 10]
---

Run this command with the Bash tool and show its output to the user as it is printed:

```
sh "${CLAUDE_PLUGIN_ROOT}/bin/specs-index" search $ARGUMENTS
```

Quote the query as one shell argument. Filters go inside the query: `type:` `status:` `priority:` `tag:` `path:` `has:scenario` `has:fds`, each negated with a leading `-`. A query that starts with `-` needs `--` before it. A hit marked stale was indexed before its file changed: suggest /specs-index:index. Use the specs-search skill for the full query grammar.
