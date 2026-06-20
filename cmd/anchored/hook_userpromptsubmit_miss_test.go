package main

import (
	"strings"
	"testing"
)

// The semantic-miss nudge must redirect the model to anchored_search (the
// hybrid search that can catch what keyword recall missed) and echo the query
// that was already tried. It must add no DB/embedding work — it is a pure
// string builder, which this test also documents.
func TestRenderRecallMissNudge_DirectsToHybridSearch(t *testing.T) {
	out := renderRecallMissNudge("how did we decide the auth flow")

	for _, want := range []string{
		"anchored_search",                 // the directed call
		`hits="0"`,                        // signals keyword recall came up empty
		"how did we decide the auth flow", // echoes the attempted query
	} {
		if !strings.Contains(out, want) {
			t.Errorf("miss nudge missing %q\n--- got ---\n%s", want, out)
		}
	}
	if !strings.HasPrefix(out, "<anchored_recall") || !strings.HasSuffix(out, "</anchored_recall>") {
		t.Errorf("miss nudge is not a well-formed anchored_recall block:\n%s", out)
	}
}

// The query echo is rune-capped so a very long prompt cannot bloat the nudge.
func TestRenderRecallMissNudge_CapsQueryEcho(t *testing.T) {
	long := strings.Repeat("alpha ", 200)
	out := renderRecallMissNudge(long)
	if !strings.Contains(out, "…") {
		t.Errorf("expected long query to be truncated with an ellipsis:\n%s", out)
	}
}

// Gating guard: trivial prompts sanitize to the empty query, which makes
// autoRecallPreview return "" BEFORE the miss-nudge branch — so below-threshold
// prompts never receive the nudge. This locks the guard the nudge relies on.
func TestSanitizeFTSQuery_TrivialPromptsGateOut(t *testing.T) {
	for _, trivial := range []string{"oi", "hi", "ok", "?", "   "} {
		if got := sanitizeFTSQuery(trivial); got != "" {
			t.Errorf("sanitizeFTSQuery(%q) = %q, want \"\" (trivial prompt must gate out before the nudge)", trivial, got)
		}
	}
}
