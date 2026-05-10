package mcp

import (
	"sort"
)

func ToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "anchored_context",
			Description: "First call of every new conversation. Returns identity, project summary, recent decisions, and durable memories from this user across every AI tool and IDE. Re-call when project (cwd) changes. Example: user says \"can you help with the auth flow?\" → call anchored_context(cwd=\".\") before reading code.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cwd": map[string]any{
						"type":        "string",
						"description": "Current working directory for project detection",
					},
					"session_id": map[string]any{
						"type":        "string",
						"description": "Optional session ID for tracking",
					},
				},
				"required": []string{"cwd"},
			},
		},
		{
			Name:        "anchored_search",
			Description: "Search persistent memory before answering. Use silently — never narrate \"let me check memory\". Triggers: user mentions past work (\"like we discussed\", \"how did we...\"), prior decisions, conventions, preferences, or names a project/service/library. Also: any time you're about to recommend a tool/architecture — search first to honor existing decisions. Hybrid vector + BM25, milliseconds. Examples: User \"como decidimos lidar com X?\" → anchored_search(query=\"X\"); User \"add auth to this service\" → anchored_search(query=\"auth convention\").",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Natural language search query",
					},
					"cwd": map[string]any{
						"type":        "string",
						"description": "Current working directory for project-scoped search. When omitted, searches globally across all projects.",
					},
					"category": map[string]any{
						"type":        "string",
						"description": "Filter by category: fact, preference, decision, event, learning, plan, summary",
						"enum":        []string{"fact", "preference", "decision", "event", "learning", "plan", "summary"},
					},
					"max_results": map[string]any{
						"type":        "integer",
						"description": "Maximum results to return (default: 20)",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "anchored_save",
			Description: "Capture durable knowledge into persistent cross-tool memory. CALL PROACTIVELY when high-signal information emerges; do not wait for the user to say \"remember this\". You MUST pick a category — picking wrong is better than skipping, and quality categorization beats keyword auto-detect. Categories:\n• fact — stable truth about user/team/stack/system (\"we run Go 1.22 on ARM\", \"the API lives at api.example.com\")\n• preference — recurring choice the user makes (\"I always pin deps\", \"prefer small PRs\")\n• decision — architectural or directional choice (\"settled on Postgres\", \"going forward, no co-author trailers\")\n• event — something that happened at a point in time (\"deployed v2 today\", \"merged #421\", \"meeting at 14h\")\n• learning — non-obvious lesson or post-mortem insight (\"TIL X\", \"got bit by Y\", \"causa raiz foi Z\")\n• plan — intent to do something (\"TODO: migrate\", \"next up: refactor\")\n• summary — consolidated recap (\"daily recap\", \"sprint summary\")\n\nDO NOT save: ephemeral task state, basic engineering trivia, anything inferable from the codebase, secrets/credentials. Auto-detects project from cwd. Content is sanitized for tokens/keys before storage.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content": map[string]any{
						"type":        "string",
						"description": "The memory content to save (concise, self-contained)",
					},
					"category": map[string]any{
						"type":        "string",
						"description": "Pick the best fit. Falls back to keyword-based auto-detect if you pass an empty string, but explicit selection is strongly preferred.",
						"enum":        []string{"fact", "preference", "decision", "event", "learning", "plan", "summary"},
					},
					"cwd": map[string]any{
						"type":        "string",
						"description": "Current working directory for project detection",
					},
				},
				"required": []string{"content", "category"},
			},
		},
		{
			Name:        "anchored_list",
			Description: "List memories by category, project, or time range. Returns paginated results with metadata.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cwd": map[string]any{
						"type":        "string",
						"description": "Current working directory for project-scoped listing. When omitted, lists from all projects.",
					},
					"category": map[string]any{
						"type":        "string",
						"description": "Filter by category",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum results (default: 20)",
					},
				},
			},
		},
		{
			Name:        "anchored_update",
			Description: "Revise an existing memory in place. TRIGGER when the user corrects a stored fact (\"actually it's X, not Y\"), updates a decision (\"we changed our mind, now we use Z\"), or refines a preference. ALWAYS prefer this over creating a duplicate — search first to find the ID, then update. Preserves ID, project, source, and creation date. Re-embeds when content changes.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "The memory ID to update",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "New content (optional — only update if provided)",
					},
					"category": map[string]any{
						"type":        "string",
						"description": "New category (optional — only update if provided)",
						"enum":        []string{"fact", "preference", "decision", "event", "learning", "plan"},
					},
					"cwd": map[string]any{
						"type":        "string",
						"description": "Current working directory",
					},
				},
				"required": []string{"id"},
			},
		},
		{
			Name:        "anchored_forget",
			Description: "Remove a memory. TRIGGER when the user says \"forget that\", \"that's no longer true\", \"we don't do X anymore\", or asks to delete a specific stored fact. Find the ID via anchored_search first. Soft-deletes by default (recoverable for 30 days); use hard=true only when the user explicitly requests permanent deletion.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "The memory ID to delete",
					},
					"hard": map[string]any{
						"type":        "boolean",
						"description": "Permanently delete (default: false, soft delete)",
					},
					"cwd": map[string]any{
						"type":        "string",
						"description": "Current working directory for project context",
					},
				},
				"required": []string{"id"},
			},
		},
		{
			Name:        "anchored_stats",
			Description: "Show memory statistics: total memories, per-project counts, import status.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "anchored_session_end",
			Description: "End a tracked session. Call when a conversation ends to properly close the session.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session_id": map[string]any{
						"type":        "string",
						"description": "The session ID to end",
					},
					"summary": map[string]any{
						"type":        "string",
						"description": "Optional session summary to save as a memory",
					},
				},
				"required": []string{"session_id"},
			},
		},
		{
			Name:        "anchored_kg_query",
			Description: "Query the knowledge graph for an entity's relationships. Use IN ADDITION to anchored_search whenever the user names a specific project, service, repo, person, API, library, or environment — anchored_kg_query returns structured edges (depends_on, deployed_on, owns, uses) that prose search misses. Example triggers: \"how does X integrate with Y?\", \"what's the relationship between A and B?\", \"who owns service X?\", \"what depends on this library?\". Cheap; pair with anchored_search for full picture.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"entity": map[string]any{
						"type":        "string",
						"description": "Entity name to query",
					},
					"cwd": map[string]any{
						"type":        "string",
						"description": "Current working directory for project-scoped query",
					},
				},
				"required": []string{"entity"},
			},
		},
		{
			Name:        "anchored_kg_add",
			Description: "Capture a relationship into the knowledge graph. CALL PROACTIVELY when the user reveals a structural fact about their stack: \"X depends on Y\", \"service A is deployed on B\", \"repo X uses library Y\", \"team T owns service S\", \"X integrates with Y via Z\". The graph compounds across sessions and complements prose memory — both should be populated as facts emerge. Do not wait for explicit instructions. Subject/predicate/object should be short noun phrases; predicate uses snake_case (uses, depends_on, deployed_on, owns, integrates_with).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"subject": map[string]any{
						"type":        "string",
						"description": "Subject entity name",
					},
					"predicate": map[string]any{
						"type":        "string",
						"description": "Relationship type (e.g., uses, depends_on, deployed_on)",
					},
					"object": map[string]any{
						"type":        "string",
						"description": "Object entity name",
					},
					"cwd": map[string]any{
						"type":        "string",
						"description": "Current working directory for project-scoped relationship",
					},
				},
				"required": []string{"subject", "predicate", "object"},
			},
		},
		{
			Name:        "anchored_execute",
			Description: "Run code in a sandboxed subprocess so raw output never floods context — only stdout enters the conversation. USE INSTEAD OF Bash whenever a command may produce >20 lines (logs, JSON, build output, test runs, git log, large diffs, API responses, data processing, dependency trees, security audits). Bash is for short, deterministic operations only (git/mkdir/rm/mv/navigation). Pair with `intent` so output >5KB is auto-indexed and only matching sections return. Languages: javascript, typescript, python, shell, ruby, go, rust, php, perl, r, elixir.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"language": map[string]any{
						"type":        "string",
						"description": "Runtime language",
						"enum":        []string{"javascript", "typescript", "python", "shell", "ruby", "go", "rust", "php", "perl", "r", "elixir"},
					},
					"code": map[string]any{
						"type":        "string",
						"description": "Source code to execute. Use console.log (JS/TS), print (Python/Ruby/Perl/R), echo (Shell), fmt.Println (Go), IO.puts (Elixir) to output a summary to context.",
					},
					"timeout": map[string]any{
						"type":        "integer",
						"description": "Max execution time in ms (default: 30000)",
						"default":     30000,
					},
					"intent": map[string]any{
						"type":        "string",
						"description": "What you're looking for in the output. When provided and output is large (>5KB), indexes output and returns only matching sections.",
					},
					"cwd": map[string]any{
						"type":        "string",
						"description": "Current working directory for project scoping",
					},
				},
				"required": []string{"language", "code"},
			},
		},
		{
			Name:        "anchored_execute_file",
			Description: "Process a file in the sandbox without loading its contents into context. USE INSTEAD OF Read when analyzing or exploring a file (logs, large JSON, CSVs, build artifacts, page snapshots, accessibility trees) — Read is only correct when you intend to Edit. Two variables are auto-injected before your code runs: FILE_PATH (absolute path) and FILE_CONTENT (UTF-8 text). Use them directly — don't repeat the read. Only your printed stdout enters context. Languages: javascript, typescript, python, shell, ruby, go, rust, php, perl, r, elixir.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Absolute file path or relative to project root",
					},
					"language": map[string]any{
						"type":        "string",
						"description": "Runtime language",
						"enum":        []string{"javascript", "typescript", "python", "shell", "ruby", "go", "rust", "php", "perl", "r", "elixir"},
					},
					"code": map[string]any{
						"type":        "string",
						"description": "Code to process the file at FILE_PATH. Print summary via console.log/print/echo/IO.puts.",
					},
					"timeout": map[string]any{
						"type":        "integer",
						"description": "Max execution time in ms (default: 30000)",
						"default":     30000,
					},
					"intent": map[string]any{
						"type":        "string",
						"description": "What you're looking for in the output.",
					},
					"cwd": map[string]any{
						"type":        "string",
						"description": "Current working directory for project scoping",
					},
				},
				"required": []string{"path", "language", "code"},
			},
		},
		{
			Name:        "anchored_batch_execute",
			Description: "PRIMARY TOOL FOR RESEARCH. Runs multiple commands, auto-indexes ALL output into the sandbox knowledge base, and answers multiple search queries in ONE call — replaces many individual Bash/Read/execute steps. Use first whenever you'd otherwise chain investigative commands (git status + git log + git diff, ls + find + grep, build + test + lint, multi-endpoint API probes, codebase stats). Pass `concurrency` (1-8) for I/O-bound batches to fan out commands in parallel; defaults to 1 (sequential). Returns only the search hits, not raw output. Batch ALL questions you have into the queries array — follow-ups should reuse anchored_ctx_search against the same indexed corpus.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"commands": map[string]any{
						"type":        "array",
						"description": "Commands to execute as a batch",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"label":    map[string]any{"type": "string", "description": "Section header for this command's output"},
								"command":  map[string]any{"type": "string", "description": "Shell command to execute"},
								"language": map[string]any{"type": "string", "description": "Runtime language (default: shell)"},
							},
							"required": []string{"command"},
						},
					},
					"queries": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Search queries to extract information from indexed output. Batch ALL questions in one call.",
					},
					"timeout": map[string]any{
						"type":        "integer",
						"description": "Max execution time in ms (default: 60000)",
						"default":     60000,
					},
					"intent": map[string]any{
						"type":        "string",
						"description": "What you're looking for in the output. Use specific technical terms.",
					},
					"concurrency": map[string]any{
						"type":        "integer",
						"description": "Parallel workers for I/O-bound batches (1-8). Default: 1 (sequential). Order of `results` is preserved regardless.",
						"minimum":     1,
						"maximum":     8,
					},
					"cwd": map[string]any{
						"type":        "string",
						"description": "Current working directory for project scoping",
					},
				},
				"required": []string{"commands", "queries"},
			},
		},
		{
			Name:        "anchored_index",
			Description: "Index docs or knowledge content into the sandbox BM25 knowledge base without loading them into context. Chunks markdown by headings (preserving code blocks) and stores in an ephemeral FTS5 corpus; only a short summary is returned. Use for skill bodies, reference manuals, large local files, or any prose you want queryable via anchored_ctx_search later. Provide either `content` or `path`, not both.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content": map[string]any{
						"type":        "string",
						"description": "Raw text/markdown to index. Provide this OR path, not both.",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "File path to read and index (content never enters context). Provide this OR content, not both.",
					},
					"source": map[string]any{
						"type":        "string",
						"description": "Label for the indexed content (e.g., 'Context7: React useEffect', 'Skill: frontend-design')",
					},
					"cwd": map[string]any{
						"type":        "string",
						"description": "Current working directory for project scoping",
					},
				},
				"required": []string{"source"},
			},
		},
		{
			Name:        "anchored_ctx_search",
			Description: "FOLLOW-UP TOOL for the sandbox knowledge base. Use for every additional question after anchored_batch_execute / anchored_execute / anchored_index / anchored_fetch_and_index has populated the corpus — instead of re-running commands or re-fetching pages. Pass ALL questions as the queries array in ONE call (BM25 + vector hybrid). Filter with `content_type: 'code'` (matches source code chunks) or `'prose'` (matches narrative/markdown). Cheap; no raw output enters context. Progressive throttling kicks in past 3 calls within the same indexing scope — fold remaining questions into a single batched call instead of fanning out.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"queries": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Array of search queries. Batch ALL questions in one call.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Results per query (default: 3)",
						"default":     3,
					},
					"source": map[string]any{
						"type":        "string",
						"description": "Filter to a specific indexed source (partial match).",
					},
					"content_type": map[string]any{
						"type":        "string",
						"description": "Filter chunks by content type: 'code' for source-code chunks, 'prose' for narrative/markdown. Empty = no filter.",
						"enum":        []string{"", "code", "prose"},
					},
					"cwd": map[string]any{
						"type":        "string",
						"description": "Current working directory for project scoping",
					},
				},
				"required": []string{"queries"},
			},
		},
		{
			Name:        "anchored_fetch_and_index",
			Description: "USE INSTEAD OF WebFetch for any URL. Fetches the page, converts HTML→markdown, indexes the full body into the sandbox knowledge base, and returns only a ~3KB preview — raw HTML never enters context. Calls within the cache TTL (default 24h) return cached results; pass force=true to re-fetch. For MULTI-URL fan-out (e.g., research across several docs/pages in one shot), pass `requests: [{url, source}, ...]` plus optional `concurrency` (1-8) instead of `url`/`source`. Follow up with anchored_ctx_search (queries array) — never refetch to dig deeper.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "Single URL to fetch and index. Use this OR `requests`, not both.",
					},
					"source": map[string]any{
						"type":        "string",
						"description": "Label for the indexed content (e.g., 'React useEffect docs', 'Supabase Auth API'). Defaults to the URL if omitted.",
					},
					"requests": map[string]any{
						"type":        "array",
						"description": "Multi-URL batch. Each entry is fetched, converted, and indexed; results are returned in input order. Use this OR `url`, not both.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"url":    map[string]any{"type": "string", "description": "URL to fetch."},
								"source": map[string]any{"type": "string", "description": "Optional label; defaults to the URL."},
							},
							"required": []string{"url"},
						},
					},
					"concurrency": map[string]any{
						"type":        "integer",
						"description": "Parallel workers for `requests` (1-8). Default: 1 (sequential). Ignored when calling with a single `url`.",
						"minimum":     1,
						"maximum":     8,
					},
					"cwd": map[string]any{
						"type":        "string",
						"description": "Current working directory for project scoping",
					},
					"force": map[string]any{
						"type":        "boolean",
						"description": "Bypass cache and re-fetch (default: false). Applies to every URL in the call.",
						"default":     false,
					},
				},
			},
		},
	}
}

func ResourceDefinitions() []Resource {
	return []Resource{
		{
			URI:         "anchored://memory/stats",
			Name:        "Memory Statistics",
			Description: "Current memory database statistics",
			MIMEType:    "application/json",
		},
		{
			URI:         "anchored://memory/recent",
			Name:        "Recent Memories",
			Description: "Last 10 saved memories",
			MIMEType:    "application/json",
		},
		{
			URI:         "anchored://identity",
			Name:        "Identity",
			Description: "The user's identity file (~/.anchored/identity.md)",
			MIMEType:    "text/plain",
		},
		{
			URI:         "anchored://projects",
			Name:        "Projects",
			Description: "List of all known projects",
			MIMEType:    "text/plain",
		},
	}
}

func FindTool(name string) *Tool {
	for _, t := range ToolDefinitions() {
		if t.Name == name {
			return &t
		}
	}
	return nil
}

func SortTools(tools []Tool) {
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name < tools[j].Name
	})
}
