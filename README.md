# Anchored

> Persistent cross-tool memory for AI coding agents. Local-first, single binary, zero dependencies.

[![License: MIT](https://img.shields.io/badge/license-MIT-green?style=for-the-badge)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.24+-00ADD8?style=for-the-badge&logo=go)]
[![Release](https://img.shields.io/github/v/release/jholhewres/anchored?style=for-the-badge)](https://github.com/jholhewres/anchored/releases)

Anchored is a local-first MCP memory server that gives every AI coding agent and IDE you use a shared, persistent memory on your machine. Install once, and Claude Code, Cursor, OpenCode, Gemini CLI, and any other MCP-compatible tool read, write, and search the same knowledge base.

No API keys. No daemon. All embeddings run locally.

For team-shared project memory, the planned self-hosted/open distribution lives in [`anchored_oss`](../anchored_oss): organization-owned projects, team permissions, remote sync, privacy guardrails, and a future cloud-compatible protocol.

## Features

- **Cross-tool memory** — one knowledge base, every AI tool and IDE shares it
- **Multilingual embeddings** — `paraphrase-multilingual-MiniLM-L12-v2` (50+ languages, PT-BR and EN parity)
- **Hybrid search** — RRF fusion of vector similarity (384-dim ONNX) and BM25 (FTS5), with entity and project boost
- **Knowledge graph** — automatic pattern-based extraction of entities and relationships (no LLM needed)
- **Smart categorization** — bilingual PT+EN regex auto-classifies memories into 7 categories (fact, preference, decision, event, learning, plan, summary)
- **Memory stack** — L0 identity + L1 essential stories + L2 on-demand retrieval, budget-enforced (~900 tokens)
- **Sandbox tools** — `anchored_execute`/`anchored_execute_file` run code in 11 languages with stdout-only capture, hardened env, FILE_PATH/FILE_CONTENT injection
- **Knowledge indexing** — `anchored_fetch_and_index` mirrors URLs to a local FTS5 store; sandbox keeps raw data out of context
- **Background auto-updater** — checks GitHub releases on startup; new binary atomically replaced, current process unaffected
- **Credential redaction** — regex-based secret sanitization before storage
- **Multi-source import** — Claude Code (JSONL), OpenCode (SQLite), Cursor (.mdc rules), DevClaw
- **Team sync ready** — design work for self-hosted and cloud-compatible shared project memory lives in `docs/team-sync.md` and the sibling `anchored_oss` repo

## Install

From [GitHub Releases](https://github.com/jholhewres/anchored/releases):

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/jholhewres/anchored/main/install/install.sh | bash
```

From source:

```bash
git clone https://github.com/jholhewres/anchored.git
cd anchored && make build
sudo cp bin/anchored /usr/local/bin/
```

First run auto-downloads the embedding model (~470MB) and creates `~/.anchored/`.

## Setup

### Claude Code (plugin)

The fastest path. Installs the MCP server, six `/anchored:*` slash commands, and an auto-trigger skill in one step:

```
/plugin marketplace add jholhewres/anchored
/plugin install anchored@anchored
```

Then restart Claude Code. From any project: `/anchored:context`, `/anchored:search <query>`, `/anchored:save <content>`, `/anchored:stats`, `/anchored:doctor`, `/anchored:purge`. The skill triggers `anchored_*` tools proactively when memory is relevant — no need to ask.

### Claude Code (MCP only, no slash commands)

```bash
claude mcp add -s user anchored anchored
```

The `-s user` flag registers Anchored at user scope so it's available in every project. Without it, `claude mcp add` defaults to local. Entry lives at `~/.claude.json` (not `~/.claude/mcp.json`). Restart Claude Code to pick up the new server.

### Other tools

Run `anchored init` to auto-detect and register, or configure manually:

| Tool | Config file |
|---|---|
| Cursor | `~/.cursor/mcp.json` |
| OpenCode | `~/.config/opencode/opencode.json` |
| Gemini CLI | `~/.gemini/settings.json` |
| VS Code Copilot | `.vscode/mcp.json` |

```json
{
  "mcpServers": {
    "anchored": {
      "command": "anchored"
    }
  }
}
```

## CLI

```
anchored                    Start MCP server (STDIO, default when no arg)
anchored serve              Start MCP server (STDIO)
anchored init [--tool]      Auto-detect tools and register MCP
anchored doctor             Diagnose installation, config, MCP registration
anchored stats              Show memory statistics

anchored search <query>     Search memories
anchored save <content>     Save a memory (auto-categorized if --category omitted)
anchored update <id>        Update a memory
anchored forget <id>        Remove a memory (soft delete; --hard for permanent)
anchored list               List memories

anchored import [sources]   Import memories from detected sources
anchored identity [edit]    View or edit identity file
anchored config [show|set|wizard] View or modify configuration

anchored dream              Analyze and consolidate duplicate memories
anchored precompact         Pre-compact memory context
anchored hook <subcommand>  Run session continuity hooks
anchored purge              Wipe memories (--hard for full DB reset with backup)
```

Import sources: `claude-code` `devclaw` `opencode` `cursor` `all`

## MCP Tools

**Memory**

| Tool | When to use |
|---|---|
| `anchored_context` | First call of every conversation — loads identity, project, recent decisions |
| `anchored_search` | Before answering domain questions (hybrid vector + BM25) |
| `anchored_save` | Capture facts, preferences, decisions, learnings (category required) |
| `anchored_update` | Revise an existing memory in place |
| `anchored_forget` | Remove a memory (soft delete by default) |
| `anchored_list` | List memories by category, project, or time |
| `anchored_stats` | Memory overview |
| `anchored_session_end` | Close a tracked session |
| `kg_query` | Query knowledge-graph relationships for an entity |
| `kg_add` | Capture a relationship (subject — predicate — object) |

**Sandbox / index** (context-saving tools that keep raw data out of context)

| Tool | When to use |
|---|---|
| `anchored_execute` | Run code in 11 languages; only stdout enters context |
| `anchored_execute_file` | Process a file; `FILE_PATH` and `FILE_CONTENT` auto-injected |
| `anchored_batch_execute` | Run multiple commands and run search queries in one call |
| `anchored_index` | Index documentation into FTS5 knowledge base |
| `anchored_ctx_search` | Search indexed content with batched queries |
| `anchored_fetch_and_index` | Fetch URL → markdown → index (`force=true` bypasses 24h cache) |

## How it works

- **Hybrid search** — RRF fusion of vector similarity (ONNX, multilingual) and BM25 (FTS5), with entity boost and project boost
- **Entity detection** — extracts project names, tools, and topics from queries to boost relevant results
- **Topic change detection** — identifies conversation shifts and increases retrieval diversity
- **Memory stack** — L0 identity + L1 essential stories + L2 on-demand, budget-enforced
- **Knowledge graph** — bitemporal triples with functional predicates and alias resolution, auto-extracted from memory text
- **Credential redaction** — regex-based secret sanitization before storage

## Storage

```
~/.anchored/
├── data/
│   ├── anchored.db        # SQLite (FTS5 + vector cache + knowledge graph)
│   └── onnx/              # local embedding model (~470MB)
└── config.yaml
```

No daemon. No ports. The binary runs on demand via MCP STDIO.

## Docs

- [Design](docs/design.md) — memory stack, hybrid search, knowledge graph, quantization
- [Architecture](docs/architecture.md) — project structure and implementation details
- [Embedding Model](docs/embedding-model.md) — model choice, quantization, inference pipeline
- [Import Sources](docs/import-sources.md) — how each tool's data is parsed
- [Team Sync](docs/team-sync.md) — planned local + remote architecture for team-shared memory via `anchored_oss`
- [Improvements Roadmap](docs/improvements-roadmap.md) — local-first roadmap before/alongside Team Sync and Cloud
- [Changelog](CHANGELOG.md) — version history

## Related Projects

- [`anchored_oss`](../anchored_oss) — planned self-hosted/open team sync server for organizations, teams, project memory, policies, and remote guardrails.

## License

[MIT](LICENSE)
