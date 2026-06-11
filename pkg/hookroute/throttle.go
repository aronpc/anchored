package hookroute

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Guidance throttle: each advisory type is shown at most once per session
// (guidanceOnce) or on a periodic cadence (guidancePeriodic). State lives in
// per-session marker files under the OS temp dir, created with O_EXCL for
// cross-process atomicity — Claude Code spawns each hook as a fresh process,
// so an in-memory set would never dedupe across tool calls.
//
// This mirrors context-mode's hooks/core/routing.mjs throttle, adapted to Go
// and to anchored's fail-safe contract: any IO error degrades to "fire the
// advisory" rather than dropping it silently — a duplicate nudge is cheaper
// than a missed one.

// markerIDSanitize keeps a session id safe to use as a path segment. Claude
// Code session ids are UUIDs, but a stray slash or NUL from another client
// must never escape the temp dir.
func markerIDSanitize(id string) string {
	if id == "" {
		return "ppid-" + strconv.Itoa(os.Getppid())
	}
	repl := func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}
	return strings.Map(repl, id)
}

func guidanceDir(sessionID string) string {
	return filepath.Join(os.TempDir(), "anchored-guidance-"+markerIDSanitize(sessionID))
}

// guidanceOnce returns a context Decision the first time it is called for
// (sessionID, typ); every subsequent call in the same session returns nil.
func guidanceOnce(typ, content, sessionID string) *Decision {
	dir := guidanceDir(sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		// Can't persist the marker — fire once based on this in-process call.
		return contextDecision(content)
	}
	marker := filepath.Join(dir, typ)
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		// EEXIST (already shown) or other IO error — suppress to avoid spam.
		return nil
	}
	_ = f.Close()
	return contextDecision(content)
}

// guidancePeriodic fires on calls 1, period+1, 2*period+1, … for a given
// (sessionID, typ). Used for advisories that must survive context compaction
// in long sessions (e.g. the memory-recall reminder), where a one-shot nudge
// would be trimmed out of the model's window and never re-applied.
func guidancePeriodic(typ, content, sessionID string, period int) *Decision {
	if period < 1 {
		period = 1
	}
	dir := guidanceDir(sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return contextDecision(content)
	}
	counterPath := filepath.Join(dir, typ+".count")
	count := 0
	if b, err := os.ReadFile(counterPath); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n >= 0 {
			count = n
		}
	}
	next := count + 1
	_ = os.WriteFile(counterPath, []byte(strconv.Itoa(next)), 0o644)
	if (next-1)%period != 0 {
		return nil
	}
	return contextDecision(content)
}

func contextDecision(content string) *Decision {
	return &Decision{Action: ActionContext, AdditionalContext: content}
}

// ResetThrottle clears all markers for a session. Exposed for tests.
func ResetThrottle(sessionID string) {
	_ = os.RemoveAll(guidanceDir(sessionID))
}
