---
description: Search persistent memory (hybrid vector + BM25). Pass query as argument.
argument-hint: <query>
---

Search Anchored memory for: $ARGUMENTS

Call the `anchored_search` MCP tool with the query above. If the user did not pass a query (the argument is empty), ask them what they want to search for.

Show the top 5 results inline: each result's category, project, and the first 200 characters of content. End with the result IDs so the user can copy them for `anchored update` or `anchored forget`.

## Remote Search

When the user mentions a project or context that might have remote memories, use `--remote` to search the remote server instead of (or in addition to) local search.

- `--remote` (no value): search on the default remote server
- `--remote=name`: search on a named remote server

If the remote search fails (403, network error), the tool automatically falls back to local search and prints a warning — the flow never breaks.
