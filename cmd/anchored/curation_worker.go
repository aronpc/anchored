package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/memory"
)

const (
	curationLastRunKey = "last_run_at"
	// curationReconciledVersionKey records the scorer version the corpus was
	// last fully reconciled at. When it lags QualityScorerVersion (e.g. right
	// after an upgrade), the worker performs a one-time full backlog drain on
	// startup so the existing memories are repaired automatically — no manual
	// `curation reconcile` needed.
	curationReconciledVersionKey = "reconciled_version"
	// curationBootstrapMax is an effectively-unlimited per-pass cap used only by
	// the startup backlog drain. The metadata pass is pure SQL UPDATEs (no
	// embeddings), so draining the whole corpus once is cheap.
	curationBootstrapMax = 1 << 30
)

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

	// One-time backlog drain after an upgrade: repair the existing corpus
	// (stale low_signal flags, unscored memories) in a single pass instead of
	// trickling through it at max_updates_per_run. Guarded by reconciled_version
	// so it only runs once per scorer bump, even though serve starts per session.
	curationBootstrap(ctx, svc, cfg.Threshold, logger)

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

// curationBootstrap drains the full backlog of stale/unscored memories once,
// the first time serve runs at a new scorer version. It is safe to call on
// every startup: when reconciled_version already matches the current scorer it
// returns immediately. Errors are logged but never fatal — the incremental
// ticker still runs and will eventually catch up.
func curationBootstrap(ctx context.Context, svc *memory.Service, threshold float64, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	done, err := getCurationReconciledVersion(ctx, svc.StoreDB())
	if err != nil {
		logger.Warn("curation bootstrap state read failed", "error", err)
		return
	}
	if done >= memory.QualityScorerVersion {
		return
	}

	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	start := time.Now()
	updated, scanned, err := runCurationMetadataPass(runCtx, svc, threshold, curationBootstrapMax)
	if err != nil {
		// Partial progress is fine; do NOT record the version so the next
		// startup retries the remaining backlog.
		logger.Warn("curation bootstrap incomplete", "error", err, "updated", updated, "scanned", scanned)
		return
	}
	if err := setCurationReconciledVersion(ctx, svc.StoreDB(), memory.QualityScorerVersion); err != nil {
		logger.Warn("curation bootstrap version write failed", "error", err)
	}
	logger.Info("curation bootstrap complete",
		"scorer_version", memory.QualityScorerVersion, "scanned", scanned, "updated", updated,
		"duration", time.Since(start).Round(time.Millisecond))
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
			meta, changed := memory.RecurateMetadata(meta, m.Content, m.Category, m.ProjectID != nil, threshold)
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
		    OR json_extract(metadata, '$.scorer_version') IS NULL
		    OR json_extract(metadata, '$.scorer_version') < ?
		  )
		ORDER BY updated_at DESC, created_at DESC
		LIMIT ?`, memory.QualityScorerVersion, limit)
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

func getCurationReconciledVersion(ctx context.Context, db *sql.DB) (int, error) {
	var raw string
	err := db.QueryRowContext(ctx, `SELECT value FROM curation_state WHERE key = ?`, curationReconciledVersionKey).Scan(&raw)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	v, convErr := strconv.Atoi(strings.TrimSpace(raw))
	if convErr != nil {
		return 0, nil
	}
	return v, nil
}

func setCurationReconciledVersion(ctx context.Context, db *sql.DB, version int) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO curation_state (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		curationReconciledVersionKey, strconv.Itoa(version),
	)
	return err
}

func setCurationLastRun(ctx context.Context, db *sql.DB, t time.Time) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO curation_state (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		curationLastRunKey, t.Format(time.RFC3339),
	)
	return err
}
