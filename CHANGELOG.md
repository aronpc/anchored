# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [0.4.3] - 2026-05-06

### Changed

- **Single source of truth for routing guidance** — the `<anchored_memory>` routing block now lives in `pkg/mcp/routing.go` (`AnchoredRoutingBlock`) and is consumed in two places that used to drift: the MCP `initialize.instructions` field and the SessionStart / UserPromptSubmit hook payloads. Pure-MCP clients (no hook support) and Claude Code-style clients (with `additionalContext`) now receive the same guidance text via different channels — no duplication, no contradictions.
- **Routing block gained `<session_continuity>`** — explicit reminder that decisions/preferences saved via `anchored_save` remain authoritative across sessions and tools, that contradictions should prefer `anchored_update` over duplicates, and that revocations use `anchored_forget`. Mirrors the equivalent section in context-mode's routing block.

## [0.4.2] - 2026-05-06

### Added

- **SessionStart + UserPromptSubmit hooks** — the Claude Code plugin now ships `hooks/hooks.json` wired to `anchored hook sessionstart` (injects the `<anchored_memory>` routing block plus a project-scoped recap of recent decisions/events at conversation start) and `anchored hook userpromptsubmit` (re-injects the routing block on every prompt so the reminder survives compaction). Result: the agent calls `anchored_context` / `anchored_search` / `anchored_save` proactively without the user having to ask.
- **Hook subcommand expansion** — new `anchored hook userpromptsubmit` (Claude Code contract `{hookSpecificOutput:{hookEventName,additionalContext}}`); the existing `anchored hook sessionstart` now emits the same contract instead of an opaque `{resume_context,...}` blob.
- **Cursor + OpenCode sample configs** — `configs/cursor/{mcp.json,hooks.json}` and `configs/opencode/opencode.json` register the `anchored` MCP server and route SessionStart/UserPromptSubmit/PreCompact equivalents to the same hook subcommands. `configs/README.md` walks through install per IDE.

### Changed

- **`kg_query` → `anchored_kg_query` / `kg_add` → `anchored_kg_add`** — namespaced under the `anchored_` prefix so the knowledge-graph tools sit alongside `anchored_search` / `anchored_save` instead of appearing as orphan top-level tools. The legacy `kg_query` / `kg_add` names still dispatch to the same handlers (no breaking change for older clients), but new clients should use the prefixed names. Tool descriptions and the MCP `initialize.instructions` text were updated accordingly.

## [0.4.1] - 2026-05-05

### Added

- **`anchored_fetch_and_index` multi-URL batch** — accepts `requests: [{url, source}, ...]` plus optional `concurrency: 1-8` to fan out HTML→markdown→index across several URLs in one call. Per-URL failures are reported in the response (no abort). Single-URL `url`/`source` form is preserved.
- **`anchored_batch_execute` parallel commands** — optional `concurrency: 1-8` runs sandbox commands in parallel for I/O-bound batches. Result order matches input order regardless of concurrency. Defaults to sequential.
- **`anchored_ctx_search` content-type filter** — optional `content_type: 'code' | 'prose'` narrows hits to source-code chunks or prose chunks. Empty (default) preserves prior behavior.
- **`anchored_ctx_search` progressive throttling** — calls 1-3 return normal `limit`, 4-8 are clamped to 1 result/query with a "fold into batch" warning, 9+ are blocked and redirect to `anchored_batch_execute`. Counter resets whenever `anchored_batch_execute`, `anchored_fetch_and_index`, or `anchored_index` repopulates the corpus.

### Changed

- **Sandbox tool descriptions sharpened for routing** — `anchored_execute`, `anchored_execute_file`, `anchored_batch_execute`, `anchored_ctx_search`, `anchored_fetch_and_index`, and `anchored_index` now lead with explicit "USE INSTEAD OF Bash/Read/WebFetch" guidance and position `anchored_batch_execute` as the primary research tool / `anchored_ctx_search` as the follow-up tool, so models pick the sandbox path even when no external routing hooks are present.

