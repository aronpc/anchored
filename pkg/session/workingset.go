package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// workingSetMaxItems caps each list in a working set. The set is a rolling
// window of the session's current focus, most-recent-first; older entries fall
// off so a long session doesn't accumulate an unbounded, stale boost surface.
const workingSetMaxItems = 50

// WorkingSet is the per-session record of what is actively being worked on:
// touched files, referenced symbols/entities, run commands/tests, observed
// errors, and the memory/artifact ids surfaced during the session. Retrieval
// uses the files/symbols/entities to boost overlapping memories.
type WorkingSet struct {
	SessionID   string    `json:"session_id"`
	ProjectID   string    `json:"project_id,omitempty"`
	Files       []string  `json:"files,omitempty"`
	Symbols     []string  `json:"symbols,omitempty"`
	Entities    []string  `json:"entities,omitempty"`
	Commands    []string  `json:"commands,omitempty"`
	Tests       []string  `json:"tests,omitempty"`
	Errors      []string  `json:"errors,omitempty"`
	MemoryIDs   []string  `json:"memory_ids,omitempty"`
	ArtifactIDs []string  `json:"artifact_ids,omitempty"`
	TopicKey    string    `json:"topic_key,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Empty reports whether the working set carries no focus signals.
func (w *WorkingSet) Empty() bool {
	if w == nil {
		return true
	}
	return len(w.Files)+len(w.Symbols)+len(w.Entities)+len(w.Commands)+
		len(w.Tests)+len(w.Errors) == 0
}

// WorkingSetDelta is an incremental update merged into the stored set. Empty
// slices are ignored. When TopicKey is set and differs from the stored key (or
// ProjectID changes), the existing set is reset before the delta is applied —
// this is the topic-shift reset that prevents a stale focus from biasing a new
// line of work.
type WorkingSetDelta struct {
	ProjectID   string
	Files       []string
	Symbols     []string
	Entities    []string
	Commands    []string
	Tests       []string
	Errors      []string
	MemoryIDs   []string
	ArtifactIDs []string
	TopicKey    string
}

// GetWorkingSet returns the working set for a session, or nil when none exists.
func (m *Manager) GetWorkingSet(ctx context.Context, sessionID string) (*WorkingSet, error) {
	if sessionID == "" {
		return nil, nil
	}
	row := m.db.QueryRowContext(ctx,
		`SELECT session_id, project_id, files, symbols, entities, commands, tests, errors, memory_ids, artifact_ids, topic_key, updated_at
		 FROM working_sets WHERE session_id = ?`, sessionID)

	var ws WorkingSet
	var files, symbols, entities, commands, tests, errs, memIDs, artIDs string
	var updatedAt sql.NullTime
	err := row.Scan(&ws.SessionID, &ws.ProjectID, &files, &symbols, &entities,
		&commands, &tests, &errs, &memIDs, &artIDs, &ws.TopicKey, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get working set: %w", err)
	}
	ws.Files = decodeJSONList(files)
	ws.Symbols = decodeJSONList(symbols)
	ws.Entities = decodeJSONList(entities)
	ws.Commands = decodeJSONList(commands)
	ws.Tests = decodeJSONList(tests)
	ws.Errors = decodeJSONList(errs)
	ws.MemoryIDs = decodeJSONList(memIDs)
	ws.ArtifactIDs = decodeJSONList(artIDs)
	if updatedAt.Valid {
		ws.UpdatedAt = updatedAt.Time
	}
	return &ws, nil
}

// UpdateWorkingSet merges delta into the session's working set (creating it on
// first call) and returns the resulting set. A topic shift (changed TopicKey or
// ProjectID) resets the prior focus before applying the delta.
func (m *Manager) UpdateWorkingSet(ctx context.Context, sessionID string, delta WorkingSetDelta) (*WorkingSet, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session id required")
	}
	cur, err := m.GetWorkingSet(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		cur = &WorkingSet{SessionID: sessionID}
	}

	// Topic-shift reset: a new project or a new explicit topic key means the
	// prior focus is no longer relevant — start fresh so its files/symbols stop
	// boosting unrelated retrieval.
	shifted := (delta.ProjectID != "" && cur.ProjectID != "" && delta.ProjectID != cur.ProjectID) ||
		(delta.TopicKey != "" && cur.TopicKey != "" && delta.TopicKey != cur.TopicKey)
	if shifted {
		cur = &WorkingSet{SessionID: sessionID}
	}

	if delta.ProjectID != "" {
		cur.ProjectID = delta.ProjectID
	}
	if delta.TopicKey != "" {
		cur.TopicKey = delta.TopicKey
	}
	cur.Files = mergeList(cur.Files, delta.Files)
	cur.Symbols = mergeList(cur.Symbols, delta.Symbols)
	cur.Entities = mergeList(cur.Entities, delta.Entities)
	cur.Commands = mergeList(cur.Commands, delta.Commands)
	cur.Tests = mergeList(cur.Tests, delta.Tests)
	cur.Errors = mergeList(cur.Errors, delta.Errors)
	cur.MemoryIDs = mergeList(cur.MemoryIDs, delta.MemoryIDs)
	cur.ArtifactIDs = mergeList(cur.ArtifactIDs, delta.ArtifactIDs)

	if _, err := m.db.ExecContext(ctx,
		`INSERT INTO working_sets
		   (session_id, project_id, files, symbols, entities, commands, tests, errors, memory_ids, artifact_ids, topic_key, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		 ON CONFLICT(session_id) DO UPDATE SET
		   project_id=excluded.project_id, files=excluded.files, symbols=excluded.symbols,
		   entities=excluded.entities, commands=excluded.commands, tests=excluded.tests,
		   errors=excluded.errors, memory_ids=excluded.memory_ids, artifact_ids=excluded.artifact_ids,
		   topic_key=excluded.topic_key, updated_at=excluded.updated_at`,
		sessionID, cur.ProjectID,
		encodeJSONList(cur.Files), encodeJSONList(cur.Symbols), encodeJSONList(cur.Entities),
		encodeJSONList(cur.Commands), encodeJSONList(cur.Tests), encodeJSONList(cur.Errors),
		encodeJSONList(cur.MemoryIDs), encodeJSONList(cur.ArtifactIDs), cur.TopicKey,
	); err != nil {
		return nil, fmt.Errorf("upsert working set: %w", err)
	}
	cur.UpdatedAt = time.Now()
	return cur, nil
}

// mergeList prepends additions (most-recent-first), de-duplicates
// case-insensitively while preserving the first-seen original casing, and caps
// the result at workingSetMaxItems.
func mergeList(existing, additions []string) []string {
	out := make([]string, 0, len(existing)+len(additions))
	seen := make(map[string]bool)
	add := func(items []string) {
		for _, it := range items {
			it = strings.TrimSpace(it)
			if it == "" {
				continue
			}
			key := strings.ToLower(it)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, it)
		}
	}
	add(additions) // newest focus first
	add(existing)
	if len(out) > workingSetMaxItems {
		out = out[:workingSetMaxItems]
	}
	return out
}

func decodeJSONList(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func encodeJSONList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	b, err := json.Marshal(items)
	if err != nil {
		return "[]"
	}
	return string(b)
}
