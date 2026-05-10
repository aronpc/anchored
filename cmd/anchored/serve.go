package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/debuglog"
	"github.com/jholhewres/anchored/pkg/kg"
	"github.com/jholhewres/anchored/pkg/memory"
	"github.com/jholhewres/anchored/pkg/mcp"
	"github.com/jholhewres/anchored/pkg/session"
	"github.com/jholhewres/anchored/pkg/updater"
)

func runServe() {
	logger := slog.Default()

	cfg, err := loadConfig("")
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if err := config.EnsureDirs(cfg); err != nil {
		slog.Error("failed to create directories", "error", err)
		os.Exit(1)
	}

	memSvc, err := memory.NewService(cfg, logger)
	if err != nil {
		slog.Error("failed to initialize memory service", "error", err)
		os.Exit(1)
	}
	defer memSvc.Close()

	indexer := memory.NewMemoryIndexer(memSvc, cfg.Indexer.Paths, logger)
	if cfg.Indexer.Interval != "" {
		if d, err := time.ParseDuration(cfg.Indexer.Interval); err == nil {
			indexer.SetInterval(d)
		}
	}
	if cfg.Indexer.Enabled {
		indexer.Start()
		defer indexer.Stop()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go updater.Run(ctx, updater.Options{
		CurrentVersion: Version,
		Logger:         logger,
	})

	// session_events grows by ~1 row per tool call (PostToolUse hook). Without
	// retention the table balloons in long-lived projects. We sweep on serve
	// startup and once a day after that; default 30-day window keeps recap
	// queries fast while preserving a useful historical horizon.
	go runEventCleanup(ctx, memSvc, logger, 30*24*time.Hour, 24*time.Hour)

	if err := serveSTDIO(ctx, memSvc, cfg, logger); err != nil {
		slog.Error("serve error", "error", err)
		os.Exit(1)
	}
}

func serveSTDIO(ctx context.Context, memSvc *memory.Service, cfg *config.Config, logFn *slog.Logger) error {
	kgSvc := kg.New(memSvc.StoreDB(), logFn)
	memSvc.SetKGExtractor(kg.NewPatternExtractor(kgSvc, logFn))

	sessionMgr := session.NewManager(memSvc.StoreDB(), logFn)

	var optimizer mcp.OptimizerFacade
	if cfg.ContextOptimizer.Enabled {
		opt, err := mcp.NewCtxOptimizer(memSvc.StoreDB(), cfg.ContextOptimizer, logFn)
		if err != nil {
			logFn.Warn("context optimizer init failed, ctx_* tools unavailable", "error", err)
		} else {
			optimizer = opt
		}
	}
	if optimizer != nil {
		defer optimizer.Close()
	}

	server := mcp.NewServer(memSvc, kgSvc, sessionMgr, optimizer, Version, logFn)

	// Optional NDJSON event log for post-mortem analysis. No-op when
	// debug.enabled is false (the default) and ANCHORED_DEBUG isn't set.
	dlog := debuglog.Open(cfg)
	defer dlog.Close()
	if dlog.Enabled() {
		logFn.Info("anchored debug log enabled", "path", dlog.Path())
		dlog.Event("server.start", map[string]any{"version": Version})
	}
	server.SetDebugLogger(dlog)

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		response := server.HandleMessage(ctx, line)
		if response == nil {
			continue
		}

		fmt.Printf("%s\n", response)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("stdin read: %w", err)
	}

	if sessionMgr != nil {
		ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel2()
		sessionMgr.EndStaleSessions(ctx2, 30*time.Minute)
	}

	return nil
}

// runEventCleanup periodically deletes session_events rows older than
// retention. Runs once on startup, then every interval. Cancellation via ctx
// stops cleanly. Errors are logged at Warn but never block — the goroutine
// keeps trying on the next tick.
func runEventCleanup(ctx context.Context, memSvc *memory.Service, logger *slog.Logger, retention, interval time.Duration) {
	mgr := session.NewManager(memSvc.StoreDB(), logger)
	sweep := func() {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		n, err := mgr.CleanupOldEvents(c, retention)
		if err != nil {
			logger.Warn("session_events cleanup failed", "error", err)
			return
		}
		if n > 0 {
			logger.Info("session_events cleanup", "deleted", n, "retention_hours", int(retention.Hours()))
		}
	}
	sweep()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

func loadConfig(explicit string) (*config.Config, error) {
	if explicit != "" {
		return config.Load(explicit)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return config.Defaults(), nil
	}

	return config.Load(home + "/.anchored/config.yaml")
}