## [0.4.0] - 2026-05-05

### Added

- **Claude Code plugin** — installable via `/plugin marketplace add jholhewres/anchored` and `/plugin install anchored@anchored`. Bundles 6 slash commands (`/anchored:context`, `/anchored:search`, `/anchored:save`, `/anchored:stats`, `/anchored:doctor`, `/anchored:purge`), an auto-triggered skill, and the MCP server registration in one install.
- **`anchored doctor`** — diagnostic checklist: binary version, ONNX model + tokenizer, FTS5/WAL, embedding coverage, MCP registration in Claude Code (`~/.claude.json`), Cursor, OpenCode, identity file. Each failure prints the exact fix command.
- **`anchored purge`** — wipe memories. Default is soft-delete (recoverable for 30 days); `--hard` backs the DB up to `~/.anchored/data/anchored.db.YYYY-MM-DD-HHMMSS.bak` and resets to a fresh schema.
- **Categorizer expansion** — ~25 new bilingual (PT+EN) regex patterns covering `learning` (was previously broken at 6/23K entries), `decision` ("vamos com", "settled on", "going forward"), `preference` ("I always", "minha preferência"), `plan` ("TODO", "next up", "preciso de"), and `event` ("merged", "shipped", "deployed"). Plan now runs before decision so "Next up: refactor" wins over "refactor". Unit tests cover each pattern.

### Changed

- **`anchored_save` requires `category`** — moved from optional to required in the MCP tool inputSchema. The description lists every category with examples so the LLM picks intentionally instead of relying on regex auto-detect. Service-level fallback to `Categorize()` is preserved if a client passes empty (no breaking change for older callers).
- **Tool descriptions rewritten for proactive but discreet usage** — `anchored_context`, `anchored_search`, `anchored_save`, `kg_query`, `kg_add`, `anchored_update`, `anchored_forget` now lead with explicit trigger conditions and examples. `Instructions` field on `initialize` reframed: "use memory silently, don't narrate the search, quality over quantity". Generic across every IDE / AI tool.

### Fixed

- **`anchored_execute_file` was non-functional** — the `path` argument was logged and dropped; user code never received `FILE_PATH` or `FILE_CONTENT`. Now injects a language-specific prelude exposing both variables (JavaScript, TypeScript, Python, Shell, Ruby, Go, Rust, PHP, Perl, R, Elixir).
- **`anchored_execute` env hardening** — host environment is now sanitized before launching the sandbox subprocess: ~40 hijack-prone vars stripped (`LD_PRELOAD`, `BASH_ENV`, `PYTHONSTARTUP`, `RUBYOPT`, `GIT_SSH_COMMAND`, `NODE_OPTIONS`, …) and forced sandbox vars added (`PYTHONUNBUFFERED=1`, `NO_COLOR=1`, `TERM=dumb`).
- **`anchored_fetch_and_index` gains `force` parameter** — bypass the 24h URL cache to re-fetch fresh content. Defaults to false for backward compatibility.
- **`purge --hard` data-safety** — `copyFile` now does explicit `Sync()` + checked `Close()`, removing the partial backup if anything fails. Pre-backup `PRAGMA wal_checkpoint(TRUNCATE)` ensures the `.bak` is self-contained even if another process holds the DB open.
- **Categorizer false positives reduced** — bare `pattern` and `design` keywords removed from the `decision` regex; "design patterns in Python" no longer becomes a decision. Plan-pattern word boundaries tightened.

### Docs

- README setup makes `claude mcp add -s user anchored anchored` explicit. Without `-s user` Claude Code registers at local scope (current project only); user scope makes Anchored available in every project. Also documents that running sessions must be restarted to see newly-added MCP servers.

## [0.3.3] - 2026-05-05

### Added

