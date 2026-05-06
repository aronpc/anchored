package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

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

	content, err := io.ReadAll(os.Stdin)
	if err != nil {
		slog.Error("failed to read stdin", "error", err)
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

	_, _, svc, err := initService(*configPath)
	if err != nil {
		// Routing block alone is still useful even if the DB is unavailable.
		emitSessionStart(additional)
		return
	}
	defer svc.Close()

	projectID := svc.ResolveProject(cwdVal)
	ctx := context.Background()
	db := svc.StoreDB()

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
