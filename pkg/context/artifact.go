package ctx

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Artifact is a stored artifact record.
type Artifact struct {
	ID           string            `json:"id"`
	ProjectID    string            `json:"project_id"`
	SessionID    string            `json:"session_id"`
	Type         string            `json:"type"`
	SourceTool   string            `json:"source_tool"`
	SourceLabel  string            `json:"source_label"`
	ContentHash  string            `json:"content_hash"`
	SizeBytes    int               `json:"size_bytes"`
	TTLExpiresAt *time.Time        `json:"ttl_expires_at,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	Metadata     map[string]any    `json:"metadata"`
}

// ArtifactInput holds the parameters for AddArtifact.
type ArtifactInput struct {
	ProjectID   string
	SessionID   string
	Type        string
	SourceTool  string
	SourceLabel string
	Content     string
	TTLHours    int
	Metadata    map[string]any
}

// AddArtifact inserts an artifact (deduplicating by content hash), chunks the
// content, and indexes each chunk tagged with the artifact ID so FTS search
// can find it.  Returns the artifact ID (existing or newly created).
func (s *Store) AddArtifact(ctx context.Context, chunker *Chunker, in ArtifactInput, defaultTTL int) (string, error) {
	hash := "sha256:" + sha256Hex(in.Content)

	// Dedup: if an artifact with this hash already exists, return its ID.
	var existingID string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM artifacts WHERE content_hash = ? LIMIT 1`, hash,
	).Scan(&existingID)
	if err != nil && err != sql.ErrNoRows {
		return "", fmt.Errorf("dedup check: %w", err)
	}
	if existingID != "" {
		return existingID, nil
	}

	id := newID()
	now := time.Now().UTC()

	var ttlExpiresAt *time.Time
	if in.TTLHours > 0 {
		t := now.Add(time.Duration(in.TTLHours) * time.Hour)
		ttlExpiresAt = &t
	}

	// nil metadata is the valid default ({}); a marshal failure on non-nil
	// metadata is a real error and must not be silently swallowed as {}.
	metaBytes := []byte("{}")
	if in.Metadata != nil {
		metaBytes, err = json.Marshal(in.Metadata)
		if err != nil {
			return "", fmt.Errorf("marshal artifact metadata: %w", err)
		}
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO artifacts (id, project_id, session_id, type, source_tool, source_label, content_hash, size_bytes, ttl_expires_at, created_at, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.ProjectID, in.SessionID, in.Type, in.SourceTool, in.SourceLabel,
		hash, len(in.Content), ttlExpiresAt, now, string(metaBytes),
	)
	if err != nil {
		return "", fmt.Errorf("insert artifact: %w", err)
	}

	// Chunk and index the content so it is searchable via FTS.
	ttlHours := in.TTLHours
	if ttlHours <= 0 {
		ttlHours = defaultTTL
	}
	if ttlHours <= 0 {
		ttlHours = 336
	}

	source := in.SourceTool
	if source == "" {
		source = in.Type
	}
	label := in.SourceLabel
	if label == "" {
		label = in.Type
	}

	chunks := chunker.Chunk([]byte(in.Content))
	for _, cd := range chunks {
		if cd.Content == "" {
			continue
		}
		chunkLabel := label
		if cd.Heading != "" {
			chunkLabel = cd.Heading
		}
		chunk := &Chunk{
			SessionID:   in.SessionID,
			ProjectID:   in.ProjectID,
			Source:      source,
			Label:       chunkLabel,
			Content:     cd.Content,
			Metadata:    "{}",
			ContentType: in.Type,
			IndexedAt:   now,
			TTLHours:    ttlHours,
			ArtifactID:  id,
		}
		if err := s.InsertChunk(ctx, chunk); err != nil {
			return "", fmt.Errorf("insert artifact chunk: %w", err)
		}
	}

	// If the content produced no markdown chunks, fall back to a single raw chunk.
	if len(chunks) == 0 && in.Content != "" {
		chunk := &Chunk{
			SessionID:   in.SessionID,
			ProjectID:   in.ProjectID,
			Source:      source,
			Label:       label,
			Content:     in.Content,
			Metadata:    "{}",
			ContentType: in.Type,
			IndexedAt:   now,
			TTLHours:    ttlHours,
			ArtifactID:  id,
		}
		if err := s.InsertChunk(ctx, chunk); err != nil {
			return "", fmt.Errorf("insert artifact chunk (raw): %w", err)
		}
	}

	return id, nil
}

