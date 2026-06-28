package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jholhewres/anchored/pkg/hookroute"
)

// The context gate is the only DETERMINISTIC channel anchored has to make the
// agent actually consult its persistent memory. Soft injection (the
// SessionStart routing block, the UserPromptSubmit recall) can be — and often
// is — ignored when the user hands the model a concrete, actionable task: the
// model jumps straight to git/grep/edit and never reads what memory already
// knows. The gate closes that hole by DENYING the first substantive tool call
// of a session until the agent has called anchored_context (or a search), which
// surfaces the deny reason to the model and makes consulting memory the only
// way forward.
//
// It is deliberately bounded so it can never become a productivity tax:
//   - fires AT MOST once per session (the marker is sticky once satisfied),
//   - relents after ctxGateMaxDenies so a model that insists on ignoring the
//     redirect is eventually let through rather than wedged,
//   - is fully fail-open: a missing session id or an unwritable state dir means
//     passthrough, never a block.
//
// State lives in one tiny marker file per session under <storage>/ctxgate/.
// We intentionally avoid the DB on this hot path: PreToolUse runs on every
// single tool call, so an os.Stat/ReadFile is the right cost, not a sql.Open.

// ctxGateMaxDenies is how many times the gate will deny work tools before
// relenting for the rest of the session. A clear deny reason usually lands on
// the first try; the budget exists purely so a non-compliant model can never be
// permanently blocked from working.
const ctxGateMaxDenies = 3

// ctxGateMarkerTTL bounds how long stale per-session markers linger before the
// lazy sweep on the (rare) deny path removes them.
const ctxGateMarkerTTL = 7 * 24 * time.Hour

// ctxGateSatisfied is the marker content once memory has been consulted (or the
// gate has relented). Any other content is a decimal deny counter.
const ctxGateSatisfied = "ok"

// contextGateSatisfyingTools are the anchored MCP leaf tools whose use proves
// the agent engaged its memory and therefore satisfies the gate for the rest of
// the session. Loading context OR searching both count.
var contextGateSatisfyingTools = map[string]bool{
	"anchored_context":    true,
	"anchored_search":     true,
	"anchored_ctx_search": true,
	"anchored_kg_query":   true,
}

// isAnchoredTool reports whether a tool belongs to anchored itself. Such tools
// must never be gated: gating them would deadlock the session, since the only
// way to satisfy the gate is to call one of them.
func isAnchoredTool(bareTool, fullTool string) bool {
	return strings.HasPrefix(fullTool, "mcp__anchored__") || strings.HasPrefix(bareTool, "anchored_")
}

