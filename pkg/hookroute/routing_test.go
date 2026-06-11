package hookroute

import (
	"strings"
	"testing"
)

// freshSession returns a unique session id per subtest so the once-per-session
// guidance throttle does not bleed between cases, and registers cleanup.
func freshSession(t *testing.T) string {
	t.Helper()
	id := "test-" + strings.ReplaceAll(t.Name(), "/", "_")
	ResetThrottle(id)
	t.Cleanup(func() { ResetThrottle(id) })
	return id
}

func optOn(sid string) Options  { return Options{OptimizerEnabled: true, SessionID: sid, SubagentBlock: "BLOCK"} }
func optOff(sid string) Options { return Options{OptimizerEnabled: false, SessionID: sid, SubagentBlock: "BLOCK"} }

func TestWebFetchRedirectsWhenOptimizerOn(t *testing.T) {
	sid := freshSession(t)
	d := RoutePreToolUse("WebFetch", map[string]any{"url": "https://example.com"}, optOn(sid))
	if d == nil || d.Action != ActionDeny {
		t.Fatalf("WebFetch should deny+redirect when optimizer on, got %+v", d)
	}
	if !strings.Contains(d.Reason, "anchored_fetch_and_index") {
		t.Errorf("deny reason must point to anchored_fetch_and_index: %q", d.Reason)
	}
}

func TestWebFetchDegradesWhenOptimizerOff(t *testing.T) {
	sid := freshSession(t)
	d := RoutePreToolUse("WebFetch", map[string]any{"url": "https://example.com"}, optOff(sid))
	if d == nil || d.Action != ActionContext {
		t.Fatalf("WebFetch must NOT deny into a disabled sandbox; want context nudge, got %+v", d)
	}
}

func TestCurlStdoutFloodRedirects(t *testing.T) {
	sid := freshSession(t)
	d := RoutePreToolUse("Bash", map[string]any{"command": "curl https://api.example.com/data"}, optOn(sid))
	if d == nil || d.Action != ActionDeny {
		t.Fatalf("curl stdout flood should deny+redirect, got %+v", d)
	}
	if !strings.Contains(d.Reason, "anchored_execute") {
		t.Errorf("reason must mention anchored_execute: %q", d.Reason)
	}
}

func TestCurlSilentFileDownloadAllowed(t *testing.T) {
	sid := freshSession(t)
	// silent + file output → not a flood → falls through to generic bash nudge,
	// never a deny.
	d := RoutePreToolUse("Bash", map[string]any{"command": "curl -s -o out.bin https://example.com/f"}, optOn(sid))
	if d != nil && d.Action == ActionDeny {
		t.Fatalf("silent file download must not be denied, got %+v", d)
	}
}

func TestCurlInsideQuotesNotFlagged(t *testing.T) {
	sid := freshSession(t)
	d := RoutePreToolUse("Bash", map[string]any{"command": `gh issue edit 1 --body "use curl to fetch"`}, optOn(sid))
	if d != nil && d.Action == ActionDeny {
		t.Fatalf("curl inside a quoted string must not deny, got %+v", d)
	}
}

func TestCurlFileDownloadWithoutSilentAllowed(t *testing.T) {
	sid := freshSession(t)
	// Genuine file destination, no stdout alias, not verbose: nothing of
	// substance reaches context → must NOT be denied (memory-first divergence
	// from context-mode, which would require -s here).
	d := RoutePreToolUse("Bash", map[string]any{"command": "curl -o report.json https://api.example.com/data"}, optOn(sid))
	if d != nil && d.Action == ActionDeny {
		t.Fatalf("plain file download must not be denied, got %+v", d)
	}
}

func TestCurlStdoutAliasStillFloods(t *testing.T) {
	sid := freshSession(t)
	d := RoutePreToolUse("Bash", map[string]any{"command": "curl -o - https://api.example.com/data"}, optOn(sid))
	if d == nil || d.Action != ActionDeny {
		t.Fatalf("curl -o - is a stdout alias → must deny, got %+v", d)
	}
}

func TestInlineHTTPInScriptLiteralRedirects(t *testing.T) {
	sid := freshSession(t)
	// The HTTP call lives inside a quoted -e literal but IS executed → redirect.
	d := RoutePreToolUse("Bash", map[string]any{"command": `node -e "fetch('https://api.example.com/x')"`}, optOn(sid))
	if d == nil || d.Action != ActionDeny {
		t.Fatalf("inline HTTP in a -e literal should deny+redirect, got %+v", d)
	}
}

func TestBuildToolRedirects(t *testing.T) {
	sid := freshSession(t)
	d := RoutePreToolUse("Bash", map[string]any{"command": "gradle build"}, optOn(sid))
	if d == nil || d.Action != ActionDeny {
		t.Fatalf("gradle build should deny+redirect, got %+v", d)
	}
}

func TestStructurallyBoundedNoNudge(t *testing.T) {
	sid := freshSession(t)
	for _, cmd := range []string{"pwd", "git status", "whoami", "node --version", "git log -5"} {
		d := RoutePreToolUse("Bash", map[string]any{"command": cmd}, optOn(sid))
		if d != nil {
			t.Errorf("%q is structurally bounded; want passthrough, got %+v", cmd, d)
		}
	}
}

