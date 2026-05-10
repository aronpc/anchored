package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/debuglog"
	"github.com/jholhewres/anchored/pkg/importer"
	"github.com/jholhewres/anchored/pkg/memory"
	"github.com/jholhewres/anchored/pkg/project"

	_ "github.com/mattn/go-sqlite3"
)

// HookContext is the lightweight runtime hooks need: just the DB and a
// project detector. memory.NewService loads the ONNX embedder (~500ms cold
// start, ~470MB memory map) which the hooks don't use — every PostToolUse
// firing was paying that cost. This bypass keeps hooks under the latency
// floor where they don't bottleneck a busy tool-call session.
type HookContext struct {
	cfg      *config.Config
	db       *sql.DB
	detector *project.Detector
}

// openHookContext opens the SQLite DB with WAL+busy_timeout and wires a
// project detector against it. Caller must Close() when done.
func openHookContext(configPath string) (*HookContext, error) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if err := config.EnsureDirs(cfg); err != nil {
		return nil, fmt.Errorf("ensure dirs: %w", err)
	}

	dsn := cfg.Memory.DatabasePath + "?_journal_mode=WAL&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// Single connection avoids "database is locked" under WAL when migrations
	// are in flight from another `anchored` invocation. Hooks are short-lived
	// so the cap is harmless.
	db.SetMaxOpenConns(1)

	return &HookContext{
		cfg:      cfg,
		db:       db,
		detector: project.NewDetector(db),
	}, nil
}

func (h *HookContext) Close() error {
	if h == nil || h.db == nil {
		return nil
	}
	return h.db.Close()
}

// ResolveProject returns the project ID for cwd, or "" when cwd is outside a
// git repo or the projects table is missing. Mirrors memory.Service.ResolveProject.
func (h *HookContext) ResolveProject(cwd string) string {
	if h == nil || h.detector == nil {
		return ""
	}
	p, err := h.detector.Detect(cwd)
	if err != nil || p == nil {
		return ""
	}
	return p.ID
}

// openDebugLogger resolves config + env to (maybe) open the NDJSON debug
// log. Always non-nil and always safe to call Event/Close on, even when
// disabled.
func openDebugLogger(configPath string) *debuglog.Logger {
	cfg, err := loadConfig(configPath)
	if err != nil {
		// Fall through with a zero config so env overrides still work and we
		// don't kill the hook over a YAML typo.
		cfg = config.Defaults()
	}
	return debuglog.Open(cfg)
}

func newFlagSet(name string) *flag.FlagSet {
	return flag.NewFlagSet(name, flag.ExitOnError)
}

func initService(configPath string) (*config.Config, *slog.Logger, *memory.Service, error) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load config: %w", err)
	}

	if err := config.EnsureDirs(cfg); err != nil {
		return nil, nil, nil, fmt.Errorf("ensure dirs: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	svc, err := memory.NewService(cfg, logger)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("init service: %w", err)
	}

	return cfg, logger, svc, nil
}

type serviceStoreAdapter struct {
	svc *memory.Service
}

func (a *serviceStoreAdapter) SaveRaw(ctx context.Context, content, category, source string, cwd string) error {
	return a.svc.SaveRawNoEmbed(ctx, content, category, source, cwd)
}

func (a *serviceStoreAdapter) SaveRawWithSource(ctx context.Context, content, category, source string, sourceID *string, cwd string) error {
	_, err := a.svc.SaveWithOptions(ctx, memory.SaveOptions{
		Content:   content,
		Category:  category,
		Source:    source,
		SourceID:  sourceID,
		CWD:       cwd,
		SkipEmbed: true,
	})
	return err
}

func (a *serviceStoreAdapter) CreateImport(id, source, path string) error {
	_, err := a.svc.StoreDB().Exec(
		`INSERT INTO imports (id, source, path, status, started_at) VALUES (?, ?, ?, 'running', CURRENT_TIMESTAMP)`,
		id, source, path,
	)
	return err
}

func (a *serviceStoreAdapter) UpdateImport(id, status string, memoriesImported int, errMsg string) error {
	_, err := a.svc.StoreDB().Exec(
		`UPDATE imports SET status = ?, memories_imported = ?, finished_at = CURRENT_TIMESTAMP, error = ? WHERE id = ?`,
		status, memoriesImported, errMsg, id,
	)
	return err
}

func (a *serviceStoreAdapter) GetLastImport(source string) (*importer.ImportRecordInfo, error) {
	row := a.svc.StoreDB().QueryRow(
		`SELECT source, path, status, finished_at FROM imports WHERE source = ? ORDER BY started_at DESC LIMIT 1`, source,
	)
	var r importer.ImportRecordInfo
	var finishedAt sql.NullTime
	err := row.Scan(&r.Source, &r.Path, &r.Status, &finishedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if finishedAt.Valid {
		r.FinishedAt = &finishedAt.Time
	}
	return &r, nil
}
