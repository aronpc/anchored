---
name: anchored
description: |
  Use Anchored memory tools (anchored_context, anchored_search, anchored_save, kg_query, kg_add) to
  remember the user across sessions and IDEs. Triggers: "do you remember", "what did we decide",
  "as we discussed", "the X we set up", "how do we usually", "our convention", "I always",
  "I never", "from now on", "going forward", "settled on", "decided", "TIL", "got bit by",
  "lição aprendida", "vimos que", "fechamos em", "vamos com", "TODO", "next up",
  "deployed", "incident", "post-mortem", project/service/team/stack questions, naming choices,
  architecture questions, requests that depend on prior context. Also triggers on every
  conversation start (call anchored_context first).
---

# Anchored: Persistent Cross-Tool Memory

## Mandatory at session start

Call `anchored_context` first, before any other tool, on every new conversation. It returns
identity, project context, recent decisions, and relevant memories accumulated from every
AI tool and IDE the user works in. Without it you have no continuity.

Re-call when the user changes directories or shifts to a different project.

## Search before answering domain questions

Quietly call `anchored_search` before answering questions about the user's projects, services,
decisions, conventions, or preferences. Let the result inform your reply. Don't narrate the
search — the user expects you to remember, not to recite the act of remembering.

## Save high-signal knowledge as it emerges

Call `anchored_save` when durable, non-obvious knowledge appears:

- **fact** — stable truth about user/team/stack ("we run Go 1.22 on ARM")
- **preference** — recurring choice ("I always pin deps", "prefer small PRs")
- **decision** — directional or architectural choice ("settled on Postgres")
- **event** — something that happened ("deployed v2", "merged #421")
- **learning** — non-obvious lesson ("TIL X", "got bit by Y", post-mortem insight)
- **plan** — intent to do something ("TODO: migrate", "next up: refactor")
- **summary** — consolidated recap ("daily recap", "sprint summary")

Always pick a category explicitly. Never save secrets, credentials, or session-only state.

## Capture relationships in the knowledge graph

Call `kg_add` when the user reveals structural facts: "X depends on Y", "service A deployed
on B", "team T owns service S". The graph compounds across sessions and complements prose
memory.

Call `kg_query` when the user names a specific entity (project, service, repo, person, API,
library) and you need its edges, not just prose mentions.

## Quality over quantity

Skip casual remarks, ephemeral task state, and anything inferable from the codebase. A few
high-signal entries beat a flood of low-value ones. The user has paid for the memory; use it
actively, but don't turn it into a logger.
