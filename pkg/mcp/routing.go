package mcp

// AnchoredRoutingBlock is the single source of truth for how the agent should
// treat anchored at runtime. It is consumed in TWO places that need to stay
// in lockstep:
//
//   1. Server.handleInitialize → returned in the MCP `initialize` response as
//      the `instructions` field, so any MCP-compatible client gets the
//      guidance during the handshake.
//   2. cmd/anchored/hook_sessionstart.go and hook_userpromptsubmit.go →
//      injected via Claude Code's `additionalContext` so the same guidance
//      survives compaction and is re-applied on every prompt.
//
// Mirrors the structure context-mode uses for its <context_window_protection>
// block: XML-tagged sections so the model can scan them quickly and so the
// content survives token-trimming better than free prose.
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

  <session_continuity>
    Decisions and preferences saved via anchored_save remain authoritative across sessions and tools. When the user contradicts a stored fact, prefer anchored_update over creating a duplicate; when they revoke one, use anchored_forget. Don't drop these directives as the conversation grows.
  </session_continuity>

  <forbidden>
    NEVER save secrets, credentials, tokens, or PII.
    NEVER narrate the search/save — just do it and let results inform the answer.
  </forbidden>
</anchored_memory>`
