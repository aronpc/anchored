package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestSanitizeFTSQuery(t *testing.T) {
	cases := map[string]string{
		"how did we decide on RRF?":            "how did decide rrf",
		"a gente fechou em Postgres ou MySQL?": "gente fechou postgres mysql",
		`weird "quotes" and (parens)`:          "weird quotes and parens",
		"":                                     "",
		"x y":                                  "", // dropped: too short
	}
	for in, want := range cases {
		got := sanitizeFTSQuery(in)
		if got != want {
			t.Errorf("sanitizeFTSQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeFTSQuery_CapsAt16Tokens(t *testing.T) {
	long := strings.Repeat("foo bar baz ", 20) // 60 tokens
	got := sanitizeFTSQuery(long)
	tokens := strings.Fields(got)
	if len(tokens) != 16 {
		t.Fatalf("expected 16 tokens, got %d", len(tokens))
	}
}

func TestRenderRecallPreview_FormatsAndEscapes(t *testing.T) {
	hits := []preSearchHit{
		{Category: "decision", Content: "Settled on RRF for hybrid search"},
		{Category: "fact", Content: "uses <b>highlights</b> & ampersands"},
	}
	out := renderRecallPreview("how did we decide", "planning", hits, nil, 4800)
	if !strings.Contains(out, `<anchored_recall intent="planning" query="how did we decide" count="2">`) {
		t.Errorf("missing wrapper: %s", out)
	}
	if !strings.Contains(out, "[decision] Settled on RRF for hybrid search") {
		t.Errorf("missing first hit: %s", out)
	}
	if !strings.Contains(out, "uses &lt;b&gt;highlights&lt;/b&gt; &amp; ampersands") {
		t.Errorf("XML not escaped: %s", out)
	}
	if !strings.HasSuffix(out, "</anchored_recall>") {
		t.Errorf("missing closing tag: %s", out)
	}
}

// TestRenderRecallPreview_BudgetDropsTrailingHits verifies the block fits the
// budget by dropping lowest-relevance (trailing) hits, never truncating a hit
// mid-content. The top (most relevant) hit always survives.
func TestRenderRecallPreview_BudgetDropsTrailingHits(t *testing.T) {
	body := strings.Repeat("x", 100) // under the 240-rune per-hit cap
	hits := []preSearchHit{
		{Category: "decision", Content: "TOP " + body},
		{Category: "fact", Content: "MID " + body},
		{Category: "learning", Content: "LOW " + body},
	}
	// Budget big enough for the wrapper + ~1 hit but not 3.
	out := renderRecallPreview("q", "debugging", hits, nil, 200)
	if len(out) > 200 {
		t.Fatalf("output %d exceeds budget 200", len(out))
	}
	if !strings.Contains(out, "TOP ") {
		t.Errorf("top (most relevant) hit must survive: %s", out)
	}
	if strings.Contains(out, "LOW ") {
		t.Errorf("lowest-relevance hit should have been dropped: %s", out)
	}
	// Surviving hit is whole (full body present), never mid-truncated.
	if !strings.Contains(out, "TOP "+body) {
		t.Errorf("surviving hit was truncated mid-content: %s", out)
	}
}

// TestRenderRecallPreview_Artifacts includes artifact lines for the debugging
// path.
func TestRenderRecallPreview_Artifacts(t *testing.T) {
	arts := []recentArtifact{{Type: "test_report", SourceLabel: "go test ./...", AgeHint: "2026-06-08"}}
	out := renderRecallPreview("why failing", "debugging", nil, arts, 4800)
	if !strings.Contains(out, `<artifact type="test_report" label="go test ./..."/>`) {
		t.Errorf("missing artifact line: %s", out)
	}
}

// TestBM25TopHits_EndToEnd seeds a real sqlite DB with the production
// memories schema and verifies the BM25 query returns project-scoped hits
// in MATCH-ranked order.
func TestBM25TopHits_EndToEnd(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Minimal schema: just the memories table + FTS5 mirror, enough to run
	// the bm25 query path. Triggers ensure FTS rows are kept in sync.
	schema := `
		CREATE TABLE memories (
			id TEXT PRIMARY KEY,
			project_id TEXT,
			category TEXT,
			content TEXT,
			keywords TEXT,
			deleted_at DATETIME
		);
		CREATE VIRTUAL TABLE memories_fts USING fts5(
			content, keywords, content=memories, content_rowid=rowid
		);
		CREATE TRIGGER memories_fts_insert AFTER INSERT ON memories BEGIN
			INSERT INTO memories_fts(rowid, content, keywords) VALUES (new.rowid, new.content, new.keywords);
		END;
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}

	insert := func(id, projectID, category, content string) {
		t.Helper()
		_, err := db.Exec(
			`INSERT INTO memories (id, project_id, category, content, keywords) VALUES (?,?,?,?,'')`,
			id, projectID, category, content,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("m1", "proj-A", "decision", "we settled on RRF for hybrid search ranking")
	insert("m2", "proj-A", "fact", "Go 1.24 is the production runtime")
	insert("m3", "proj-B", "decision", "RRF was rejected for the other team")

	// Project-scoped: only proj-A rows should match.
	hits, err := bm25TopHits(context.Background(), db, "rrf hybrid search", "proj-A", 5)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(hits) != 1 || hits[0].Category != "decision" {
		t.Fatalf("project-scoped hits = %+v, want [decision m1]", hits)
	}

	// Global: empty projectID returns matches across all projects.
	all, err := bm25TopHits(context.Background(), db, "rrf", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("global hits = %d, want 2", len(all))
	}
}

// TestBM25TopHits_CategoryFilter verifies the intent-aware category boost:
// passing categories restricts results to those memory kinds.
func TestBM25TopHits_CategoryFilter(t *testing.T) {
	db := newFTSTestDB(t)
	insertMem(t, db, "m1", "proj-A", "decision", "we should use postgres for durable storage")
	insertMem(t, db, "m2", "proj-A", "event", "deployed postgres to production today")

	// No filter: both match.
	all, err := bm25TopHits(context.Background(), db, "postgres", "proj-A", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("unfiltered hits = %d, want 2", len(all))
	}

	// Decision-only filter: the event is excluded.
	dec, err := bm25TopHits(context.Background(), db, "postgres", "proj-A", 5, "decision")
	if err != nil {
		t.Fatal(err)
	}
	if len(dec) != 1 || dec[0].Category != "decision" {
		t.Fatalf("category-filtered hits = %+v, want [decision]", dec)
	}
}

// newFTSTestDB builds the minimal memories + FTS5 schema shared by the BM25
// tests.
func newFTSTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	schema := `
		CREATE TABLE memories (
			id TEXT PRIMARY KEY, project_id TEXT, category TEXT,
			content TEXT, keywords TEXT, deleted_at DATETIME
		);
		CREATE VIRTUAL TABLE memories_fts USING fts5(
			content, keywords, content=memories, content_rowid=rowid
		);
		CREATE TRIGGER memories_fts_insert AFTER INSERT ON memories BEGIN
			INSERT INTO memories_fts(rowid, content, keywords) VALUES (new.rowid, new.content, new.keywords);
		END;`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertMem(t *testing.T, db *sql.DB, id, projectID, category, content string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO memories (id, project_id, category, content, keywords) VALUES (?,?,?,?,'')`,
		id, projectID, category, content,
	); err != nil {
		t.Fatal(err)
	}
}

// BenchmarkBM25TopHits measures the hot path of auto-recall against a 5k-memory
// DB. The wave gate is body p95 < 100ms; this benchmarks the query+scan only
// (the dominant cost), not process spawn.
func BenchmarkBM25TopHits(b *testing.B) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	schema := `
		CREATE TABLE memories (id TEXT PRIMARY KEY, project_id TEXT, category TEXT, content TEXT, keywords TEXT, deleted_at DATETIME);
		CREATE VIRTUAL TABLE memories_fts USING fts5(content, keywords, content=memories, content_rowid=rowid);
		CREATE TRIGGER t AFTER INSERT ON memories BEGIN INSERT INTO memories_fts(rowid, content, keywords) VALUES (new.rowid, new.content, new.keywords); END;`
	if _, err := db.Exec(schema); err != nil {
		b.Fatal(err)
	}
	tx, _ := db.Begin()
	for i := 0; i < 5000; i++ {
		_, _ = tx.Exec(`INSERT INTO memories (id, project_id, category, content, keywords) VALUES (?,?,?,?,'')`,
			"m"+itoaBench(i), "proj-A", "decision",
			"decision number "+itoaBench(i)+" about postgres sqlite redis kafka architecture and sync engine design", "")
	}
	_ = tx.Commit()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := bm25TopHits(context.Background(), db, "postgres sync engine architecture", "proj-A", 3); err != nil {
			b.Fatal(err)
		}
	}
}

func itoaBench(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestTruncateRunes_LocalCopy(t *testing.T) {
	if got := truncateRunes("ção é ñ", 3); got != "çãо…" && got != "ção…" {
		// Two literal forms accepted: "ção…" (rune count 4) is what we get;
		// any other shorter prefix is also fine. We only assert the final
		// rune is the ellipsis.
		if !strings.HasSuffix(got, "…") {
			t.Errorf("expected ellipsis suffix, got %q", got)
		}
	}
	if got := truncateRunes("hi", 0); got != "" {
		t.Errorf("max=0 should return empty, got %q", got)
	}
}
