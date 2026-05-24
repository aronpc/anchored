package sync

import (
	"regexp"
	"strings"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/memory"
)

type RemoteSafetyViolation struct {
	Field   string
	Pattern string
	Value   string
	Reason  string
}

type RemoteSafetyResult struct {
	Content    string
	Allowed    bool
	Blocked    bool
	Violations []RemoteSafetyViolation
	// Rewritten is true when Content was modified (paths or secrets redacted).
	// A memory can be both Rewritten and Blocked — rewriting reduces sensitive
	// content but other violations may still block sync.
	Rewritten  bool
}

var (
	linuxHomeRe   = regexp.MustCompile(`/home/[^/\s]+(?:/[\S]*)?`)
	macOSHomeRe   = regexp.MustCompile(`/Users/[^/\s]+(?:/[\S]*)?`)
	winHomeRe     = regexp.MustCompile(`[A-Za-z]:\\Users\\[^\\/\s]+(?:\\[^\s]*)?`)
	tildeHomeRe   = regexp.MustCompile(`~/[\S]+`)
	tmpPathsRe    = regexp.MustCompile(`(?:/tmp|/var/folders|/private/tmp)(?:/[\S]*)?`)
)

const maxViolationValue = 50

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func RemoteSafetyFilter(content string, metadata map[string]any, projectRoot string) RemoteSafetyResult {
	result := RemoteSafetyResult{
		Content: content,
		Allowed: true,
	}

	result.Content, result.Violations = detectLocalPaths(content, projectRoot, result.Violations)

	secretContent, secretViolations := detectSecrets(result.Content)
	if len(secretViolations) > 0 {
		result.Violations = append(result.Violations, secretViolations...)
		result.Content = secretContent
	}

	if metadata != nil {
		result.Violations = detectPersonalPreference(metadata, result.Violations)
	}

	if result.Content != content {
		result.Rewritten = true
	}

	if len(result.Violations) > 0 {
		result.Blocked = true
		result.Allowed = false
	}

	return result
}

func detectLocalPaths(content string, projectRoot string, violations []RemoteSafetyViolation) (string, []RemoteSafetyViolation) {
	rewritten := content

	for _, re := range []*regexp.Regexp{linuxHomeRe, macOSHomeRe, winHomeRe, tildeHomeRe} {
		matches := re.FindAllString(rewritten, -1)
		for _, m := range matches {
			if projectRoot != "" && strings.HasPrefix(m, projectRoot) {
				continue
			}
			violations = append(violations, RemoteSafetyViolation{
				Field:   "content",
				Pattern: re.String(),
				Value:   truncate(m, maxViolationValue),
				Reason:  "local_path",
			})
		}
	}

	tmpMatches := tmpPathsRe.FindAllString(rewritten, -1)
	for _, m := range tmpMatches {
		violations = append(violations, RemoteSafetyViolation{
			Field:   "content",
			Pattern: tmpPathsRe.String(),
			Value:   truncate(m, maxViolationValue),
			Reason:  "local_path",
		})
	}

	if projectRoot != "" {
		newContent := strings.ReplaceAll(rewritten, projectRoot+"/", "./")
		newContent = strings.ReplaceAll(newContent, projectRoot, ".")
		if newContent != rewritten {
			rewritten = newContent
		}
	}

	return rewritten, violations
}

func detectSecrets(content string) (string, []RemoteSafetyViolation) {
	s := memory.NewSanitizer(config.SanitizerConfig{Enabled: true})
	sanitized := s.Sanitize(content)

	if sanitized == content {
		return content, nil
	}

	return sanitized, []RemoteSafetyViolation{
		{
			Field:   "content",
			Pattern: "sanitizer_rules",
			Value:   "secrets detected and redacted",
			Reason:  "secret_pattern",
		},
	}
}

func detectPersonalPreference(metadata map[string]any, violations []RemoteSafetyViolation) []RemoteSafetyViolation {
	scope, ok := metadata["scope"].(string)
	if !ok || scope != "user" {
		return violations
	}
	return append(violations, RemoteSafetyViolation{
		Field:   "metadata.scope",
		Pattern: "scope=user",
		Value:   "user",
		Reason:  "personal_preference",
	})
}
