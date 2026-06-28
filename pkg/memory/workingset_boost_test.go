package memory

import (
	"testing"
	"time"
)

func ptr(s string) *string { return &s }

func TestApplyWorkingSetBoost_RanksInFocusAbove(t *testing.T) {
	// Two equivalent-scoring memories; only one mentions a working-set file.
	results := []SearchResult{
		{Memory: Memory{ID: "generic", Content: "the sync engine batches pushes"}, Score: 1.0},
		{Memory: Memory{ID: "infocus", Content: "client.go partitions large pushes under the cap"}, Score: 1.0},
	}
	ws := &WorkingSetSignals{Files: []string{"pkg/sync/client.go"}}

	out := applyWorkingSetBoost(results, ws, true)

	var generic, infocus float64
	var infocusSignals []string
	for _, r := range out {
		switch r.Memory.ID {
		case "generic":
			generic = r.Score
		case "infocus":
			infocus = r.Score
			infocusSignals = r.Signals
		}
	}
	if infocus <= generic {
		t.Fatalf("in-focus memory must rank above generic: infocus=%v generic=%v", infocus, generic)
	}
	if infocus != 1.3 {
		t.Fatalf("expected 1.3x boost, got %v", infocus)
	}
	found := false
	for _, s := range infocusSignals {
		if s == "working_set" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected working_set signal, got %v", infocusSignals)
	}
}

func TestApplyWorkingSetBoost_NoMatchNoChange(t *testing.T) {
	results := []SearchResult{{Memory: Memory{ID: "m", Content: "unrelated content"}, Score: 2.0}}
	ws := &WorkingSetSignals{Files: []string{"pkg/sync/client.go"}, Symbols: []string{"PushBatch"}}
	out := applyWorkingSetBoost(results, ws, true)
	if out[0].Score != 2.0 {
		t.Fatalf("score must be unchanged without overlap, got %v", out[0].Score)
	}
	if len(out[0].Signals) != 0 {
		t.Fatalf("no signal expected, got %v", out[0].Signals)
	}
}

func TestWorkingSetTokens_Basename(t *testing.T) {
	ws := &WorkingSetSignals{Files: []string{"pkg/sync/client.go"}, Symbols: []string{"PushBatch"}, Entities: []string{"anchored"}}
	toks := workingSetTokens(ws)
	want := map[string]bool{"client.go": false, "client": false, "pushbatch": false, "anchored": false}
	for _, tk := range toks {
		if _, ok := want[tk]; ok {
			want[tk] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Fatalf("expected token %q in %v", k, toks)
		}
	}
}

func TestAnnotateBaseSignals(t *testing.T) {
	now := time.Now()
	results := []SearchResult{
		{Memory: Memory{ID: "p", ProjectID: ptr("proj-1"), CreatedAt: now}},
		{Memory: Memory{ID: "g", ProjectID: nil, CreatedAt: now.Add(-365 * 24 * time.Hour)}},
	}
	annotateBaseSignals(results, "proj-1", now)

	hasSignal := func(sigs []string, s string) bool {
		for _, x := range sigs {
			if x == s {
				return true
			}
		}
		return false
	}
	if !hasSignal(results[0].Signals, "project") || !hasSignal(results[0].Signals, "fresh") {
		t.Fatalf("project memory signals: %v", results[0].Signals)
	}
	if !hasSignal(results[1].Signals, "global") {
		t.Fatalf("global memory signals: %v", results[1].Signals)
	}
	if hasSignal(results[1].Signals, "fresh") {
		t.Fatalf("year-old memory must not be fresh: %v", results[1].Signals)
	}
}
