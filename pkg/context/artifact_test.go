package ctx

import (
	"context"
	"testing"
	"time"
)

// openArtifactTestDB returns a Store backed by an in-memory DB with all migrations applied.
func openArtifactTestDB(t *testing.T) *Store {
	t.Helper()
	// openStoreTestDB already applies MigrationSQL, MigrationSQL009, and MigrationSQL014.
	db := openStoreTestDB(t)
	s := NewStore(db, nil)
	if err := s.PrepareStatements(); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return s
}

func TestArtifact_AddAndSearch(t *testing.T) {
	s := openArtifactTestDB(t)
	ctx := context.Background()
	chunker := NewChunker(4096)

	in := ArtifactInput{
		ProjectID:   "proj-1",
		SessionID:   "sess-1",
		Type:        "prose",
		SourceTool:  "test-tool",
		SourceLabel: "test-label",
		Content:     "# Hello\n\nThis document describes the anchored artifact store for persistent memory.",
		TTLHours:    24,
	}

	id, err := s.AddArtifact(ctx, chunker, in, 336)
	if err != nil {
		t.Fatalf("AddArtifact: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty artifact ID")
	}

	results, err := s.SearchArtifacts(ctx, "anchored artifact", 10, "proj-1")
	if err != nil {
		t.Fatalf("SearchArtifacts: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one search result")
	}

	found := false
	for _, a := range results {
		if a.ID == id {
			found = true
			if a.Type != "prose" {
				t.Errorf("Type: got %q, want %q", a.Type, "prose")
			}
			if a.SourceTool != "test-tool" {
				t.Errorf("SourceTool: got %q, want %q", a.SourceTool, "test-tool")
			}
			if a.ContentHash != artifactContentHash(in.Content) {
				t.Errorf("ContentHash mismatch: got %q", a.ContentHash)
			}
		}
	}
	if !found {
		t.Errorf("artifact %q not found in search results", id)
	}
}

func TestArtifact_ContentHashDedup(t *testing.T) {
	s := openArtifactTestDB(t)
	ctx := context.Background()
	chunker := NewChunker(4096)

	in := ArtifactInput{
		ProjectID: "proj-dedup",
		Type:      "code",
		Content:   "package main\n\nfunc main() {}\n",
	}

	id1, err := s.AddArtifact(ctx, chunker, in, 336)
	if err != nil {
		t.Fatalf("first AddArtifact: %v", err)
	}

	id2, err := s.AddArtifact(ctx, chunker, in, 336)
	if err != nil {
		t.Fatalf("second AddArtifact: %v", err)
	}

	if id1 != id2 {
		t.Errorf("expected dedup to return the same ID: got %q and %q", id1, id2)
	}

	// Verify only one row was inserted.
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM artifacts WHERE project_id = 'proj-dedup'`,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 artifact row, got %d", count)
	}
}

func TestArtifact_TTLPrune(t *testing.T) {
	s := openArtifactTestDB(t)
	ctx := context.Background()
	chunker := NewChunker(4096)

	// Insert an artifact with a TTL already in the past.
	pastTime := time.Now().UTC().Add(-2 * time.Hour)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO artifacts (id, project_id, session_id, type, source_tool, source_label, content_hash, size_bytes, ttl_expires_at, created_at, metadata)
		VALUES ('expired-art', 'proj-ttl', '', 'prose', '', '', 'sha256:aaa', 10, ?, ?, '{}')`,
		pastTime, pastTime,
	)
	if err != nil {
		t.Fatalf("insert expired artifact: %v", err)
	}

	// Insert a chunk linked to the expired artifact.
	if err := s.InsertChunk(ctx, &Chunk{
		ID:          "chunk-for-expired",
		ProjectID:   "proj-ttl",
		Source:      "prose",
		Label:       "expired",
		Content:     "this content should be pruned",
		ContentType: "prose",
		IndexedAt:   pastTime,
		TTLHours:    0,
		ArtifactID:  "expired-art",
	}); err != nil {
		t.Fatalf("insert chunk: %v", err)
	}

	// Insert a fresh artifact with future TTL.
	freshIn := ArtifactInput{
		ProjectID: "proj-ttl",
		Type:      "prose",
		Content:   "this content should survive pruning",
		TTLHours:  999,
	}
	freshID, err := s.AddArtifact(ctx, chunker, freshIn, 336)
	if err != nil {
		t.Fatalf("AddArtifact fresh: %v", err)
	}

	// PruneExpired must remove the expired artifact and its chunk, leave the fresh one.
	pruned, err := s.PruneExpired(ctx)
	if err != nil {
		t.Fatalf("PruneExpired: %v", err)
	}
	if pruned != 1 {
		t.Errorf("expected 1 pruned artifact, got %d", pruned)
	}

	// Expired artifact gone.
	var count int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE id = 'expired-art'`).Scan(&count)
	if count != 0 {
		t.Error("expired artifact should have been deleted")
	}

	// Its chunk gone.
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM content_chunks WHERE id = 'chunk-for-expired'`).Scan(&count)
	if count != 0 {
		t.Error("chunk linked to expired artifact should have been deleted")
	}

	// Fresh artifact survives.
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE id = ?`, freshID).Scan(&count)
	if count != 1 {
		t.Error("fresh artifact should still exist")
	}
}

func TestArtifact_ListNewestFirst(t *testing.T) {
	s := openArtifactTestDB(t)
	ctx := context.Background()

	base := time.Now().UTC()

	for i, label := range []string{"first", "second", "third"} {
		delay := time.Duration(i) * time.Millisecond
		in := ArtifactInput{
			ProjectID:   "proj-list",
			Type:        "prose",
			SourceLabel: label,
			Content:     "content for " + label + " artifact with unique data " + label,
			TTLHours:    24,
		}
		// Insert directly so we control created_at ordering.
		hash := artifactContentHash(in.Content)
		id := newID()
		createdAt := base.Add(delay)
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO artifacts (id, project_id, session_id, type, source_tool, source_label, content_hash, size_bytes, ttl_expires_at, created_at, metadata)
			VALUES (?, ?, '', ?, '', ?, ?, ?, NULL, ?, '{}')`,
			id, in.ProjectID, in.Type, in.SourceLabel, hash, len(in.Content), createdAt,
		); err != nil {
			t.Fatalf("insert artifact %s: %v", label, err)
		}
		// Add chunks so it's a complete artifact.
		if err := s.InsertChunk(ctx, &Chunk{
			ProjectID:   in.ProjectID,
			Source:      in.Type,
			Label:       label,
			Content:     in.Content,
			ContentType: in.Type,
			IndexedAt:   createdAt,
			TTLHours:    in.TTLHours,
			ArtifactID:  id,
		}); err != nil {
			t.Fatalf("insert chunk for %s: %v", label, err)
		}
	}

	list, err := s.ListArtifacts(ctx, "proj-list", 10)
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 artifacts, got %d", len(list))
	}

	// Newest first: "third" > "second" > "first".
	if list[0].SourceLabel != "third" {
		t.Errorf("first result should be 'third', got %q", list[0].SourceLabel)
	}
	if list[2].SourceLabel != "first" {
		t.Errorf("last result should be 'first', got %q", list[2].SourceLabel)
	}
}