// SearchArtifacts performs FTS over artifact chunks, then maps chunk→artifact,
// deduplicating by artifact ID. Returns artifacts ordered by first-match score.
func (s *Store) SearchArtifacts(ctx context.Context, query string, maxResults int, projectID string) ([]Artifact, error) {
	if maxResults <= 0 {
		maxResults = 20
	}

	// Search chunks with the artifact content_type filter absent (all types).
	// We over-fetch to account for dedup collapse.
	chunkResults, err := s.SearchChunks(ctx, query, maxResults*5, "", "", projectID)
	if err != nil {
		return nil, fmt.Errorf("search chunks for artifacts: %w", err)
	}

	// Collect artifact IDs from chunks that have one, preserving order.
	seen := make(map[string]bool)
	var artifactIDs []string

	// First pass: gather artifact IDs from chunk results (need to look them up).
	// We resolve artifact_id per chunk via a direct query.
	for _, cr := range chunkResults {
		var artifactID string
		err := s.db.QueryRowContext(ctx,
			`SELECT artifact_id FROM content_chunks WHERE id = ?`, cr.ChunkID,
		).Scan(&artifactID)
		if err != nil || artifactID == "" {
			continue
		}
		if !seen[artifactID] {
			seen[artifactID] = true
			artifactIDs = append(artifactIDs, artifactID)
		}
		if len(artifactIDs) >= maxResults {
			break
		}
	}

	if len(artifactIDs) == 0 {
		return nil, nil
	}

	artifacts := make([]Artifact, 0, len(artifactIDs))
	for _, aid := range artifactIDs {
		a, err := s.getArtifactByID(ctx, aid)
		if err != nil || a == nil {
			continue
		}
		artifacts = append(artifacts, *a)
	}
	return artifacts, nil
}

// ListArtifacts returns up to limit artifacts for a project, newest first.
func (s *Store) ListArtifacts(ctx context.Context, projectID string, limit int) ([]Artifact, error) {
	if limit <= 0 {
		limit = 20
	}

	var rows *sql.Rows
	var err error
	if projectID != "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, project_id, session_id, type, source_tool, source_label, content_hash, size_bytes, ttl_expires_at, created_at, metadata
			 FROM artifacts WHERE project_id = ? ORDER BY created_at DESC LIMIT ?`,
			projectID, limit,
		)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, project_id, session_id, type, source_tool, source_label, content_hash, size_bytes, ttl_expires_at, created_at, metadata
			 FROM artifacts ORDER BY created_at DESC LIMIT ?`,
			limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("list artifacts: %w", err)
	}
	defer rows.Close()

	var artifacts []Artifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, a)
	}
	return artifacts, rows.Err()
}

// PruneExpired deletes artifacts whose ttl_expires_at has passed and their
// associated chunks. Returns the count of artifacts deleted.
func (s *Store) PruneExpired(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM artifacts WHERE ttl_expires_at IS NOT NULL AND ttl_expires_at <= datetime('now')`,
	)
	if err != nil {
		return 0, fmt.Errorf("query expired artifacts: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan expired artifact id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close rows: %w", err)
	}

	if len(ids) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM content_chunks WHERE artifact_id = ?`, id); err != nil {
			return 0, fmt.Errorf("delete artifact chunks %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM artifacts WHERE id = ?`, id); err != nil {
			return 0, fmt.Errorf("delete artifact %s: %w", id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit prune: %w", err)
	}
	return len(ids), nil
}

// getArtifactByID fetches a single artifact row by ID.
func (s *Store) getArtifactByID(ctx context.Context, id string) (*Artifact, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, session_id, type, source_tool, source_label, content_hash, size_bytes, ttl_expires_at, created_at, metadata
		 FROM artifacts WHERE id = ?`, id,
	)
	var a Artifact
	var ttlExp sql.NullTime
	var metaStr string
	err := row.Scan(
		&a.ID, &a.ProjectID, &a.SessionID, &a.Type, &a.SourceTool, &a.SourceLabel,
		&a.ContentHash, &a.SizeBytes, &ttlExp, &a.CreatedAt, &metaStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get artifact %s: %w", id, err)
	}
	if ttlExp.Valid {
		a.TTLExpiresAt = &ttlExp.Time
	}
	if err := json.Unmarshal([]byte(metaStr), &a.Metadata); err != nil {
		a.Metadata = map[string]any{}
	}
	return &a, nil
}

// scanArtifact scans one artifact row from a *sql.Rows.
func scanArtifact(rows *sql.Rows) (Artifact, error) {
	var a Artifact
	var ttlExp sql.NullTime
	var metaStr string
	if err := rows.Scan(
		&a.ID, &a.ProjectID, &a.SessionID, &a.Type, &a.SourceTool, &a.SourceLabel,
		&a.ContentHash, &a.SizeBytes, &ttlExp, &a.CreatedAt, &metaStr,
	); err != nil {
		return a, fmt.Errorf("scan artifact: %w", err)
	}
	if ttlExp.Valid {
		a.TTLExpiresAt = &ttlExp.Time
	}
	if err := json.Unmarshal([]byte(metaStr), &a.Metadata); err != nil {
		a.Metadata = map[string]any{}
	}
	return a, nil
}

// artifactContentHash computes the sha256 hash string used for dedup.
// Exported for use in tests.
func artifactContentHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(h[:])
}
