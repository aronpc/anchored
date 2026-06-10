package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jholhewres/anchored/pkg/contextbudget"
	"github.com/jholhewres/anchored/pkg/debuglog"
	"github.com/jholhewres/anchored/pkg/mcp"
	"github.com/jholhewres/anchored/pkg/session"
)

const sessionStartQueryTimeout = 150 * time.Millisecond

// runHookSessionStart emits a Claude Code SessionStart hook payload that
// injects the anchored routing block plus a project-scoped recap of recent
// decisions / events. The shape `{hookSpecificOutput:{hookEventName,
// additionalContext}}` is the contract Claude Code reads to add a system
// reminder for the model. Cursor / OpenCode follow the same convention.
func runHookSessionStart(args []string) {
	fs := newFlagSet("hook sessionstart")
	sessionID := fs.String("session-id", "", "session identifier")
	configPath := fs.String("config", "", "path to config file")
	cwd := fs.String("cwd", "", "current working directory")
	fs.Parse(args)

	dlog := openDebugLogger(*configPath)
	defer dlog.Close()

	content, err := io.ReadAll(os.Stdin)
	if err != nil {
		slog.Error("failed to read stdin", "error", err)
		dlog.Event("hook.sessionstart", map[string]any{"stage": "stdin_error", "error": err.Error()})
		emitSessionStart(mcp.AnchoredRoutingBlock)
		return
	}

	var input struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
		Directory string `json:"directory"`
	}
	_ = json.Unmarshal(content, &input)

	_ = sessionID

	cwdVal := *cwd
	if cwdVal == "" {
		cwdVal = input.Cwd
	}
	if cwdVal == "" {
		cwdVal = input.Directory
	}
	if cwdVal == "" {
		cwdVal = "."
	}

	resolvedSessionID := input.SessionID

	additional := mcp.AnchoredRoutingBlock

	// Plugin drift check: when the installed plugin cache is older than the
	// running binary, the user is missing hooks/skills from the new release.
	// We always notify; if config.Plugin.AutoUpdate is on we also fast-
	// forward the marketplace mirror + wipe the stale cache so Claude Code
	// reinstalls on its next launch.
	cfg, _ := loadConfig(*configPath)
	if cfg != nil {
		drift := detectPluginDrift(cfg, Version)
		if drift.HasDrift {
			if cfg.Plugin.AutoUpdate {
				drift = applyPluginAutoUpdate(drift)
			}
			if notice := renderPluginUpdateNotice(drift); notice != "" {
				additional += "\n\n" + notice
			}
			dlog.Event("hook.sessionstart", map[string]any{
				"stage":               "plugin_drift",
				"binary":              drift.BinaryVersion,
				"mirror":              drift.MirrorVersion,
				"cache":               drift.CacheVersion,
				"mirror_behind":       drift.MirrorBehind,
				"cache_behind":        drift.CacheBehind,
				"mirror_synced":       drift.SyncPerformed,
				"sync_error":          drift.SyncError,
				"cache_installed":     drift.CacheInstalled,
				"cache_install_error": drift.CacheInstallError,
				"marketplace_dir":     drift.MarketplaceDir,
				"cache_dir":           drift.CacheDir,
			})
		}
	}

	dlog.Event("hook.sessionstart", map[string]any{
		"stage":      "start",
		"session_id": input.SessionID,
		"cwd":        cwdVal,
		"input_len":  len(content),
		"input_head": debuglog.Snippet(string(content), 200),
	})

	hc, err := openHookContext(*configPath)
	if err != nil {
		dlog.Event("hook.sessionstart", map[string]any{"stage": "service_init_failed", "error": err.Error()})
		// Routing block alone is still useful even if the DB is unavailable.
		emitSessionStart(additional)
		return
	}
	defer hc.Close()

	projectID := hc.ResolveProject(cwdVal)
	ctx := context.Background()

	// When sessionstart_budget_bytes == 0 the user has opted out of the rich
	// block; fall back to the original plain format (RoutingBlock + events).
	budget := 7000
	if cfg != nil {
		budget = cfg.Plugin.SessionStartBudget()
	}

	if budget == 0 {
		// Legacy path: plain recent_events block, no budgeter.
		appendLegacyRecentEvents(ctx, hc, projectID, &additional)
		dlog.Event("hook.sessionstart", map[string]any{
			"stage":      "emitted_legacy",
			"project_id": projectID,
		})
		emitSessionStart(additional)
		return
	}

	// Rich path: assemble tiers via contextbudget.
	tiers := buildSessionStartTiers(ctx, hc, resolvedSessionID, projectID)
	richBlock, dropped := contextbudget.Assemble(tiers, budget)

	if richBlock != "" {
		additional += "\n\n<anchored_context>\n" + richBlock + "\n</anchored_context>"
	}

	dlog.Event("hook.sessionstart", map[string]any{
		"stage":         "emitted",
		"project_id":    projectID,
		"context_bytes": len(richBlock),
		"dropped_items": dropped,
		"budget_bytes":  budget,
	})
	emitSessionStart(additional)
}

