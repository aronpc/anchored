package memory

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	testOutputRe = regexp.MustCompile(`(?i)\b(\d+\s+(passed|failed|skipped)|0\s+failures?|testes?\s+passando|suite\s+completa|rodando\s+suite|go\s+test|pytest|npm\s+test)\b`)
	progressRe   = regexp.MustCompile(`(?i)\b(corrigido|rodando|testando|retestar)\b`)
	terminalRe   = regexp.MustCompile(`(?i)(^|\n)\s*(error:|warning:|panic:|traceback|stack trace|expected|actual|assert)\b`)
)

func ScoreQuality(content, category string, hasProject bool) float64 {
	text := strings.TrimSpace(content)
	if text == "" {
		return 0
	}

	score := 0.62
	chars := len([]rune(text))
	words := strings.Fields(text)

	switch category {
	case "decision", "learning":
		score += 0.18
	case "summary", "plan":
		score += 0.08
	case "event", "preference":
		score -= 0.2
	}

	if hasProject {
		score += 0.12
	} else {
		score -= 0.08
	}

	switch {
	case chars < 40:
		score -= 0.42
	case chars < 90:
		score -= 0.24
	case chars > 220:
		score += 0.08
	}

	if len(words) < 6 {
		score -= 0.18
	}
	if testOutputRe.MatchString(text) {
		score -= 0.32
	}
	if progressRe.MatchString(text) {
		score -= 0.28
	}
	if terminalRe.MatchString(text) {
		score -= 0.18
	}
	if strings.Count(text, "\n") > 12 && category == "fact" {
		score -= 0.1
	}
	if punctuationRatio(text) > 0.24 && chars < 180 {
		score -= 0.12
	}

	return clamp01(score)
}

func ApplyQualityMetadata(metadata any, content, category string, hasProject bool) any {
	m := ParseMetadata(metadata)
	defaults := InferDefaultsFromCategory(category, hasProject)
	if m.MemoryType == "" {
		m.MemoryType = defaults.MemoryType
	}
	if m.Scope == "" {
		m.Scope = defaults.Scope
	}
	if m.Kind == "" {
		m.Kind = category
	}
	if m.Origin == "" {
		m.Origin = OriginManual
	}

	m, _ = RecurateMetadata(m, content, category, hasProject, RemoteQualityThreshold)
	return m.ToAny()
}

// RecurateMetadata is the single canonical pass that refreshes the quality
// lifecycle fields (quality_score, importance, curation_status, scorer_version)
// for a memory. It is shared by the save path (ApplyQualityMetadata), the
// serve-time worker, and the `curation` CLI so the three never diverge.
//
// It is intentionally non-destructive: importance is only initialized when
// unset (never ratcheted down toward the mechanical quality score), and only
// the lifecycle fields above are touched. The returned bool reports whether any
// field actually changed, so callers can skip no-op writes.
func RecurateMetadata(m MemoryMetadata, content, category string, hasProject bool, threshold float64) (MemoryMetadata, bool) {
	if threshold <= 0 {
		threshold = RemoteQualityThreshold
	}

	score := ScoreQuality(content, category, hasProject)
	changed := false

	if m.QualityScore != score {
		m.QualityScore = score
		changed = true
	}
	if m.ScorerVersion != QualityScorerVersion {
		m.ScorerVersion = QualityScorerVersion
		changed = true
	}
	// Initialize importance only; never reduce a value someone (user/dream) set
	// deliberately. importance is a retention/ranking hint, not the text score.
	if m.Importance == 0 {
		m.Importance = score
		changed = true
	}

	switch {
	case score < threshold && !m.Pinned:
		if m.CurationStatus != CurationStatusLowSignal {
			m.CurationStatus = CurationStatusLowSignal
			changed = true
		}
	case m.CurationStatus == CurationStatusLowSignal:
		// Score recovered (or memory pinned): lift the demotion flag.
		m.CurationStatus = ""
		changed = true
	}

	return m, changed
}

func punctuationRatio(s string) float64 {
	if s == "" {
		return 0
	}
	total := 0
	punct := 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		total++
		if unicode.IsPunct(r) || unicode.IsSymbol(r) {
			punct++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(punct) / float64(total)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
