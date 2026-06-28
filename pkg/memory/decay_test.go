package memory

import (
	"testing"
	"time"
)

func decayResult(created time.Time, meta MemoryMetadata) float64 {
	res := []SearchResult{{Memory: Memory{CreatedAt: created, Metadata: meta.ToAny()}, Score: 1.0}}
	res = applyLifecycleBoost(res, time.Now())
	return res[0].Score
}

// TestAgeDecay_SearchTime guards Feature E's decay: never-used memories fade
// with age at search time; recent use resets the clock; pinned never decays;
// nothing is ever written back.
func TestAgeDecay_SearchTime(t *testing.T) {
	now := time.Now()

	fresh := decayResult(now.Add(-10*24*time.Hour), MemoryMetadata{ScorerVersion: 3})
	aging := decayResult(now.Add(-100*24*time.Hour), MemoryMetadata{ScorerVersion: 3})
	stale := decayResult(now.Add(-200*24*time.Hour), MemoryMetadata{ScorerVersion: 3})

	if !(stale < aging && aging < fresh) {
		t.Fatalf("decay should be monotonic: fresh=%.3f aging=%.3f stale=%.3f", fresh, aging, stale)
	}

	// A recent use resets the decay clock.
	used := decayResult(now.Add(-200*24*time.Hour), MemoryMetadata{
		ScorerVersion: 3,
		LastUsedAt:    now.Add(-5 * 24 * time.Hour).Format(time.RFC3339),
	})
	if used != fresh {
		t.Errorf("recent use must reset decay: used=%.3f fresh=%.3f", used, fresh)
	}

	// Pinned memories never decay (pinned also gets the 1.5x boost — compare
	// against a pinned fresh memory).
	pinnedOld := decayResult(now.Add(-400*24*time.Hour), MemoryMetadata{ScorerVersion: 3, Pinned: true})
	pinnedNew := decayResult(now.Add(-1*24*time.Hour), MemoryMetadata{ScorerVersion: 3, Pinned: true})
	if pinnedOld != pinnedNew {
		t.Errorf("pinned must not decay: old=%.3f new=%.3f", pinnedOld, pinnedNew)
	}
}
