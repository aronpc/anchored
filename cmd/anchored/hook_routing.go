package main

// AnchoredRoutingBlock is the system-reminder injected by the SessionStart and
// UserPromptSubmit hooks so Claude (and every other MCP client that respects
// `additionalContext`) treats anchored as the persistent memory layer instead
// of waiting for the user to ask. Mirrors the structure context-mode uses for
// its own routing block (XML-tagged so it survives compaction-friendly).
const AnchoredRoutingBlock = `<anchored_memory>
  <priority_instructions>
    Anchored is the user's persistent cross-tool memory, shared across every IDE and AI tool. Use it silently — don't narrate "let me check memory" or "saving this". The user expects you to remember.
  </priority_instructions>

  <call_first>
    On every new conversation: anchored_context(cwd=...) — loads identity, project, recent decisions. Re-call only when the project changes.
  </call_first>

  <search_triggers>
    Quietly call anchored_search BEFORE answering when:
    - User references prior context: "do you remember", "as we discussed", "we decided", "lembra", "como fizemos", "nossa convenção".
    - User asks about a project, service, repo, decision, preference, or convention.
    - You're about to make an architectural choice or recommend a tool — search first to honor existing decisions.
    - User names a specific entity (project / service / library / person) — also call anchored_kg_query for structured edges.
  </search_triggers>

  <save_triggers>
    Call anchored_save (and anchored_kg_add for "X depends_on Y" / "owns" / "deployed_on" facts) PROACTIVELY — without waiting for "remember this" — when durable, non-obvious knowledge emerges:
    - fact — stable truth ("we run Go 1.22 on ARM").
    - preference — recurring choice ("I always pin deps").
    - decision — directional choice ("settled on Postgres").
    - event — point-in-time happening ("deployed v2 today", "merged #421").
    - learning — non-obvious lesson ("TIL", "got bit by", "lição aprendida").
    - plan — intent ("TODO: migrate", "next up: refactor").
    - summary — consolidated recap.
    Pick the category explicitly. Skip ephemerals and anything inferable from the codebase.
  </save_triggers>

  <forbidden>
    NEVER save secrets, credentials, tokens, or PII.
    NEVER narrate the search/save — just do it and let results inform the answer.
  </forbidden>
</anchored_memory>`
