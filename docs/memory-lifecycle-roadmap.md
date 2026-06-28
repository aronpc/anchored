# Anchored Memory Lifecycle Roadmap

## Purpose

Anchored local must remain the complete memory runtime for one developer and
all their AI tools. Anchored OSS is an optional shared project/team sync layer,
not a dependency for day-to-day context retrieval.

This plan adds memory lifecycle primitives learned from `ai-memory` without
adopting a wiki model. SQLite remains the local source of truth, hybrid local
retrieval remains the hot path, and remote sync remains privacy-first.

## Design principles

- **Local complete first.** Anchored must work fully offline with no remote
  server, no Docker, and no required LLM provider.
- **Remote as replication, not retrieval hot path.** `anchored_context` should
  read from the local SQLite/FTS/vector/knowledge-graph indexes. Sync pulls
  remote memories into the local store before agents need them.
- **No wiki source of truth.** Markdown/wiki export may exist later as a
  derived view, but not as primary storage.
- **Metadata evolves compatibly.** Existing memories with old or empty metadata
  must continue to parse and behave as they do today.
- **Privacy by default.** Personal preferences, local paths, secrets, raw
  session traces, and machine-local operational state stay local unless an
  explicit project/team-safe path exists.
- **Small context, good ranking.** Every new feature must improve context
  quality without bloating token budgets.

## Current baseline

Anchored already has:

- SQLite source of truth with FTS5 and local embedding cache.
- Categories: `fact`, `preference`, `decision`, `event`, `learning`, `plan`,
  `summary`.
- Local context stack (`anchored_context`) and search (`anchored_search`).
- Knowledge graph primitives.
- Preference scope metadata (`user`, `project`, `team`).
- Remote safety filtering for local paths, secrets, and personal preferences.
- Sync metadata fields and a minimal sync client for `anchored_oss`.
- Dream/consolidation foundations.

The missing piece is a common lifecycle model that lets local storage, context
ranking, dream, retention, and remote sync make consistent decisions.

## Memory metadata v2

Extend `memory.MemoryMetadata` as the common local/remote contract. Keep all
fields optional and preserve old JSON.

```json
{
  "source": "user",
  "session_id": "...",
  "preference_scope": "project",
  "memory_type": "semantic",
  "kind": "decision",
  "scope": "project",
  "origin": "manual",
  "importance": 0.82,
  "pinned": false,
  "expires_at": null,
  "supersedes": ["old-memory-id"],
  "consolidates": ["memory-a", "memory-b"],
  "context_tier": "L1",
  "confidence": 0.9
}
```

### Field semantics

| Field | Values | Purpose |
|---|---|---|
| `memory_type` | `semantic`, `episodic`, `operational` | Lifecycle class independent from category. |
| `kind` | `fact`, `decision`, `learning`, `summary`, `rule`, `handoff`, `precompact`, etc. | More precise purpose. |
| `scope` | `user`, `project`, `team` | Shareability boundary. |
| `origin` | `manual`, `hook`, `bootstrap`, `dream`, `remote`, `handoff`, `precompact`, `import` | Where the memory came from. |
| `importance` | `0.0..1.0` | Ranking and retention hint. |
| `pinned` | bool | Exempt from retention and demotion. |
| `expires_at` | RFC3339 timestamp | TTL for operational/episodic items. |
| `supersedes` | string array | Memories this item replaces. |
| `consolidates` | string array | Memories merged into this item. |
| `context_tier` | `L0`, `L1`, `L2` | Context stack hint. |
| `confidence` | `0.0..1.0` | Useful for bootstrap/import/inferred items. |

### Default mapping

When metadata is absent, infer behavior from existing category:

| Category | Default `memory_type` | Default `scope` | Notes |
|---|---|---|---|
| `fact` | `semantic` | `project` when project-bound, else `user` | Syncable if project/team-safe. |
| `decision` | `semantic` | `project` when project-bound, else `user` | High context value. |
| `learning` | `semantic` | `project` when project-bound, else `user` | High context value. |
| `plan` | `semantic` or `operational` | project/user | Operational when session-specific. |
| `summary` | `semantic` or `operational` | project/user | Operational for handoff/precompact. |
| `event` | `episodic` | `user` | Local-only by default. |
| `preference` | `semantic` | `user` by default | Already handled by `preference_scope`. |