// contextGateDecision applies the optional PreToolUse context gate. It returns
// a non-nil Decision when the caller must DENY the tool, plus a short stage
// string for debug logging. A nil Decision means passthrough (caller continues
// to routing/allow). It is fail-open by contract: any unexpected condition
// yields passthrough so the gate can never block on infrastructure faults.
//
// storageDir is cfg.Memory.StorageDir (already home-expanded); sessionID is the
// client session id; bareTool is the leaf tool name (server prefix stripped)
// and fullTool the wire name.
func contextGateDecision(storageDir, sessionID, bareTool, fullTool string) (*hookroute.Decision, string) {
	if sessionID == "" || storageDir == "" {
		return nil, "skip_no_session"
	}

	gateDir := filepath.Join(storageDir, "ctxgate")
	marker := filepath.Join(gateDir, sanitizeSessionID(sessionID))

	// Consulting memory satisfies the gate for the rest of the session. Do this
	// check first so an agent that correctly leads with anchored_context sees
	// zero friction.
	if contextGateSatisfyingTools[bareTool] {
		writeGateMarker(gateDir, marker, ctxGateSatisfied)
		return nil, "satisfied"
	}

	// Never gate anchored's own tools (would deadlock).
	if isAnchoredTool(bareTool, fullTool) {
		return nil, "exempt_anchored"
	}

	data, readErr := os.ReadFile(marker)
	content := ""
	if readErr == nil {
		content = strings.TrimSpace(string(data))
	}

	// Already satisfied (or relented): never block again this session.
	if content == ctxGateSatisfied {
		return nil, "already_satisfied"
	}

	denies := 0
	if content != "" {
		if n, err := strconv.Atoi(content); err == nil {
			denies = n
		}
	}

	// Relent: a model that ignored the redirect ctxGateMaxDenies times is let
	// through so it can never be permanently wedged.
	if denies >= ctxGateMaxDenies {
		writeGateMarker(gateDir, marker, ctxGateSatisfied)
		return nil, "relented"
	}

	// Record the deny and block. If we can't even create the state dir, fail
	// open rather than block the user on a filesystem fault.
	if err := os.MkdirAll(gateDir, 0o755); err != nil {
		return nil, "skip_mkdir_failed"
	}
	_ = os.WriteFile(marker, []byte(strconv.Itoa(denies+1)), 0o644)
	sweepStaleGateMarkers(gateDir)

	return &hookroute.Decision{
		Action: hookroute.ActionDeny,
		Reason: "anchored: consult your persistent memory before working. Call " +
			"anchored_context(cwd: \"<this project's absolute path>\") to load identity, " +
			"the project, and recent decisions — your prior work and the user's conventions " +
			"live there, not in the codebase. (Already know what you need? anchored_search works too.) " +
			"If the anchored_* tools are not loaded yet (deferred — a direct call fails as not-found), " +
			"FIRST run ToolSearch(query: \"select:mcp__anchored__anchored_context,mcp__anchored__anchored_search\") " +
			"to load them, THEN call anchored_context — do not retry the blocked tool until you have. " +
			"Calling anchored_context (or a search) clears this gate for the rest of the session. " +
			"It is NOT a permanent block: it auto-relents after a few denies, so consulting memory is the " +
			"way through, not retrying the same tool.",
	}, "denied"
}

// satisfyGateFromPostToolUse is the redundant satisfaction path. PostToolUse
// fires AFTER a tool runs, so if the agent DID call a satisfying anchored tool
// (anchored_context/search/ctx_search/kg_query), mark the gate satisfied for
// the rest of the session — even when the PreToolUse credit was missed. The
// missed-credit case is real: a stale plugin hooks.json whose PreToolUse
// matcher doesn't fire for mcp__ tools would otherwise let the gate deny the
// agent's work three times and relent, never crediting the memory call. This
// PostToolUse path closes that hole. Best-effort and a no-op unless the tool is
// a satisfying anchored tool. Returns true when it wrote the satisfied marker.
func satisfyGateFromPostToolUse(storageDir, sessionID, bareTool string) bool {
	if storageDir == "" || sessionID == "" || !contextGateSatisfyingTools[bareTool] {
		return false
	}
	gateDir := filepath.Join(storageDir, "ctxgate")
	writeGateMarker(gateDir, filepath.Join(gateDir, sanitizeSessionID(sessionID)), ctxGateSatisfied)
	return true
}

// writeGateMarker best-effort writes content to the session marker, creating the
// gate dir if needed. Errors are swallowed: a failed write degrades to "gate not
// yet satisfied", which at worst costs one extra (bounded) deny — never a block.
func writeGateMarker(gateDir, marker, content string) {
	if err := os.MkdirAll(gateDir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(marker, []byte(content), 0o644)
}

// sanitizeSessionID turns a client session id into a filesystem-safe marker
// name. Non [A-Za-z0-9_-] runes become '_'; the result is capped and suffixed.
func sanitizeSessionID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if len(s) > 128 {
		s = s[:128]
	}
	if s == "" {
		s = "_"
	}
	return s + ".gate"
}

// sweepStaleGateMarkers removes markers older than ctxGateMarkerTTL. Best-effort
// and only invoked on the rare deny path, so it never touches the hot allow
// path. Keeps the ctxgate dir from accumulating one file per session forever.
func sweepStaleGateMarkers(gateDir string) {
	entries, err := os.ReadDir(gateDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-ctxGateMarkerTTL)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(gateDir, e.Name()))
		}
	}
}
