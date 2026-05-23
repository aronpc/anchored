# Anchored Improvements Roadmap

Roadmap for improving the current local Anchored product before and alongside Team Sync / Anchored Cloud.

The goal is to keep Anchored's current value proposition intact — local-first, single binary, low-ops, fast memory retrieval — while adding the primitives needed for teams, privacy, curation, and cloud sync later.

---

## Principles

- Preserve the local-first experience. Anchored must keep working without cloud, accounts, or network access.
- Do not leak user-local data. Paths, usernames, local environment details, access patterns, and preferences stay local unless explicitly shared.
- Improve the local product first. Cloud/team sync should reuse solid local primitives, not bypass them.
- Keep the binary simple. Prefer Go stdlib and existing patterns before adding dependencies.
- Make memory inspectable. Users should be able to see, edit, delete, and understand what Anchored knows.

---

## P0 — Sync-Ready Foundation ✅

These changes unblock future Team Sync and also improve the current local product.

### Stable Project Identity ✅

Problem: local `project_id` values can differ across machines for the same repository.

Implemented in `pkg/project/detector.go`:

- `RemoteKey` derived from git remote URL via SHA-256 hash.
- Auto-computes and backfills for existing projects.
- Repos without git remotes stay local-only.
- Two machines with the same git remote derive the same remote project key.
- Local filesystem paths are never used as remote identity.

### Sync Metadata Columns ✅

Problem: local memories have no sync state.

Implemented via SQLite migration:

- `sync_dirty BOOLEAN DEFAULT FALSE` on `memories` table.
- `sync_origin TEXT DEFAULT 'local'` on `memories` table.
- `author TEXT` on `memories` table.
- `remote_project_key TEXT` on `memories` table.
- `sync_state` table with `project_id`, `watermark`, `last_sync`, `client_id`.

### Service Observer Hooks ✅

Problem: `Service.Save`, `Update`, and `Forget` have no clean extension point for sync, audit, or future side effects.

Implemented `MemoryObserver` interface:

```go
type MemoryObserver interface {
    OnMemorySaved(ctx context.Context, m Memory)
    OnMemoryUpdated(ctx context.Context, m Memory)
    OnMemoryDeleted(ctx context.Context, id string, projectID *string)
}
```

- Optional and non-blocking. Observer failures never break local operations.
- `ListOptions` extended with `Source` and `IncludeDeleted` filters.

---

## P1 — Privacy and Preference Model ✅

These changes make the product safer and clarify what is personal vs shared.

### Preference Scope ✅

Problem: `preference` currently mixes personal preferences, project conventions, and team rules.

Implemented in `anchored_save`:

- `scope` parameter accepts `user`, `project`, or `team`.
- Defaults to `user` for preferences.
- Stored in metadata JSON.
- Existing preference searches still work.
- Sync filters can block personal preferences while allowing explicit project/team preferences.

### Remote-Safe Content Filter ✅

Problem: sanitizer redacts secrets, but sync/cloud also needs to block local paths and personal environment details.

Implemented in `pkg/sync/filter.go`:

- `RemoteSafetyFilter` detects and flags:
  - `/home/<user>/...`, `/Users/<user>/...`, `C:\Users\<user>\...`
  - home-relative paths with personal context
  - secrets and personal preferences
- Local memory save remains allowed; only remote push is blocked.

### Configurable Sanitizer Patterns ✅

Problem: config supports sanitizer patterns conceptually, but custom patterns should be first-class for companies.

Implemented in `NewSanitizer`:

- `SanitizerConfig` accepts custom patterns.
- User-defined patterns redact content before local save and before remote push.

---

## P2 — Local UX and Trust ✅

These changes help users understand and curate what Anchored knows.

### Memory Inspection CLI ✅

Problem: users need confidence in the memory store before trusting sync/cloud.

Implemented:

- `anchored inspect <id>` shows full memory details as JSON with all metadata.
- `anchored export` with `--project`, `--category`, `--source`, `--include-deleted`, `--format json|jsonl`, `--output` flags.
- Embeddings excluded from export.
- Users can inspect exactly what would be synced.

### Interactive Configuration Wizard

Problem: `anchored config set <key> <value>` is useful for scripts but awkward for first-time users and for broader configuration review.

Plan:

- Add an interactive terminal wizard:

```bash
anchored config wizard
anchored config interactive
```

- Show the current value for each setting.
- Let the user press Enter to keep the current value.
- Group prompts by subsystem:
  - memory storage
  - embeddings
  - search
  - sanitizer
  - context optimizer
  - dream
  - debug/plugin
