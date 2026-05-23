package dream

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// DreamActionRecord represents a dream_action row from the database.
type DreamActionRecord struct {
	ID              string     `json:"id"`
	RunID           string     `json:"run_id"`
	MemoryID        string     `json:"memory_id"`
	RelatedMemoryID string     `json:"related_memory_id,omitempty"`
	ActionType      string     `json:"action_type"`
	Confidence      float64    `json:"confidence"`
	Reason          string     `json:"reason"`
	ProposedAt      time.Time  `json:"proposed_at"`
	AppliedAt       *time.Time `json:"applied_at,omitempty"`
	Status          string     `json:"status"`
}

// GetAction retrieves a single dream action by ID.
// Returns nil if not found.
func GetAction(ctx context.Context, db *sql.DB, actionID string) (*DreamActionRecord, error) {
	row := db.QueryRowContext(ctx,
		"SELECT id, run_id, memory_id, related_memory_id, action_type, confidence, reason, proposed_at, applied_at, status FROM dream_actions WHERE id = ?",
		actionID)

	var a DreamActionRecord
	err := row.Scan(&a.ID, &a.RunID, &a.MemoryID, &a.RelatedMemoryID, &a.ActionType, &a.Confidence, &a.Reason, &a.ProposedAt, &a.AppliedAt, &a.Status)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query action %q: %w", actionID, err)
	}
	return &a, nil
}
