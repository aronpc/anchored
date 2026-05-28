package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/memory"
)

const curationLastRunKey = "last_run_at"

// runCurationWorker is a safe, opportunistic maintenance loop for MCP serve.
// It never deletes or rewrites memory content; it only refreshes metadata used
// by search, sync preview, and explicit curation clean commands.
func runCurationWorker(ctx context.Context, svc *memory.Service, cfg config.CurationConfig, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.IntervalHours <= 0 {
		cfg.IntervalHours = 24
	}
	if cfg.Threshold <= 0 {
		cfg.Threshold = memory.RemoteQualityThreshold
	}
	if cfg.MaxUpdates <= 0 {
		cfg.MaxUpdates = 500
	}

	interval := curationInterval(cfg)
	tryRun := func() {
		if !curationDue(ctx, svc.StoreDB(), interval, time.Now().UTC(), logger) {
			return
		}
		runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		updated, scanned, err := runCurationMetadataPass(runCtx, svc, cfg.Threshold, cfg.MaxUpdates)
		if err != nil {
			logger.Warn("curation worker failed", "error", err)
			return
		}
		if err := setCurationLastRun(ctx, svc.StoreDB(), time.Now().UTC()); err != nil {
			logger.Warn("curation worker state update failed", "error", err)
		}
		logger.Info("curation worker pass", "scanned", scanned, "updated", updated, "threshold", cfg.Threshold)
	}

	tryRun()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tryRun()
		}
	}
}

func curationInterval(cfg config.CurationConfig) time.Duration {
	if cfg.IntervalMinutes > 0 {
		return time.Duration(cfg.IntervalMinutes) * time.Minute
	}
	if cfg.IntervalHours <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(cfg.IntervalHours) * time.Hour
}

func curationDue(ctx context.Context, db *sql.DB, interval time.Duration, now time.Time, logger *slog.Logger) bool {
	last, ok, err := getCurationLastRun(ctx, db)
	if err != nil {
		if logger != nil {
			logger.Warn("curation worker state read failed", "error", err)
		}
		return false
	}
	if !ok {
		return true
	}
	return now.Sub(last) >= interval
}

func runCurationMetadataPass(ctx context.Context, svc *memory.Service, threshold float64, maxUpdates int) (updated int, scanned int, err error) {
	const pageSize = 1000
	for {
		page, err := listRecentCurationCandidates(ctx, svc.StoreDB(), pageSize)
		if err != nil {
			return updated, scanned, err
		}
		for _, m := range page {
			scanned++
			meta := memory.ParseMetadata(m.Metadata)
			score := memory.ScoreQuality(m.Content, m.Category, m.ProjectID != nil)
			changed := meta.QualityScore != score
			meta.QualityScore = score
			if meta.Importance == 0 || meta.Importance > score {
				meta.Importance = score
				changed = true
			}
			if score < threshold && !meta.Pinned && meta.CurationStatus != memory.CurationStatusLowSignal {
				meta.CurationStatus = memory.CurationStatusLowSignal
				changed = true
			}
			if score >= threshold && meta.CurationStatus == memory.CurationStatusLowSignal {
				meta.CurationStatus = ""
				changed = true
			}
			if !changed {
				continue
			}
			if err := svc.UpdateMetadata(ctx, m.ID, meta.ToAny()); err != nil {
				return updated, scanned, err
			}
			updated++
			if updated >= maxUpdates {
				return updated, scanned, nil
			}
		}
		if len(page) == 0 || len(page) < pageSize {
			return updated, scanned, nil
		}
	}
}

func listRecentCurationCandidates(ctx context.Context, db *sql.DB, limit int) ([]memory.Memory, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, project_id, category, content, metadata
		FROM memories
		WHERE deleted_at IS NULL
		  AND (
		    metadata IS NULL
		    OR json_extract(metadata, '$.quality_score') IS NULL
		    OR json_extract(metadata, '$.importance') IS NULL
		  )
		ORDER BY updated_at DESC, created_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []memory.Memory
	for rows.Next() {
		var m memory.Memory
		var projectID sql.NullString
		var metadataStr sql.NullString
		if err := rows.Scan(&m.ID, &projectID, &m.Category, &m.Content, &metadataStr); err != nil {
			return nil, err
		}
		if projectID.Valid {
			m.ProjectID = &projectID.String
		}
		if metadataStr.Valid {
			m.Metadata = map[string]any{}
			if err := json.Unmarshal([]byte(metadataStr.String), &m.Metadata); err != nil {
				m.Metadata = nil
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func getCurationLastRun(ctx context.Context, db *sql.DB) (time.Time, bool, error) {
	var raw string
	err := db.QueryRowContext(ctx, `SELECT value FROM curation_state WHERE key = ?`, curationLastRunKey).Scan(&raw)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false, nil
	}
	return t, true, nil
}

func setCurationLastRun(ctx context.Context, db *sql.DB, t time.Time) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO curation_state (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		curationLastRunKey, t.Format(time.RFC3339),
	)
	return err
}
