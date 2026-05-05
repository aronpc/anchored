package mcp

import (
	"sort"
)

func ToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "anchored_context",
			Description: "MUST CALL FIRST on every new conversation, before any other tool, before answering anything. Returns the user's identity, project summary, recent decisions, and relevant memories accumulated across every AI tool and IDE they use. This is persistent cross-tool memory — without it you have no context on who the user is, what they're working on, prior decisions, or established preferences. Re-call when the user changes directories or shifts to a different project. The cost is one tool call; the benefit is acting like you remember the user instead of starting fresh every session.",
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
			Description: "Quietly search persistent cross-tool memory before answering domain questions — let results inform your reply without narrating the search. TRIGGERS: user references prior context (\"like we discussed\", \"that bug from last week\"); user asks about a project, service, decision, preference, or convention; you're about to make an architectural/naming choice or recommend a tool (search first to honor existing decisions). Hybrid vector + BM25, results in milliseconds. Don't announce \"let me check memory\" — just search, integrate, answer naturally.",
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
			Name:        "kg_query",
			Description: "Query the knowledge graph for an entity's relationships. Use IN ADDITION to anchored_search whenever the user names a specific project, service, repo, person, API, library, or environment — kg_query returns structured edges (depends_on, deployed_on, owns, uses) that prose search misses. Example triggers: \"how does X integrate with Y?\", \"what's the relationship between A and B?\", \"who owns service X?\", \"what depends on this library?\". Cheap; pair with anchored_search for full picture.",
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
			Name:        "kg_add",
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
			Description: "Execute code in a sandboxed subprocess. Only stdout enters context — raw data stays in the subprocess. Available: javascript, typescript, python, shell, ruby, go, rust, php, perl, r, elixir.",
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
			Description: "Process a file in the sandbox without loading its contents into context. Two variables are auto-injected before your code runs: FILE_PATH (absolute path) and FILE_CONTENT (file read as UTF-8 text). Use them directly — don't repeat the read. Only your printed output (stdout) enters context. Available languages: javascript, typescript, python, shell, ruby, go, rust, php, perl, r, elixir.",
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
			Description: "Execute multiple commands in ONE call, auto-index all output, and search with multiple queries. Returns search results directly — no follow-up calls needed.",
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
			Description: "Index documentation or knowledge content into a searchable BM25 knowledge base. Chunks markdown by headings (keeping code blocks intact) and stores in ephemeral FTS5 database. The full content does NOT stay in context — only a brief summary is returned.",
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
			Description: "Search indexed content. Requires prior indexing via anchored_index, anchored_execute, or anchored_batch_execute. Pass ALL search questions as queries array in ONE call.",
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
			Description: "Fetches URL content, converts HTML to markdown, indexes into the searchable knowledge base, and returns a ~3KB preview. Subsequent calls within the cache TTL (default 24h) return the cached result; pass force=true to bypass the cache and re-fetch from the network. Full content stays in the sandbox — use anchored_ctx_search for deeper lookups.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "The URL to fetch and index",
					},
					"source": map[string]any{
						"type":        "string",
						"description": "Label for the indexed content (e.g., 'React useEffect docs', 'Supabase Auth API')",
					},
					"cwd": map[string]any{
						"type":        "string",
						"description": "Current working directory for project scoping",
					},
					"force": map[string]any{
						"type":        "boolean",
						"description": "Bypass cache and re-fetch (default: false)",
						"default":     false,
					},
				},
				"required": []string{"url"},
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