## Feature plan

### Phase 1 — Metadata v2 foundations

Changes:

- Extend `MemoryMetadata` with optional v2 fields.
- Update `ParseMetadata`, `ToAny`, tests, and JSON round-trip behavior.
- Add normalizers for `memory_type`, `scope`, `origin`, and `context_tier`.
- Keep `preference_scope` backward-compatible and do not rename existing JSON.
- Add helper methods:
  - `IsSemantic()`
  - `IsOperational()`
  - `IsExpired(now)`
  - `IsRemoteSyncCandidate()`

Compatibility:

- Old metadata maps must parse as before.
- Unknown metadata keys must be preserved.
- Existing `preference_scope` tests must continue passing.
- No schema migration required for the first implementation because metadata is
  already JSON-compatible.

Acceptance criteria:

- Existing memories keep old behavior.
- New v2 metadata can be saved, listed, exported, and inspected.
- `anchored inspect` shows v2 metadata without losing unknown fields.

### Phase 2 — Sync preview v2

Changes:

- Update remote preview classification to read v2 metadata.
- Block local-only lifecycle classes:
  - `scope=user`
  - `memory_type=episodic`
  - `kind=precompact`
  - `origin=precompact`
  - personal `preference`
  - `event`
- Allow project/team-safe semantic items:
  - `memory_type=semantic`
  - `scope=project|team`
  - `category=fact|decision|learning|plan|summary`
- Preserve current local path, secret, and preference checks.

Compatibility:

- Memories without v2 metadata use category-based fallback.
- Existing remote preview JSON shape remains stable; add optional reason values
  only.
- Existing sync client continues working against current `anchored_oss` compat
  endpoints.

Acceptance criteria:

- Preview explains lifecycle rejections clearly.
- No personal/episodic/precompact data is pushed by default.
- Bootstrap/dream semantic project memories can be syncable.

### Phase 3 — Handoff local runtime

Purpose: make it easy to stop in one tool and resume in another without large
token context.

Data model:

```json
{
  "memory_type": "operational",
  "kind": "handoff",
  "origin": "handoff",
  "scope": "project",
  "expires_at": "...",
  "context_tier": "L1"
}
```

Content shape:

```text
Summary: ...
Current goal: ...
Next steps:
- ...
Open questions:
- ...
Blocked on:
- ...
Files touched:
- ...
```

Changes:

- Add CLI/MCP support for explicit handoff creation.
- Optionally create handoffs from SessionEnd hooks.
- Teach `anchored_context` to surface the latest non-expired handoff for the
  current project in L1.
- Add config gates for automatic handoff generation.

Compatibility:

- Handoffs are stored as normal memories with metadata.
- No new required category is needed; use `summary` or `plan`.
- Existing clients that do not know handoff metadata simply see a summary.

Remote behavior:

- Local-only by default.
- Syncable only when `scope=project|team`, content passes safety filters, and
  future OSS policy allows `kind=handoff`.

### Phase 4 — PreCompact recovery snapshots

Purpose: save a short operational snapshot before an agent loses working
context.

Changes:

- Hook PreCompact saves a compact `summary` memory with:
  - active goal
  - relevant files
  - decisions made
  - failed approaches
  - next action
  - blockers
- Metadata:
  - `memory_type=operational`
  - `kind=precompact`
  - `origin=precompact`
  - short `expires_at`
- `anchored_context` may include the latest precompact snapshot only when it is
  recent and relevant to the current project/session.

Compatibility:

- PreCompact snapshots are normal memories.
- They are blocked from remote sync by default.
- Existing hooks may keep writing older snapshot metadata; parser must accept
  both forms.

### Phase 5 — Bootstrap without wiki

Purpose: remove cold start for existing projects.

Command:

