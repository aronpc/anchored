package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/jholhewres/anchored/pkg/debuglog"
	"github.com/jholhewres/anchored/pkg/mcp"
)

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
	db := hc.db

	// Recent project-scoped events (decisions, learnings, deployments, etc.).
	type recentEvent struct {
		EventType string
		Summary   string
	}
	var recent []recentEvent
	rows, err := db.QueryContext(ctx,
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
		additional += sb.String()
	}

	dlog.Event("hook.sessionstart", map[string]any{
		"stage":         "emitted",
		"project_id":    projectID,
		"recent_events": len(recent),
		"context_bytes": len(additional),
	})
	emitSessionStart(additional)
}

func emitSessionStart(additional string) {
	outputJSON(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": additional,
		},
	})
}
