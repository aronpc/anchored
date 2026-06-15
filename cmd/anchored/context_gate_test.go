package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jholhewres/anchored/pkg/hookroute"
)

func TestContextGate_SatisfyingToolMarksSession(t *testing.T) {
	dir := t.TempDir()
	dec, stage := contextGateDecision(dir, "sess-1", "anchored_context", "mcp__anchored__anchored_context")
	if dec != nil {
		t.Fatalf("anchored_context should never be denied, got deny: %q", dec.Reason)
	}
	if stage != "satisfied" {
		t.Fatalf("want stage=satisfied, got %q", stage)
	}
	// A subsequent work tool must now pass.
	dec, stage = contextGateDecision(dir, "sess-1", "Bash", "Bash")
	if dec != nil {
		t.Fatalf("work tool after satisfy should pass, got deny (stage=%q)", stage)
	}
	if stage != "already_satisfied" {
		t.Fatalf("want stage=already_satisfied, got %q", stage)
	}
}

func TestContextGate_DeniesFirstWorkTool(t *testing.T) {
	dir := t.TempDir()
	dec, stage := contextGateDecision(dir, "sess-2", "Bash", "Bash")
	if dec == nil {
		t.Fatalf("first work tool should be denied, got passthrough (stage=%q)", stage)
	}
	if dec.Action != hookroute.ActionDeny {
		t.Fatalf("want ActionDeny, got %q", dec.Action)
	}
	if stage != "denied" {
		t.Fatalf("want stage=denied, got %q", stage)
	}
	// Marker should hold the deny counter (1).
	data, err := os.ReadFile(filepath.Join(dir, "ctxgate", sanitizeSessionID("sess-2")))
	if err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	if string(data) != "1" {
		t.Fatalf("want deny counter 1, got %q", data)
	}
}

func TestContextGate_RelentsAfterBudget(t *testing.T) {
	dir := t.TempDir()
	// Exhaust the deny budget.
	for i := 0; i < ctxGateMaxDenies; i++ {
		dec, _ := contextGateDecision(dir, "sess-3", "Bash", "Bash")
		if dec == nil {
			t.Fatalf("deny %d should still block", i+1)
		}
	}
	// Next call must relent (pass through) rather than block forever.
	dec, stage := contextGateDecision(dir, "sess-3", "Bash", "Bash")
	if dec != nil {
		t.Fatalf("gate must relent after %d denies, still blocking (stage=%q)", ctxGateMaxDenies, stage)
	}
	if stage != "relented" {
		t.Fatalf("want stage=relented, got %q", stage)
	}
	// And stay relented.
	if _, stage := contextGateDecision(dir, "sess-3", "Bash", "Bash"); stage != "already_satisfied" {
		t.Fatalf("after relent want already_satisfied, got %q", stage)
	}
}

func TestContextGate_NeverGatesAnchoredTools(t *testing.T) {
	dir := t.TempDir()
	// An anchored tool that is NOT a satisfying one (e.g. a save) must pass
	// without ever being gated, even on a fresh session — otherwise the agent
	// could be deadlocked.
	dec, stage := contextGateDecision(dir, "sess-4", "anchored_save", "mcp__anchored__anchored_save")
	if dec != nil {
		t.Fatalf("anchored tools must never be gated, got deny (stage=%q)", stage)
	}
	if stage != "exempt_anchored" {
		t.Fatalf("want stage=exempt_anchored, got %q", stage)
	}
}

func TestContextGate_FailOpenWithoutSession(t *testing.T) {
	dir := t.TempDir()
	if dec, stage := contextGateDecision(dir, "", "Bash", "Bash"); dec != nil || stage != "skip_no_session" {
		t.Fatalf("empty session id must fail open, got dec=%v stage=%q", dec, stage)
	}
	if dec, stage := contextGateDecision("", "sess-5", "Bash", "Bash"); dec != nil || stage != "skip_no_session" {
		t.Fatalf("empty storage dir must fail open, got dec=%v stage=%q", dec, stage)
	}
}

func TestContextGate_SearchAlsoSatisfies(t *testing.T) {
	dir := t.TempDir()
	for _, tool := range []string{"anchored_search", "anchored_ctx_search", "anchored_kg_query"} {
		sess := "sess-" + tool
		if _, stage := contextGateDecision(dir, sess, tool, "mcp__anchored__"+tool); stage != "satisfied" {
			t.Fatalf("%s should satisfy the gate, got %q", tool, stage)
		}
	}
}

func TestSanitizeSessionID(t *testing.T) {
	if got := sanitizeSessionID(""); got != "_.gate" {
		t.Fatalf("empty id: want _.gate, got %q", got)
	}
	if got := sanitizeSessionID("a/b:c d"); got != "a_b_c_d.gate" {
		t.Fatalf("unsafe runes: want a_b_c_d.gate, got %q", got)
	}
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	if got := sanitizeSessionID(long); len(got) != 128+len(".gate") {
		t.Fatalf("overlong id not capped: len=%d", len(got))
	}
}
