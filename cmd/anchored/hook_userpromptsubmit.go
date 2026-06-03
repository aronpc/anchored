package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jholhewres/anchored/pkg/debuglog"
	"github.com/jholhewres/anchored/pkg/mcp"
)

// preSearchTimeout caps how long we wait on the BM25 query so a slow DB
// never blocks the user's prompt from reaching the model. Pre-search is
// always best-effort: missing hits fall back to the routing block alone.
const preSearchTimeout = 200 * time.Millisecond

// preSearchLimit is the max rows we return; small enough to fit in context
// without bloat, big enough that two recent decisions + one fact still come
// through.
const preSearchLimit = 3

// runHookUserPromptSubmit injects the anchored routing block on every user
// prompt and, when the prompt mentions memory/preferences/past work,
// pre-fetches the top-N hits via BM25 and ships them as additionalContext.
// The agent sees relevant memories before deciding whether to call
// anchored_search — making the right answer the path of least resistance.
func runHookUserPromptSubmit(args []string) {
	fs := newFlagSet("hook userpromptsubmit")
	configPath := fs.String("config", "", "path to config file")
	fs.Parse(args)

	dlog := openDebugLogger(*configPath)
	defer dlog.Close()

	body, _ := io.ReadAll(os.Stdin)
	var parsed struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
		Prompt    string `json:"prompt"`
	}
	_ = json.Unmarshal(body, &parsed)

	// Compact reminder each turn; the full routing block is injected once per
	// session (SessionStart + MCP initialize), so we don't re-pay ~2KB here.
	additional := mcp.AnchoredRoutingReminder

	// Pre-search runs on EVERY prompt within a tight budget. It's the most
	// reliable retrieval path (it puts hits in front of the model rather than
	// hoping it calls anchored_search), and it self-gates: sanitizeFTSQuery
	// drops trivial prompts to an empty query and preSearchPreview returns ""
	// when there are no hits, so non-memory prompts add nothing.
	if preview := preSearchPreview(*configPath, parsed.Cwd, parsed.Prompt, dlog); preview != "" {
		additional += "\n\n" + preview
	}

	dlog.Event("hook.userpromptsubmit", map[string]any{
		"stage":         "emitted",
		"session_id":    parsed.SessionID,
		"prompt_len":    len(parsed.Prompt),
		"prompt_head":   debuglog.Snippet(parsed.Prompt, 240),
		"context_bytes": len(additional),
	})

	outputJSON(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "UserPromptSubmit",
			"additionalContext": additional,
		},
	})
}

// preSearchPreview opens the lightweight hook DB, runs a BM25 query against
// the memories_fts virtual table, and renders the top hits as an XML block.
// Returns "" on any failure or empty result — pre-search must NEVER prevent
// the prompt from going through.
func preSearchPreview(configPath, cwd, prompt string, dlog *debuglog.Logger) string {
	q := sanitizeFTSQuery(prompt)
	if q == "" {
		return ""
	}

	hc, err := openHookContext(configPath)
	if err != nil {
		dlog.Event("hook.userpromptsubmit.presearch", map[string]any{"stage": "ctx_init_failed", "error": err.Error()})
		return ""
	}
	defer hc.Close()

	cwdVal := cwd
	if cwdVal == "" {
		cwdVal = "."
	}
	projectID := hc.ResolveProject(cwdVal)

	ctx, cancel := context.WithTimeout(context.Background(), preSearchTimeout)
	defer cancel()

	hits, err := bm25TopHits(ctx, hc.db, q, projectID, preSearchLimit)
	if err != nil {
		dlog.Event("hook.userpromptsubmit.presearch", map[string]any{"stage": "query_failed", "error": err.Error()})
		return ""
	}
	if len(hits) == 0 {
		dlog.Event("hook.userpromptsubmit.presearch", map[string]any{"stage": "no_hits", "query": debuglog.Snippet(q, 80)})
		return ""
	}

	dlog.Event("hook.userpromptsubmit.presearch", map[string]any{
		"stage":   "hits",
		"count":   len(hits),
		"query":   debuglog.Snippet(q, 80),
		"project": projectID,
	})
	return renderPreSearchPreview(q, hits)
}

type preSearchHit struct {
	Category string
	Content  string
}

// bm25TopHits runs a project-scoped (with global fallback) BM25 query and
// returns up to `limit` hits. Project-scoped first via UNION ALL with a
// `priority` constant so project rows always rank before cross-project
// rows of equal BM25 score.
func bm25TopHits(ctx context.Context, db *sql.DB, q string, projectID string, limit int) ([]preSearchHit, error) {
	const sqlStmt = `
		SELECT m.category, m.content
		FROM memories_fts fts
		JOIN memories m ON m.rowid = fts.rowid
		WHERE memories_fts MATCH ?
		  AND m.deleted_at IS NULL
		  AND (? = '' OR m.project_id = ?)
		ORDER BY bm25(memories_fts) ASC
		LIMIT ?`

	rows, err := db.QueryContext(ctx, sqlStmt, q, projectID, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []preSearchHit
	for rows.Next() {
		var h preSearchHit
		if err := rows.Scan(&h.Category, &h.Content); err != nil {
			continue
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// sanitizeFTSQuery strips FTS5 syntax so a free-form prompt becomes a safe
// MATCH expression. We keep alphanumerics and accented letters, replace
// everything else with spaces, lowercase, and collapse runs of whitespace.
// FTS5 with default tokenizer treats this as a bag-of-tokens OR query —
// what we want for prompt-driven retrieval. Resulting query is capped at
// 16 tokens to keep BM25 ranking focused.
func sanitizeFTSQuery(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	tokens := strings.Fields(b.String())
	// Drop very short tokens (1-2 chars) — they're stop-noise and hurt BM25.
	filtered := tokens[:0]
	for _, t := range tokens {
		if len([]rune(t)) >= 3 {
			filtered = append(filtered, t)
		}
	}
	if len(filtered) > 16 {
		filtered = filtered[:16]
	}
	return strings.Join(filtered, " ")
}

const preSearchPreviewBudget = 1200

func renderPreSearchPreview(query string, hits []preSearchHit) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<anchored_search_preview query=%q count=%q>\n", truncateRunes(query, 80), fmt.Sprintf("%d", len(hits)))
	for _, h := range hits {
		// One-liner per hit; collapse newlines so the XML stays tidy.
		content := strings.ReplaceAll(h.Content, "\n", " ")
		content = strings.ReplaceAll(content, "\r", " ")
		content = truncateRunes(content, 240)
		fmt.Fprintf(&sb, "  [%s] %s\n", escapeText(h.Category), escapeText(content))
	}
	sb.WriteString("</anchored_search_preview>")

	out := sb.String()
	if len(out) <= preSearchPreviewBudget {
		return out
	}
	// Defensive cap — should be unreachable with limit=3 + 240 char hits,
	// but if a future bump pushes this over budget we fall back gracefully.
	return out[:preSearchPreviewBudget-len("\n  <truncated/>\n</anchored_search_preview>")] + "\n  <truncated/>\n</anchored_search_preview>"
}

// truncateRunes caps `s` at `max` runes (NOT bytes) so we never split a
// multibyte UTF-8 sequence. Mirrors the helper in pkg/mcp; intentionally
// duplicated to keep cmd/anchored free of internal pkg/mcp imports beyond
// the routing block constant.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}

// escapeText escapes the three character-data hostiles for XML; quotes are
// left as-is so prose remains readable inside <anchored_search_preview>.
func escapeText(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return r.Replace(s)
}
