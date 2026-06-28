package eval

import (
	"fmt"
	"strings"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/memory"
	syncpkg "github.com/jholhewres/anchored/pkg/sync"
)

// RunSyncSafety verifies that every item in the privacy fixture is stopped by
// the remote safety filter (+ sanitizer) before it could sync: blocked when
// must_block, and rewritten/redacted when must_redact. A single leak fails the
// run — this is the gate that protects against pushing secrets, local paths, or
// personal-scope content to a shared server.
func RunSyncSafety(fixture []byte) (Report, error) {
	var fix PrivacyFixture
	if err := parseYAML(fixture, &fix); err != nil {
		return Report{}, err
	}

	// The remote safety filter is the primary gate; the sanitizer is the
	// secondary token-redaction layer, enabled here so the eval exercises both.
	sanitizer := memory.NewSanitizer(config.SanitizerConfig{Enabled: true})

	rep := Report{Name: "sync-safety", Passed: true}
	var passCount float64
	for _, item := range fix.Items {
		res := syncpkg.RemoteSafetyFilter(item.Content, item.Metadata, "")
		sanitized := sanitizer.Sanitize(item.Content)

		var problems []string
		if item.MustBlock && !res.Blocked {
			problems = append(problems, "expected BLOCK but filter allowed it")
		}
		if item.MustRedact {
			redacted := res.Rewritten || sanitized != item.Content
			if !redacted {
				problems = append(problems, "expected REDACT but content was unchanged by filter and sanitizer")
			}
		}
		passed := len(problems) == 0
		if !passed {
			rep.Passed = false
		} else {
			passCount++
		}
		rep.Cases = append(rep.Cases, CaseResult{
			Name:   item.Name,
			Passed: passed,
			Detail: detailOrOK(problems, fmt.Sprintf("blocked=%v rewritten=%v violations=%d", res.Blocked, res.Rewritten, len(res.Violations))),
		})
	}
	if len(rep.Cases) > 0 {
		rep.Score = passCount / float64(len(rep.Cases))
	}
	rep.Summary = fmt.Sprintf("%d privacy items, %d safe", len(rep.Cases), int(passCount))
	return rep, nil
}

func detailOrOK(problems []string, ok string) string {
	if len(problems) == 0 {
		return ok
	}
	return strings.Join(problems, "; ")
}