- Ask for final confirmation before writing `~/.anchored/config.yaml`.
- Keep `anchored config show` and `anchored config set` unchanged for non-interactive usage.

Acceptance:

- Running `anchored config wizard` can create or update `~/.anchored/config.yaml` without editing YAML manually.
- Existing `anchored config` behavior remains backward-compatible.
- Invalid numeric/boolean inputs re-prompt instead of writing broken config.

### Sync Preview ✅

Problem: before enabling cloud/team sync, users should see what would leave the machine.

Implemented:

- `anchored remote preview` classifies memories as syncable/blocked/needs_review.
- No network requests. Fully offline.
- Output clearly separates syncable, blocked, and needs-review memories with counts and sample IDs.

### Memory Provenance

Problem: team/cloud views need to show where a memory came from.

Plan:

- Normalize source metadata:
  - tool: Claude Code, Cursor, OpenCode, CLI, import
  - source type: manual, hook, import, sync
  - author: local account/user when configured
- Avoid storing local paths as provenance for remote records.

Acceptance:

- Memories can show “added by X via Claude Code” without leaking local machine details.

---

## P3 — Search and Context Quality

These changes improve the core experience independent of cloud.

### Project Context Ranking

Problem: project-scoped memories should dominate when working inside a repo, but relevant global preferences still matter.

Plan:

- Keep project boost behavior.
- Add explicit result labels:
  - local personal
  - local project
  - remote project
  - global
- Tune category diversity for `anchored_context`.

Acceptance:

- `anchored_context` surfaces project facts/decisions before generic memories.
- User preferences remain visible but do not crowd out project decisions.

### Preference Retrieval Layer

Problem: user preferences are valuable but should not pollute project facts.

Plan:

- Add a dedicated preference retrieval section in context output.
- Keep it small and stable.
- Prefer explicit preferences over inferred ones.

Acceptance:

- Context output visibly separates “Your preferences” from “Project knowledge”.

### Remote-Origin Labeling

Problem: once sync exists, agents need to know whether a memory came from the current user or the team.

Plan:

- Add display metadata for remote-origin memories.
- In context/search output, mark team-shared memories as such.

Acceptance:

- Search results can show whether a memory is local-only or team-shared.

---

## P4 — Dream and Curation (partially complete)

These changes keep the memory base useful over time.

### Dream Dry Run

Problem: automatic cleanup is risky without review.

Plan:

- Make dream analysis produce a review report first:
  - duplicates
  - contradictions
  - stale memories
  - suggested merges
  - category corrections
- No destructive action by default.

Acceptance:

- `anchored dream --dry-run` produces actionable suggestions without modifying DB.

### Manual Apply ✅

Problem: users need controlled cleanup.

Implemented:

- `anchored dream --apply <action-id>` applies a single dream action.
- Dedup actions soft-delete (not hard delete).
- Contradiction actions rejected, requiring manual review.
- Audit trail preserved in metadata.

### Team Dream Compatibility

Problem: cloud/server dream should reuse local logic later.

Plan:

- Keep dream algorithms pure Go and store-agnostic where possible.
- Separate analysis from mutation.

Acceptance:

- The same dedup/contradiction logic can run against local SQLite and future server Postgres adapters.

---

## P5 — Remote / Cloud Readiness (partially complete)

These changes connect the local product to the future remote layer.

### Remote Config ✅

Implemented in config:

```yaml
remote:
  enabled: false
  server_url: ""
  api_key: ""
  projects: []
```

CLI support implemented:

```bash
anchored remote status    # Show current remote config (offline)
anchored remote preview   # Preview what would sync (offline)
```

### Minimal Sync Client (in progress)

`pkg/sync/client.go` being implemented:

- HTTP client with Push/Pull DTOs.
- Safety-validated payloads.
- Still requires `anchored_oss` server endpoint for end-to-end testing.

---

## Recommended Order

1. ~~Stable project identity~~ ✅
2. ~~Privacy/safety filter for remote-eligible content~~ ✅
3. ~~Preference scope metadata~~ ✅
4. ~~Service observer hooks~~ ✅
5. ~~Sync metadata migration~~ ✅
6. ~~Memory inspection and sync preview CLI~~ ✅
7. ~~Remote config~~ ✅
8. Minimal `pkg/sync` dry-run client (in progress)
9. Dream dry-run report
10. Team Sync server implementation (`anchored_oss`)

Items 1-7 are complete. Item 8 is in progress. Items 9-10 are pending.
