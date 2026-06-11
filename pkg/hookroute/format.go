package hookroute

// FormatDecision maps a normalized Decision to the Claude Code PreToolUse hook
// wire shape, or nil for passthrough (the caller then emits a plain allow).
//
// Wire-shape rationale (mirrors context-mode/hooks/core/formatters.mjs for the
// claude-code adapter):
//   - deny  → hookSpecificOutput.permissionDecision="deny" + reason. This is
//             the ONLY shape Claude Code honors for blocking a Bash/WebFetch
//             call AND surfacing the reason to the model.
//   - ask   → permissionDecision="ask".
//   - modify→ hookSpecificOutput.updatedInput. CC honors allow+updatedInput for
//             the Agent tool (the modified subagent prompt reaches the
//             subagent). Used ONLY for Agent here — Bash redirects are built as
//             Deny directly because CC ignores updatedInput.command under allow.
//
// Note: PreToolUse has NO additionalContext field (the schema accepts only
// permissionDecision/permissionDecisionReason/updatedInput), so there is no
// "inject context" action here — emitting one fails CC's output validation.
func FormatDecision(d *Decision) map[string]any {
	if d == nil || d.Action == ActionPass {
		return nil
	}
	switch d.Action {
	case ActionDeny:
		return map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":            "PreToolUse",
				"permissionDecision":       "deny",
				"permissionDecisionReason": d.Reason,
			},
		}
	case ActionAsk:
		return map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":      "PreToolUse",
				"permissionDecision": "ask",
			},
		}
	case ActionModify:
		return map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName": "PreToolUse",
				"updatedInput":  d.UpdatedInput,
			},
		}
	}
	return nil
}
