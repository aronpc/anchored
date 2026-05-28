package memory

import "testing"

// A long, project-scoped decision scores comfortably above threshold.
const highSignalContent = "We decided to migrate the curation worker to a scorer_version model so that " +
	"formula changes re-flow through the entire corpus instead of only touching brand-new memories. " +
	"This repairs the stale low_signal flags left behind by older scoring formulas."

func TestRecurateMetadata_FreshHighSignal(t *testing.T) {
	meta, changed := RecurateMetadata(MemoryMetadata{}, highSignalContent, "decision", true, RemoteQualityThreshold)
	if !changed {
		t.Fatal("expected changed=true for a fresh memory")
	}
	if meta.ScorerVersion != QualityScorerVersion {
		t.Fatalf("scorer_version = %d, want %d", meta.ScorerVersion, QualityScorerVersion)
	}
	if meta.QualityScore < RemoteQualityThreshold {
		t.Fatalf("quality_score = %.2f, want >= %.2f", meta.QualityScore, RemoteQualityThreshold)
	}
	if meta.Importance != meta.QualityScore {
		t.Fatalf("importance = %.2f, want initialized to quality_score %.2f", meta.Importance, meta.QualityScore)
	}
	if meta.CurationStatus != "" {
		t.Fatalf("curation_status = %q, want empty for high-signal", meta.CurationStatus)
	}
}

func TestRecurateMetadata_Idempotent(t *testing.T) {
	meta, _ := RecurateMetadata(MemoryMetadata{}, highSignalContent, "decision", true, RemoteQualityThreshold)
	_, changed := RecurateMetadata(meta, highSignalContent, "decision", true, RemoteQualityThreshold)
	if changed {
		t.Fatal("second pass over unchanged content should report changed=false")
	}
}

func TestRecurateMetadata_DoesNotReduceImportance(t *testing.T) {
	// Short content scores low, but a deliberately high importance must survive.
	meta := MemoryMetadata{Importance: 0.95}
	out, _ := RecurateMetadata(meta, "too short", "event", false, RemoteQualityThreshold)
	if out.Importance != 0.95 {
		t.Fatalf("importance = %.2f, want preserved 0.95 (never ratcheted down)", out.Importance)
	}
}

func TestRecurateMetadata_FlagsLowSignal(t *testing.T) {
	out, changed := RecurateMetadata(MemoryMetadata{}, "ok", "event", false, RemoteQualityThreshold)
	if !changed {
		t.Fatal("expected changed=true")
	}
	if out.CurationStatus != CurationStatusLowSignal {
		t.Fatalf("curation_status = %q, want low_signal", out.CurationStatus)
	}
}

func TestRecurateMetadata_ClearsStaleLowSignal(t *testing.T) {
	// A previously-flagged memory whose content is now high-signal must be lifted.
	meta := MemoryMetadata{CurationStatus: CurationStatusLowSignal, QualityScore: 0.2, ScorerVersion: 1}
	out, changed := RecurateMetadata(meta, highSignalContent, "decision", true, RemoteQualityThreshold)
	if !changed {
		t.Fatal("expected changed=true for stale flag repair")
	}
	if out.CurationStatus != "" {
		t.Fatalf("curation_status = %q, want cleared", out.CurationStatus)
	}
}

func TestRecurateMetadata_PinnedNeverFlagged(t *testing.T) {
	out, _ := RecurateMetadata(MemoryMetadata{Pinned: true}, "x", "event", false, RemoteQualityThreshold)
	if out.CurationStatus == CurationStatusLowSignal {
		t.Fatal("pinned memory must never be flagged low_signal")
	}
}

// A memory scoring exactly 0 must still be stamped with scorer_version so it is
// not re-selected forever as an unscored candidate (the "zombie" regression).
func TestRecurateMetadata_ZeroScoreStillStamped(t *testing.T) {
	out, changed := RecurateMetadata(MemoryMetadata{}, "", "event", false, RemoteQualityThreshold)
	if out.QualityScore != 0 {
		t.Fatalf("quality_score = %.2f, want 0 for empty content", out.QualityScore)
	}
	if !changed || out.ScorerVersion != QualityScorerVersion {
		t.Fatalf("zero-score memory must be stamped scorer_version=%d (changed=%v, got v%d)",
			QualityScorerVersion, changed, out.ScorerVersion)
	}
	// And ToAny must not collapse to nil, or the stamp would be lost on write.
	if out.ToAny() == nil {
		t.Fatal("ToAny() returned nil; scorer_version stamp would be lost")
	}
}