func TestControlOperatorDisqualifiesAllowlist(t *testing.T) {
	if isStructurallyBounded("git status | grep modified") {
		t.Error("piped command must not be structurally bounded")
	}
	if isStructurallyBounded("pwd && cat /etc/passwd") {
		t.Error("chained command must not be structurally bounded")
	}
	if isStructurallyBounded("git status\nfind /") {
		t.Error("newline-injected command must not be structurally bounded")
	}
}

func TestVerboseCpNotBounded(t *testing.T) {
	if isStructurallyBounded("cp -rv src dst") {
		t.Error("cp -rv floods one line per file; must not be bounded")
	}
	if isStructurallyBounded("ls -R /") {
		t.Error("ls -R is recursive; must not be bounded")
	}
}

func TestCodebaseSearchMemoryNudge(t *testing.T) {
	sid := freshSession(t)
	d := RoutePreToolUse("Bash", map[string]any{"command": "grep -r authConvention ."}, optOn(sid))
	if d == nil || d.Action != ActionContext {
		t.Fatalf("recursive grep should emit a memory nudge, got %+v", d)
	}
	if !strings.Contains(d.AdditionalContext, "anchored_search") {
		t.Errorf("memory nudge must mention anchored_search: %q", d.AdditionalContext)
	}
}

func TestReadNeverDenies(t *testing.T) {
	sid := freshSession(t)
	d := RoutePreToolUse("Read", map[string]any{"file_path": "/x/huge.json"}, optOn(sid))
	if d != nil && d.Action == ActionDeny {
		t.Fatalf("Read must never be denied (Edit needs the bytes), got %+v", d)
	}
}

func TestGrepNudgeOncePerSession(t *testing.T) {
	sid := freshSession(t)
	first := RoutePreToolUse("Grep", map[string]any{"pattern": "x"}, optOn(sid))
	if first == nil || first.Action != ActionContext {
		t.Fatalf("first Grep should nudge, got %+v", first)
	}
	second := RoutePreToolUse("Grep", map[string]any{"pattern": "y"}, optOn(sid))
	if second != nil {
		t.Errorf("second Grep in same session should be throttled, got %+v", second)
	}
}

func TestAgentPromptInjection(t *testing.T) {
	sid := freshSession(t)
	d := RoutePreToolUse("Agent", map[string]any{"prompt": "do the thing", "subagent_type": "general-purpose"}, optOn(sid))
	if d == nil || d.Action != ActionModify {
		t.Fatalf("Agent should modify the prompt, got %+v", d)
	}
	got, _ := d.UpdatedInput["prompt"].(string)
	if !strings.Contains(got, "do the thing") || !strings.Contains(got, "BLOCK") {
		t.Errorf("modified prompt must keep original and append the block: %q", got)
	}
	if d.UpdatedInput["subagent_type"] != "general-purpose" {
		t.Error("other fields must be preserved")
	}
}

func TestAgentTaskAliasInjects(t *testing.T) {
	sid := freshSession(t)
	d := RoutePreToolUse("Task", map[string]any{"prompt": "x"}, optOn(sid))
	if d == nil || d.Action != ActionModify {
		t.Fatalf("Task (legacy Agent) should modify the prompt, got %+v", d)
	}
}

func TestExternalMCPNudgeGatedOnOptimizer(t *testing.T) {
	sid := freshSession(t)
	if d := RoutePreToolUse("mcp__slack__post", map[string]any{}, optOff(sid)); d != nil {
		t.Errorf("external MCP nudge must be gated off when optimizer disabled, got %+v", d)
	}
	d := RoutePreToolUse("mcp__slack__post", map[string]any{}, optOn(sid))
	if d == nil || d.Action != ActionContext {
		t.Fatalf("external MCP should nudge when optimizer on, got %+v", d)
	}
}

func TestAnchoredOwnToolsNotRoutedAsExternal(t *testing.T) {
	sid := freshSession(t)
	if d := RoutePreToolUse("mcp__anchored__anchored_save", map[string]any{}, optOn(sid)); d != nil {
		t.Errorf("anchored's own tools must not get the external-MCP nudge, got %+v", d)
	}
}

func TestFormatDecisionWireShapes(t *testing.T) {
	deny := FormatDecision(&Decision{Action: ActionDeny, Reason: "no"})
	hso, _ := deny["hookSpecificOutput"].(map[string]any)
	if hso["permissionDecision"] != "deny" || hso["permissionDecisionReason"] != "no" {
		t.Errorf("deny wire shape wrong: %+v", deny)
	}
	ctx := FormatDecision(&Decision{Action: ActionContext, AdditionalContext: "hi"})
	hso2, _ := ctx["hookSpecificOutput"].(map[string]any)
	if hso2["additionalContext"] != "hi" {
		t.Errorf("context wire shape wrong: %+v", ctx)
	}
	if FormatDecision(nil) != nil {
		t.Error("nil decision must format to nil (passthrough)")
	}
}