- **Auto-updater** — `anchored serve` now checks GitHub releases on startup, downloads the matching tarball if a newer version is available, and atomically replaces `~/.anchored/bin/anchored`. The check runs in a background goroutine and never blocks the MCP handshake. Only triggers for binaries living under the canonical install dir, so dev builds are never overwritten. Disable via `ANCHORED_NO_AUTOUPDATE=1`. The currently running process keeps its in-memory image; the new binary activates on the next MCP spawn.

## [0.3.2] - 2026-05-05

### Fixed

- **Claude Code MCP registration** — `anchored init --tool claude-code` now writes to `~/.claude.json` (the actual file Claude Code reads) instead of the non-existent `~/.claude/mcp.json`. Anchored was silently invisible to Claude Code while working in OpenCode/Cursor.
- **Backup before merge** — `registerMCP` now writes a `.bak` copy before overwriting any existing tool config, protecting user state (e.g., the 200KB+ payload Claude Code keeps in `~/.claude.json`).

### Docs

- README setup section corrected: shows `claude mcp add anchored anchored` as the canonical Claude Code install path and clarifies the actual config-file locations for each tool.

## [0.2.0] - 2025-04-30

### Added

- **Vector cache** (T1): in-memory RAM cache of all embedding vectors for sub-millisecond similarity search
- **PreTrainedTokenizerFast** (T3): full HuggingFace `tokenizer.json` support with BPE/WordPiece, normalizer pipeline, and automatic fallback to WordPiece (`vocab.txt`)
- **Model swap** (T4): switch from `all-MiniLM-L6-v2` to `paraphrase-multilingual-MiniLM-L12-v2` with automatic cache migration
- **Embedding cache migration** (T5): lazy re-embedding when model changes, old cache entries auto-invalidated
- **OpenCode importer** (T6): SQL-based import from `opencode.db` (sessions, messages, parts, todos)
- **Cursor rules importer** (T7): `.mdc` file parsing with YAML frontmatter (description, globs)
- **Incremental import tracking** (T8): `imports` table with delta sync per source (mtime, SHA-256, timestamps)
- **Entity detector** (T9): regex-based entity extraction from queries using project/keyword/content snapshots, with cached TTL
- **Topic change detector** (T10): detects conversation topic shifts to trigger broader, more diverse retrieval
- **Essential stories layer** (T11, L1): deterministic per-project summary template (top facts, decisions, events, preferences) with 6h SQLite cache
- **On-demand layer** (T12, L2): entity-driven FTS5 retrieval with category diversification and budget enforcement
- **Stack telemetry** (T21): atomic counters for L0/L1/L2 byte counts, L1 cache hit/miss stats
- **Memory indexer** (T16): heading-aware markdown chunking with SHA-256 delta sync and polling-based file watching
- **KG extractor** (T17): automatic pattern-based entity and relationship extraction on every save, rate-limited to 5 triples
- **Credential redaction** (T18): expanded regex patterns for API keys, tokens, passwords, SSH keys, and generic secrets
- **CLI refactoring** (T19): single `main.go` split into 11 separate command files with shared helpers
- **Config management** (T20): `anchored config show|set` with dotted key support
- **CHANGELOG.md** (T22): this file

### Changed

- Embedding model upgraded from `all-MiniLM-L6-v2` (English-only, 512 tokens) to `paraphrase-multilingual-MiniLM-L12-v2` (50+ languages, 128 tokens, 384 dims)
- Hybrid search now includes entity boost (1.1x) and project boost
- Memory stack L1 replaced generic project layer with deterministic essential stories
- Memory stack L2 now uses entity detection + topic change detection instead of simple regex lookup
- Import pipeline now uses `paraphrase-multilingual-MiniLM-L12-v2` instead of `bge-small-en-v1.5`
- Claude Code importer rewritten with robust multi-line JSON parsing and session summaries
- CLI split from single `main.go` (332 lines) into 11 files (~50-130 lines each)

### Fixed

- Download timeout increased from 5min to 10min for ~470MB model downloads
- HTTP resume support for interrupted model downloads (Range header)
- Duplicate map key compile errors in query expansion stop words
