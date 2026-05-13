# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run

```bash
make build              # Build binary to bin/anchored (injects VERSION via ldflags)
make test               # Run all tests with CGO flags (FTS5 required)
make lint               # golangci-lint (also needs CGO flags)
make clean              # Remove bin/
make sync-version       # Bump VERSION file and sync into plugin manifests via cmd/version-sync
./bin/anchored serve    # Start MCP server on STDIO
```

Run a single test or package:
```bash
CGO_CFLAGS="-DSQLITE_ENABLE_FTS5" CGO_LDFLAGS="-lm" go test ./pkg/memory/ -v -run TestHybridSearch
```

**macOS note:** `-lm` is implicit, the Makefile strips it automatically.

## Version

Single source of truth is the `VERSION` file. Build injects it via `-ldflags -X main.Version`. Bump flow: update `VERSION` → `make sync-version` → commit both.

## Architecture

Go 1.24, single binary, CGO required (go-sqlite3 with FTS5). No daemon — the binary runs on-demand via MCP STDIO.

### Package layout

```
cmd/anchored/          # CLI entry point + all subcommands (serve, search, save, import, hooks, etc.)
cmd/version-sync/      # Helper that syncs VERSION into plugin manifests
pkg/config/            # Config struct + YAML loading (config.yaml)
pkg/memory/            # Core memory service — SQLite store, hybrid search, embeddings, categorizer, sanitizer
pkg/mcp/               # MCP protocol types (JSON-RPC), server routing, tool handlers
pkg/context/           # Context optimizer — sandbox code execution, URL fetcher, FTS5 indexer, batch executor
pkg/kg/                # Knowledge graph — bitemporal triples, pattern-based entity/relation extraction
pkg/dream/             # Memory consolidation — dedup, contradiction detection, merging
pkg/importer/          # Import from Claude Code JSONL, OpenCode SQLite, Cursor .mdc, DevClaw
pkg/session/           # Session tracking (start/end/persistence)
pkg/project/           # Project detection from cwd
pkg/updater/           # Background auto-updater (checks GitHub releases on startup)
pkg/debuglog/          # Optional NDJSON event log (env: ANCHORED_DEBUG, ANCHORED_DEBUG_PATH)
```

### Key data flows

1. **MCP request lifecycle:** `cmd/anchored/serve.go` creates `memory.Service` → `mcp.Server` reads JSON-RPC from stdin → routes to tool handler → handler calls into `memory.Service` / `kg.KG` / `context.Optimizer`.

2. **Memory storage:** Content → credential sanitizer → auto-categorizer (regex, PT+EN) → ONNX embedder (384-dim, paraphrase-multilingual-MiniLM-L12-v2) → SQLite (FTS5 + vector cache) + knowledge graph triple extraction.

3. **Hybrid search:** Query → entity detection → query expansion → parallel vector similarity + BM25 FTS5 → RRF fusion with entity/project boost → MMR re-ranking.

4. **Context optimizer (sandbox tools):** `pkg/context/` composes Store, Sandbox, Chunker, Searcher, Indexer, Fetcher, BatchExecutor, Evictor into a facade. The MCP layer wraps it behind an `OptimizerFacade` interface (with a Windows no-op shim in `server_ctx.go`).

5. **Plugin system:** `cmd/anchored/hook_sessionstart.go` detects binary/plugin version drift. If `plugin.auto_update` is enabled in config, it fast-forwards the marketplace mirror and clears stale cache.

### Routing block

`pkg/mcp/routing.go` contains `AnchoredRoutingBlock` — the XML-tagged instruction set injected into MCP `initialize` responses and Claude Code hooks. Any changes to memory behavior directives must update this single constant.

### Storage

`~/.anchored/data/anchored.db` (SQLite with FTS5 + vector cache + KG tables). Embedding model at `~/.anchored/data/onnx/`. Schema migrations in `pkg/memory/sqlite_migrations.go`.

### Build tags

`//go:build !windows` on context optimizer facade (`pkg/mcp/server_ctx.go`, `pkg/context/optimizer.go`) — the sandbox doesn't compile on Windows. Platform-specific plugin sync: `plugin_sync_unix.go` / `plugin_sync_windows.go`.
