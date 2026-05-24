# IDE / Tool Configs

Sample configs for clients that don't auto-install hooks via the Claude Code plugin manifest.

For **Claude Code**, just install the plugin (`/plugin install anchored@anchored`) — the SessionStart, UserPromptSubmit, PostToolUse, and PreCompact hooks are wired by `hooks/hooks.json` automatically.

For everything else, the snippets below register the `anchored` MCP server **and** the routing-block hooks so the agent treats anchored as the persistent memory layer instead of waiting for explicit instructions.

## Cursor

1. Drop the contents of [`cursor/mcp.json`](cursor/mcp.json) into `~/.cursor/mcp.json` (merge with existing `mcpServers`).
2. Drop the contents of [`cursor/hooks.json`](cursor/hooks.json) into `~/.cursor/hooks.json` (or `<project>/.cursor/hooks.json` for project-scoped).
3. Restart Cursor.

The pretooluse hook ensures anchored gets a chance to inject memory context before Cursor's own tool calls; postToolUse + stop capture session events.

## OpenCode

1. Merge [`opencode/opencode.json`](opencode/opencode.json) into your `~/.config/opencode/opencode.json` (or `opencode.json` at the repo root).
2. Restart OpenCode.

OpenCode does not yet expose a stable `SessionStart` event, so `experimental.chat.system.transform` is used as the surrogate — anchored injects the routing block into the system prompt at session start. `experimental.hook.chat_message` re-injects on every user prompt; `experimental.hook.session_compacting` snapshots before compaction.

## Antigravity (agy)

1. Merge [`agy/mcp_config.json`](agy/mcp_config.json) into your `~/.gemini/config/mcp_config.json` (Antigravity 2.0 desktop) or `~/.gemini/antigravity-cli/mcp_config.json` (Antigravity CLI).
2. Restart Antigravity.

Antigravity does not yet expose a hook system, so the MCP tool descriptions and the `Instructions` field returned in `initialize` steer the model. You may need to ask the agent to "check anchored memory" occasionally.

## Anything else MCP-compatible

If your tool only supports MCP server registration (no hooks), just register `anchored` as the MCP server and the tool descriptions + the `Instructions` field returned in `initialize` will steer the model. You won't get the SessionStart/UserPromptSubmit reminders, so the model may need a nudge ("check anchored memory") more often.
