package memory

import "testing"

// Feature D: usage-feedback demotion rules in RecurateMetadata.

func TestRecurate_NeverUsedDemotion(t *testing.T) {
	// Good-quality content so the quality rule does not interfere.
	content := "we decided to adopt the tiered context budgeter for every hook injection path because it keeps the prompt size bounded and deterministic across sessions"

	m := MemoryMetadata{InjectedCount: NeverUsedInjectionFloor, UsedCount: 0, ScorerVersion: QualityScorerVersion}
	got, changed := RecurateMetadata(m, content, "decision", true, 0)
	if !changed || got.CurationStatus != CurationStatusLowSignal || got.CurationRule != CurationRuleNeverUsed {
		t.Fatalf("want never_used demotion, got status=%q rule=%q changed=%v", got.CurationStatus, got.CurationRule, changed)
	}

	// Below the floor: no demotion.
	m = MemoryMetadata{InjectedCount: NeverUsedInjectionFloor - 1, UsedCount: 0, ScorerVersion: QualityScorerVersion}
	got, _ = RecurateMetadata(m, content, "decision", true, 0)
	if got.CurationStatus != "" {
		t.Errorf("below floor must not demote, got %q", got.CurationStatus)
	}

	// Used at least once: no demotion.
	m = MemoryMetadata{InjectedCount: 50, UsedCount: 1, ScorerVersion: QualityScorerVersion}
	got, _ = RecurateMetadata(m, content, "decision", true, 0)
	if got.CurationStatus != "" {
		t.Errorf("used memory must not demote, got %q", got.CurationStatus)
	}

	// Pinned: exempt.
	m = MemoryMetadata{InjectedCount: 50, UsedCount: 0, Pinned: true, ScorerVersion: QualityScorerVersion}
	got, _ = RecurateMetadata(m, content, "decision", true, 0)
	if got.CurationStatus != "" {
		t.Errorf("pinned memory must not demote, got %q", got.CurationStatus)
	}
}

func TestRecurate_NeverUsedLiftsWhenUsed(t *testing.T) {
	content := "we decided to adopt the tiered context budgeter for every hook injection path because it keeps the prompt size bounded and deterministic across sessions"
	m := MemoryMetadata{
		InjectedCount:  20,
		UsedCount:      1, // finally used
		ScorerVersion:  QualityScorerVersion,
		CurationStatus: CurationStatusLowSignal,
		CurationRule:   CurationRuleNeverUsed,
	}
	got, changed := RecurateMetadata(m, content, "decision", true, 0)
	if !changed || got.CurationStatus != "" || got.CurationRule != "" {
		t.Fatalf("never_used demotion must lift once used, got status=%q rule=%q", got.CurationStatus, got.CurationRule)
	}
}

func TestRecurate_QualityRuleTakesPriority(t *testing.T) {
	// Short/low-quality content scores below threshold → quality rule wins
	// even with heavy injection history.
	m := MemoryMetadata{InjectedCount: 50, UsedCount: 0, ScorerVersion: QualityScorerVersion}
	got, _ := RecurateMetadata(m, "ok done", "event", false, 0)
	if got.CurationStatus != CurationStatusLowSignal || got.CurationRule != CurationRuleQuality {
		t.Fatalf("quality rule should take priority, got status=%q rule=%q", got.CurationStatus, got.CurationRule)
	}
}

func TestParseMetadata_UsageFieldsTyped(t *testing.T) {
	// The hooks write these keys via raw json_set; they must parse into the
	// typed fields (NOT fall into Extra) so RecurateMetadata can read them.
	raw := map[string]any{
		"injected_count":   float64(7),
		"used_count":       float64(2),
		"last_injected_at": "2026-06-10T00:00:00Z",
		"last_used_at":     "2026-06-10T01:00:00Z",
		"curation_rule":    "never_used",
	}
	m := ParseMetadata(raw)
	if m.InjectedCount != 7 || m.UsedCount != 2 {
		t.Errorf("counts not parsed: injected=%d used=%d", m.InjectedCount, m.UsedCount)
	}
	if m.LastInjectedAt == "" || m.LastUsedAt == "" || m.CurationRule != "never_used" {
		t.Errorf("timestamps/rule not parsed: %+v", m)
	}
	for _, k := range []string{"injected_count", "used_count", "last_injected_at", "last_used_at", "curation_rule"} {
		if _, inExtra := m.Extra[k]; inExtra {
			t.Errorf("%s leaked into Extra (would duplicate on marshal)", k)
		}
	}
}

// TestRecurate_V3MigrationResetsInheritedInjections guards the upgrade path:
// injected_count accumulated on v0.8.x (before the used-signal existed) must
// be zeroed on the first v3 recurate instead of instantly demoting memories
// that never had a chance to be marked used.
func TestRecurate_V3MigrationResetsInheritedInjections(t *testing.T) {
	content := "we decided to adopt the tiered context budgeter for every hook injection path because it keeps the prompt size bounded and deterministic across sessions"
	m := MemoryMetadata{InjectedCount: 40, UsedCount: 0, ScorerVersion: 2}
	got, changed := RecurateMetadata(m, content, "decision", true, 0)
	if !changed {
		t.Fatal("migration must report a change")
	}
	if got.CurationStatus != "" {
		t.Fatalf("inherited injections must not demote on upgrade, got %q", got.CurationStatus)
	}
	if got.InjectedCount != 0 || got.LastInjectedAt != "" {
		t.Errorf("inherited counters must be reset, got injected=%d last=%q", got.InjectedCount, got.LastInjectedAt)
	}
	if got.ScorerVersion != QualityScorerVersion {
		t.Errorf("scorer version must be stamped, got %d", got.ScorerVersion)
	}
}