// buildSessionStartTiers assembles the four tiers for the rich context block.
// Each tier is best-effort: any DB error causes that tier to be empty (fail-safe).
func buildSessionStartTiers(ctx context.Context, hc *HookContext, sessionID, projectID string) []contextbudget.Tier {
	queryCtx, cancel := context.WithTimeout(ctx, sessionStartQueryTimeout)
	defer cancel()

	// ── Tier 0: identity ────────────────────────────────────────────────────
	var identityItems []contextbudget.Item
	if id := readSessionIdentity(); id != "" {
		identityItems = []contextbudget.Item{{Text: id, Priority: 0}}
	}

	// ── Tier 1: decisions (pinned + decision/learning, top 5) ───────────────
	decisionItems := queryDecisions(queryCtx, hc, projectID)

	// ── Tier 2: task (working set) ───────────────────────────────────────────
	var taskItems []contextbudget.Item
	if sessionID != "" {
		wsMgr := session.NewManager(hc.db, nil)
		ws, err := wsMgr.GetWorkingSet(ctx, sessionID)
		if err == nil && ws != nil && !ws.Empty() {
			taskItems = []contextbudget.Item{{Text: renderWorkingSetCompact(ws), Priority: 0}}
		}
	}

	// ── Tier 3: recent events ────────────────────────────────────────────────
	eventItems := queryRecentEvents(queryCtx, hc, projectID)

	return []contextbudget.Tier{
		{Name: "identity", Items: identityItems, MinItems: 1},
		{Name: "decisions", Items: decisionItems, MinItems: 1},
		{Name: "task", Items: taskItems, MinItems: 0},
		{Name: "events", Items: eventItems, MinItems: 0},
	}
}

// readSessionIdentity reads ~/.anchored/identity.md, capped at 600 runes.
func readSessionIdentity() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".anchored", "identity.md"))
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(data))
	return sessionTruncateRunes(s, 600)
}

