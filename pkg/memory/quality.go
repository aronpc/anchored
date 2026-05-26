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

	quality := ScoreQuality(content, category, hasProject)
	m.QualityScore = quality
	if m.Importance == 0 {
		m.Importance = quality
	}
	if quality < 0.55 && !m.Pinned {
		m.CurationStatus = CurationStatusLowSignal
	}
	if quality >= 0.7 && m.CurationStatus == CurationStatusLowSignal {
		m.CurationStatus = ""
	}
	return m.ToAny()
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
