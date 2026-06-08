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
	"github.com/jholhewres/anchored/pkg/intent"
	"github.com/jholhewres/anchored/pkg/mcp"
)

// preSearchTimeout caps how long we wait on the BM25 query so a slow DB
// never blocks the user's prompt from reaching the model. Pre-search is
// always best-effort: missing hits fall back to the routing block alone.
const preSearchTimeout = 150 * time.Millisecond

// preSearchLimit is the max rows we return; small enough to fit in context
// without bloat, big enough that two recent decisions + one fact still come
// through.
const preSearchLimit = 3

// recallMinTokens is the floor of meaningful tokens an unknown-intent prompt
// must have before we run retrieval. Trivial prompts ("oi", "hi", "thanks")
// sanitize below this and inject nothing but the reminder.
const recallMinTokens = 3

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

	// Intent-aware auto-recall runs on EVERY prompt within a tight budget. It
	// puts relevant memories (and, for debugging, recent artifacts) in front of
	// the model rather than hoping it calls anchored_search, and self-gates:
	// trivial/unknown prompts inject nothing. Gated by plugin.auto_recall.
	if preview := autoRecallPreview(*configPath, parsed.Cwd, parsed.Prompt, dlog); preview != "" {
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

// categoriesForIntent maps a detected intent to the memory categories worth
// boosting in retrieval. An empty result means "no category filter" (search
// everything) — used for code_change and unknown where the relevant memory
// could be any kind.
func categoriesForIntent(k intent.Kind) []string {
	switch k {
	case intent.KindPlanning, intent.KindArchitecture:
		return []string{"decision", "plan", "learning"}
	case intent.KindDebugging:
		return []string{"learning", "decision"}
	case intent.KindRelease:
		return []string{"decision", "plan", "summary"}
	case intent.KindSecurity:
		return []string{"decision", "learning"}
	default:
		return nil
	}
}

// autoRecallPreview is the intent-aware retrieval path. It classifies the
// prompt, runs a (optionally category-boosted) BM25 query, and for
// debugging/test intents in "full" mode also surfaces recent artifacts
// (test reports, stack traces). Returns "" on any failure, empty result, or
// when auto_recall is off — it must NEVER prevent the prompt from going through
// and must never exit non-zero.
func autoRecallPreview(configPath, cwd, prompt string, dlog *debuglog.Logger) string {
	q := sanitizeFTSQuery(prompt)
	if q == "" {
		return ""
	}

	hc, err := openHookContextReadOnly(configPath)
	if err != nil {
		dlog.Event("hook.userpromptsubmit.recall", map[string]any{"stage": "ctx_init_failed", "error": err.Error()})
		return ""
	}
	defer hc.Close()

	mode := hc.cfg.Plugin.AutoRecallMode()
	if mode == "off" {
		return ""
	}

	in := intent.Detect(prompt)

	// Unknown intent is the noisy case: only retrieve when the prompt carries
	// enough signal (>= recallMinTokens meaningful tokens), so chit-chat that
	// happens to share a word with a memory doesn't trigger injection.
	if in.Kind == intent.KindUnknown && len(strings.Fields(q)) < recallMinTokens {
		dlog.Event("hook.userpromptsubmit.recall", map[string]any{"stage": "below_threshold", "tokens": len(strings.Fields(q))})
		return ""
	}

	cwdVal := cwd
	if cwdVal == "" {
		cwdVal = "."
	}
	projectID := hc.ResolveProject(cwdVal)

	ctx, cancel := context.WithTimeout(context.Background(), preSearchTimeout)
	defer cancel()

	hits, err := bm25TopHits(ctx, hc.db, q, projectID, preSearchLimit, categoriesForIntent(in.Kind)...)
	if err != nil {
		dlog.Event("hook.userpromptsubmit.recall", map[string]any{"stage": "query_failed", "error": err.Error()})
		return ""
	}

	// For debugging/test intents in "full" mode, surface the most recent
	// captured artifacts so the model can pull the failing test output or
	// stack trace without guessing it exists.
	var arts []recentArtifact
	if mode == "full" && (in.Kind == intent.KindDebugging || in.Kind == intent.KindTestExecution) {
		arts = recentArtifacts(ctx, hc.db, projectID, []string{"test_report", "stack_trace", "build_report"}, 3)
	}

	if len(hits) == 0 && len(arts) == 0 {
		dlog.Event("hook.userpromptsubmit.recall", map[string]any{"stage": "no_hits", "intent": string(in.Kind), "query": debuglog.Snippet(q, 80)})
		return ""
	}

	dlog.Event("hook.userpromptsubmit.recall", map[string]any{
		"stage":     "hits",
		"intent":    string(in.Kind),
		"hits":      len(hits),
		"artifacts": len(arts),
		"query":     debuglog.Snippet(q, 80),
		"project":   projectID,
	})
	return renderRecallPreview(q, in.Kind, hits, arts, hc.cfg.Plugin.HookBudget())
}

type preSearchHit struct {
	Category string
	Content  string
}

// bm25TopHits runs a project-scoped (with global fallback) BM25 query and
// returns up to `limit` hits. When categories are given, results are filtered
// to those memory categories (the intent-aware boost); empty means no filter.
func bm25TopHits(ctx context.Context, db *sql.DB, q string, projectID string, limit int, categories ...string) ([]preSearchHit, error) {
	sqlStmt := `
		SELECT m.category, m.content
		FROM memories_fts fts
		JOIN memories m ON m.rowid = fts.rowid
		WHERE memories_fts MATCH ?
		  AND m.deleted_at IS NULL
		  AND (? = '' OR m.project_id = ?)`
	args := []any{q, projectID, projectID}
	if len(categories) > 0 {
		placeholders := make([]string, len(categories))
		for i, c := range categories {
			placeholders[i] = "?"
			args = append(args, c)
		}
		sqlStmt += " AND m.category IN (" + strings.Join(placeholders, ",") + ")"
	}
	sqlStmt += " ORDER BY bm25(memories_fts) ASC LIMIT ?"
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, sqlStmt, args...)
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

// recentArtifact is a lightweight view of a captured artifact for the recall
// preview — enough for the model to know it exists and search it.
type recentArtifact struct {
	Type        string
	SourceLabel string
	AgeHint     string
}

// recentArtifacts returns the newest artifacts of the given types for the
// project (empty projectID matches all). Best-effort: returns nil on any error
// (e.g. an older DB without the artifacts table). Never blocks the hook.
func recentArtifacts(ctx context.Context, db *sql.DB, projectID string, types []string, limit int) []recentArtifact {
	if len(types) == 0 {
		return nil
	}
	placeholders := make([]string, len(types))
	args := []any{projectID, projectID}
	for i, t := range types {
		placeholders[i] = "?"
		args = append(args, t)
	}
	args = append(args, limit)
	stmt := `
		SELECT type, source_label, created_at
		FROM artifacts
		WHERE (? = '' OR project_id = ?)
		  AND type IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY created_at DESC
		LIMIT ?`

	rows, err := db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []recentArtifact
	for rows.Next() {
		var a recentArtifact
		var created string
		if err := rows.Scan(&a.Type, &a.SourceLabel, &created); err != nil {
			continue
		}
		a.AgeHint = created
		out = append(out, a)
	}
	return out
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

// renderRecallPreview renders the recall block under a byte budget. Hits are
// already relevance-ordered (best BM25 first); when the block would exceed the
// budget we drop the lowest-relevance trailing hits rather than truncating a
// hit mid-content, so what survives is always coherent and the most relevant.
func renderRecallPreview(query string, kind intent.Kind, hits []preSearchHit, arts []recentArtifact, budget int) string {
	render := func(hh []preSearchHit) string {
		var sb strings.Builder
		fmt.Fprintf(&sb, "<anchored_recall intent=%q query=%q count=%q>\n",
			escapeText(string(kind)), truncateRunes(query, 80), fmt.Sprintf("%d", len(hh)))
		for _, h := range hh {
			content := strings.ReplaceAll(h.Content, "\n", " ")
			content = strings.ReplaceAll(content, "\r", " ")
			content = truncateRunes(content, 240)
			fmt.Fprintf(&sb, "  [%s] %s\n", escapeText(h.Category), escapeText(content))
		}
		for _, a := range arts {
			fmt.Fprintf(&sb, "  <artifact type=%q label=%q/> (search with: anchored artifact search)\n",
				escapeText(a.Type), escapeText(truncateRunes(a.SourceLabel, 80)))
		}
		sb.WriteString("</anchored_recall>")
		return sb.String()
	}

	// Drop trailing (lowest-relevance) hits until it fits. Artifacts are few
	// and short; keep them. Always emit at least the top hit if present.
	for n := len(hits); n >= 0; n-- {
		out := render(hits[:n])
		if len(out) <= budget || n == 0 {
			if n == 0 && len(arts) == 0 {
				return ""
			}
			return out
		}
	}
	return ""
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
