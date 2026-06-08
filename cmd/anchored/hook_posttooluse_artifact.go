package main

import (
	"regexp"
	"strings"
)

// Artifact-capture thresholds for posttooluse. Small outputs become a session
// event (timeline signal); large outputs are indexed as a searchable artifact
// so they don't bloat the model's context but stay retrievable.
// artifactMinBytes is the output size above which a tool result is captured as
// a searchable artifact rather than summarized into a session event. Smaller
// outputs keep the existing event path (a 500-rune summary).
const artifactMinBytes = 8 * 1024

var (
	stackTraceRe  = regexp.MustCompile(`(?m)(panic:|goroutine \d+ \[|Traceback \(most recent call last\)|\bat [\w.$]+\([\w.]+:\d+\)|Exception in thread)`)
	testOutputRe  = regexp.MustCompile(`(?mi)(--- (FAIL|PASS)|^(ok|FAIL)\s|\d+ passed|\d+ failed|PASS\b|FAIL\b|test result:|=== RUN)`)
	buildOutputRe = regexp.MustCompile(`(?mi)(build (succeeded|failed)|compilation (error|failed)|cannot find module|undefined reference|linker)`)
)

// classifyArtifact picks the artifact type from the tool name and output
// content. Heuristic and deterministic — no LLM. The order matters: a stack
// trace inside test output should still classify as a stack_trace because that
// is the more actionable signal for a debugging recall.
func classifyArtifact(toolName, input, output string) string {
	lowerTool := strings.ToLower(toolName)
	combined := input + "\n" + output

	switch {
	case stackTraceRe.MatchString(output):
		return "stack_trace"
	case isTestCommand(input) || testOutputRe.MatchString(output):
		return "test_report"
	case isBuildCommand(input) || buildOutputRe.MatchString(output):
		return "build_report"
	case strings.Contains(strings.ToLower(input), "git diff") || strings.HasPrefix(strings.TrimSpace(output), "diff --git"):
		return "diff"
	case lowerTool == "webfetch" || lowerTool == "web_fetch":
		return "external_doc"
	case strings.Contains(combined, "\"status\":") && (lowerTool == "bash" && looksLikeHTTP(output)):
		return "api_response"
	default:
		return "command_output"
	}
}

func isTestCommand(input string) bool {
	l := strings.ToLower(input)
	for _, c := range []string{"go test", "npm test", "npm run test", "pytest", "jest", "make test", "cargo test", "go test ./"} {
		if strings.Contains(l, c) {
			return true
		}
	}
	return false
}

func isBuildCommand(input string) bool {
	l := strings.ToLower(input)
	for _, c := range []string{"go build", "npm run build", "make build", "cargo build", "tsc ", "webpack", "vite build"} {
		if strings.Contains(l, c) {
			return true
		}
	}
	return false
}

func looksLikeHTTP(output string) bool {
	o := strings.ToLower(output)
	return strings.Contains(o, "http/1.1") || strings.Contains(o, "http/2") || strings.Contains(o, "content-type:")
}

// artifactSourceLabel derives a short human label for the artifact from the
// tool input (e.g. the command line) so the recall preview can name it.
func artifactSourceLabel(toolName, input string) string {
	in := strings.TrimSpace(input)
	if in == "" {
		return toolName
	}
	in = strings.ReplaceAll(in, "\n", " ")
	if len(in) > 80 {
		in = in[:80]
	}
	return in
}