// queryDecisions returns up to 5 decision/learning memories for the project,
// pinned first then by recency. Fail-safe: errors return nil.
func queryDecisions(ctx context.Context, hc *HookContext, projectID string) []contextbudget.Item {
	rows, err := hc.db.QueryContext(ctx, `
		SELECT content, category,
		       COALESCE(json_extract(metadata, '$.pinned'), 0) AS pinned
		FROM memories
		WHERE (project_id = ? OR project_id = '' OR project_id IS NULL)
		  AND deleted_at IS NULL
		  AND (COALESCE(json_extract(metadata, '$.pinned'), 0) = 1
		       OR category IN ('decision', 'learning'))
		  AND COALESCE(json_extract(metadata, '$.curation_status'), 'ok')
		      NOT IN ('low_signal', 'near_duplicate', 'rejected')
		ORDER BY COALESCE(json_extract(metadata, '$.pinned'), 0) DESC, created_at DESC
		LIMIT 5`,
		projectID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var items []contextbudget.Item
	for rows.Next() {
		var content, category string
		var pinned int
		if err := rows.Scan(&content, &category, &pinned); err != nil {
			continue
		}
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		text := fmt.Sprintf("<memory category=%q pinned=%d>%s</memory>",
			sessionEscapeAttr(category), pinned, escapeText(content))
		priority := 1
		if pinned == 1 {
			priority = 0
		}
		items = append(items, contextbudget.Item{Text: text, Priority: priority})
	}
	// A mid-iteration error (context deadline, I/O) leaves the set partial;
	// honour the fail-safe contract by treating it as empty rather than
	// presenting an incomplete block as complete.
	if err := rows.Err(); err != nil {
		return nil
	}
	return items
}

// queryRecentEvents returns up to 8 recent session events (priority <= 2).
// Fail-safe: errors return nil.
func queryRecentEvents(ctx context.Context, hc *HookContext, projectID string) []contextbudget.Item {
	rows, err := hc.db.QueryContext(ctx, `
		SELECT event_type, summary FROM session_events
		WHERE priority <= 2 AND (project_id = ? OR project_id = '')
		ORDER BY created_at DESC LIMIT 8`,
		projectID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var items []contextbudget.Item
	for rows.Next() {
		var eventType, summary string
		if err := rows.Scan(&eventType, &summary); err != nil {
			continue
		}
		if strings.TrimSpace(summary) == "" {
			continue
		}
		text := fmt.Sprintf("<event type=%q>%s</event>",
			sessionEscapeAttr(eventType), escapeText(summary))
		items = append(items, contextbudget.Item{Text: text, Priority: len(items)})
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return items
}

// renderWorkingSetCompact renders a single compact line from the working set.
func renderWorkingSetCompact(ws *session.WorkingSet) string {
	var parts []string
	if len(ws.Files) > 0 {
		parts = append(parts, "files="+strings.Join(ws.Files, ","))
	}
	if len(ws.Tests) > 0 {
		parts = append(parts, "tests="+strings.Join(ws.Tests, ","))
	}
	if len(ws.Symbols) > 0 {
		parts = append(parts, "symbols="+strings.Join(ws.Symbols, ","))
	}
	if ws.TopicKey != "" {
		parts = append(parts, "topic="+ws.TopicKey)
	}
	return "<working_set>" + escapeText(strings.Join(parts, " ")) + "</working_set>"
}

// appendLegacyRecentEvents appends the original plain recent_events block.
// Used when sessionstart_budget_bytes == 0 (opt-out of rich block).
func appendLegacyRecentEvents(ctx context.Context, hc *HookContext, projectID string, additional *string) {
	type recentEvent struct {
		EventType string
		Summary   string
	}
	var recent []recentEvent
	rows, err := hc.db.QueryContext(ctx,
		`SELECT event_type, summary FROM session_events
		 WHERE priority <= 2 AND (project_id = ? OR project_id = '')
		 ORDER BY created_at DESC LIMIT 8`,
		projectID,
	)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var e recentEvent
			if err := rows.Scan(&e.EventType, &e.Summary); err == nil && strings.TrimSpace(e.Summary) != "" {
				recent = append(recent, e)
			}
		}
	}

	if len(recent) > 0 {
		var sb strings.Builder
		sb.WriteString("\n\n<anchored_recent_events>\n")
		for _, e := range recent {
			fmt.Fprintf(&sb, "  <event type=%q>%s</event>\n", e.EventType, e.Summary)
		}
		sb.WriteString("</anchored_recent_events>")
		*additional += sb.String()
	}
}

// sessionTruncateRunes caps a string at max runes (not bytes), preserving UTF-8.
func sessionTruncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}

// sessionEscapeAttr escapes characters that would break XML double-quoted attrs.
func sessionEscapeAttr(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;",
		"\"", "&quot;", "\r", "&#xD;", "\n", "&#xA;", "\t", "&#x9;",
	)
	return r.Replace(s)
}

func emitSessionStart(additional string) {
	outputJSON(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": additional,
		},
	})
}
