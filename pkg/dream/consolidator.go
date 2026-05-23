package dream

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

type ConsolidationResult struct {
	Merged      int `json:"merged"`
	SoftDeleted int `json:"soft_deleted"`
	Flagged     int `json:"flagged"`
	Skipped     int `json:"skipped"`
}

type DreamConsolidator struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewConsolidator(db *sql.DB, logger *slog.Logger) *DreamConsolidator {
	if logger == nil {
		logger = slog.Default()
	}
	return &DreamConsolidator{db: db, logger: logger}
}

func (c *DreamConsolidator) Consolidate(ctx context.Context, report *DreamReport, cfg DreamConfig) (*ConsolidationResult, error) {
	result := &ConsolidationResult{}
	deletions := 0

	for _, action := range report.Actions {
		switch action.ActionType {
		case "dedup":
			if cfg.MaxDeletionsPerRun == 0 {
				result.Skipped++
				continue
			}
			if deletions >= cfg.MaxDeletionsPerRun {
				result.Skipped++
				continue
			}
			if action.Confidence < cfg.DedupThreshold {
				result.Skipped++
				continue
			}

			_, err := c.db.ExecContext(ctx,
				"UPDATE memories SET deleted_at = CURRENT_TIMESTAMP WHERE id = ? AND deleted_at IS NULL",
				action.MemoryID)
			if err != nil {
				c.logger.Warn("soft-delete failed", "id", action.MemoryID, "error", err)
				result.Skipped++
				continue
			}
			result.SoftDeleted++
			deletions++

		case "contradiction":
			result.Flagged++
			// Never auto-resolve contradictions

		default:
			result.Skipped++
		}
	}

	return result, nil
}

type ApplyActionResult struct {
	ActionID   string `json:"action_id"`
	ActionType string `json:"action_type"`
	MemoryID   string `json:"memory_id"`
	Status     string `json:"status"`
	Message    string `json:"message"`
}

func (c *DreamConsolidator) ApplyAction(ctx context.Context, actionID string) (*ApplyActionResult, error) {
	action, err := GetAction(ctx, c.db, actionID)
	if err != nil {
		return nil, fmt.Errorf("lookup action: %w", err)
	}
	if action == nil {
		return nil, fmt.Errorf("action %q not found", actionID)
	}
	if action.Status != "proposed" {
		return nil, fmt.Errorf("action %q has status %q, cannot apply (only \"proposed\" actions are eligible)", actionID, action.Status)
	}

	switch action.ActionType {
	case "dedup":
		_, err := c.db.ExecContext(ctx,
			"UPDATE memories SET deleted_at = CURRENT_TIMESTAMP WHERE id = ? AND deleted_at IS NULL",
			action.MemoryID)
		if err != nil {
			return nil, fmt.Errorf("soft-delete memory %q: %w", action.MemoryID, err)
		}

		_, err = c.db.ExecContext(ctx,
			"UPDATE dream_actions SET status = 'applied', applied_at = CURRENT_TIMESTAMP WHERE id = ?",
			actionID)
		if err != nil {
			return nil, fmt.Errorf("update action status: %w", err)
		}

		return &ApplyActionResult{
			ActionID:   actionID,
			ActionType: action.ActionType,
			MemoryID:   action.MemoryID,
			Status:     "applied",
			Message:    fmt.Sprintf("soft-deleted memory %q (dedup, confidence=%.2f)", action.MemoryID, action.Confidence),
		}, nil

	case "contradiction":
		return nil, fmt.Errorf("contradiction actions require manual review and cannot be auto-applied")

	default:
		return nil, fmt.Errorf("unknown action type %q", action.ActionType)
	}
}