```text
anchored bootstrap [--dry-run] [--force] [--source readme,docs,git,rules,tree] [--sync-preview]
```

Sources:

- `README.md`
- `docs/`
- `CLAUDE.md`, `AGENTS.md`, `GEMINI.md`
- `CHANGELOG.md`
- recent `git log`
- top-level directory/module structure
- package/module headers where cheap to read

Output:

- Normal SQLite memories only.
- Suggested categories: `fact`, `decision`, `learning`, `summary`.
- Metadata:
  - `origin=bootstrap`
  - `memory_type=semantic`
  - `scope=project`
  - `confidence=<score>`

Compatibility:

- Bootstrap must never overwrite existing memories unless `--force` is explicit.
- Content hashing/dedup should avoid duplicate seeds.
- No LLM required for initial version: use deterministic extraction and
  categorization. Optional LLM summarization can be added later behind config.

### Phase 6 — Context optimizer v2

Use lifecycle metadata to reduce token usage and improve relevance.

Ranking hints:

- Boost:
  - `pinned=true`
  - current project
  - `memory_type=semantic`
  - `kind=decision|learning|rule`
  - recent `kind=handoff|precompact`
  - high `importance`
  - access count / recency when useful
- Penalize:
  - expired operational memories
  - old episodic memories
  - low-confidence bootstrap memories
  - superseded memories unless explicitly requested

Context stack:

- L0: identity + core project facts/rules.
- L1: current-project decisions, learnings, latest handoff/precompact.
- L2: query-dependent retrieval.

Compatibility:

- When metadata is absent, fallback to current category/project ranking.
- Context output format should remain stable enough for existing tools.

### Phase 7 — Dream with lineage

Extend dream from dedup/consolidation into explicit memory lineage.

Actions:

- `merge`
- `supersede`
- `archive`
- `mark_rule_candidate`
- `promote_importance`
- `demote_ephemeral`

Metadata updates:

- New memory gets `consolidates=[...]`.
- Replacement memory gets `supersedes=[...]`.
- Superseded memories remain queryable through `anchored history <id>` but are
  penalized in normal context.

Compatibility:

- Do not hard-delete superseded memories.
- Existing dream actions must continue to work.
- Export and inspect should expose lineage.

### Phase 8 — Local retention sweep

Command:

```text
anchored retention sweep [--dry-run]
```

Default policy:

| Type | Default behavior |
|---|---|
| `semantic` | keep |
| `episodic` | decay/archive by age and access |
| `operational` | expire by TTL |
| `pinned` | never expire |
| remote/team memories | conservative; do not delete without explicit policy |

Config sketch:

```yaml
retention:
  enabled: true
  operational_ttl_days: 14
  episodic_ttl_days: 90
  preserve_pinned: true
  dry_run_default: true
```

Compatibility:

- First release should be dry-run oriented.
- Use soft-delete/archive, not hard delete.
- Never delete pinned or remote-origin memories automatically.

## Coordinated sync model

Local remains the hot path:

```text
agent/tool -> anchored local -> SQLite/FTS/vector/KG -> small context
                       |
                       +-- periodic/manual sync <-> anchored_oss
```

Sync eligibility should be computed locally before any network call. Remote
responses should be pulled into local SQLite and indexed locally before they are
used in `anchored_context`.

## Backward compatibility checklist

- Existing metadata parses without migration.
- Existing categories remain valid.
- Existing MCP tools keep their request/response shapes unless adding optional
  fields.
- Existing sync compat endpoints continue to work.
- Remote preview remains safe when metadata is absent.
- Local context retrieval works with old memories and new v2 memories.
- Dream/retention never hard-delete user data by default.
- Any new automatic capture is configurable and conservative.

## Suggested milestone order

1. Metadata v2 parser + tests.
2. Sync preview lifecycle rules.
3. `anchored_oss` metadata round-trip support.
4. Handoff local runtime.
5. PreCompact snapshots.
6. Bootstrap.
7. Context optimizer v2.
8. Dream lineage.
9. Retention sweep.
