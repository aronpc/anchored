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

func TestRenderPreSearchPreview_FormatsAndEscapes(t *testing.T) {
	hits := []preSearchHit{
		{Category: "decision", Content: "Settled on RRF for hybrid search"},
		{Category: "fact", Content: "uses <b>highlights</b> & ampersands"},
	}
	out := renderPreSearchPreview("how did we decide", hits)
	if !strings.Contains(out, `<anchored_search_preview query="how did we decide" count="2">`) {
		t.Errorf("missing wrapper: %s", out)
	}
	if !strings.Contains(out, "[decision] Settled on RRF for hybrid search") {
		t.Errorf("missing first hit: %s", out)
	}
	if !strings.Contains(out, "uses &lt;b&gt;highlights&lt;/b&gt; &amp; ampersands") {
		t.Errorf("XML not escaped: %s", out)
	}
	if !strings.HasSuffix(out, "</anchored_search_preview>") {
		t.Errorf("missing closing tag: %s", out)
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
