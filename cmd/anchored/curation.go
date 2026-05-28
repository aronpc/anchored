package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jholhewres/anchored/pkg/memory"
)

func runCuration(args []string) {
	if len(args) == 0 {
		printCurationUsage()
		os.Exit(1)
	}
	switch args[0] {
	case "status":
		runCurationStatus(args[1:])
	case "enable":
		runCurationSetEnabled(true)
	case "disable":
		runCurationSetEnabled(false)
	case "score":
		runCurationScore(args)
	case "reconcile":
		runCurationReconcile(args[1:])
	case "clean":
		runCurationClean(args[1:])
	case "restore":
		runCurationRestore(args[1:])
	default:
		printCurationUsage()
		os.Exit(1)
	}
}

func printCurationUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  anchored curation status")
	fmt.Fprintln(os.Stderr, "  anchored curation enable")
	fmt.Fprintln(os.Stderr, "  anchored curation disable")
	fmt.Fprintln(os.Stderr, "  anchored curation score     [--apply] [--threshold 0.55] [--limit 20]")
	fmt.Fprintln(os.Stderr, "  anchored curation reconcile [--threshold 0.55] [--category fact] [--yes]")
	fmt.Fprintln(os.Stderr, "  anchored curation clean   [--hard] [--threshold 0.55] [--dry-run] [--yes]")
	fmt.Fprintln(os.Stderr, "  anchored curation restore [--from PATH] [--latest] [--yes]")
}

func runCurationStatus(args []string) {
	fs := newFlagSet("curation status")
	configPath := fs.String("config", "", "path to config file")
	fs.Parse(args)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	var last time.Time
	var ok bool
	reconciledVersion := -1
	pending := -1
	if _, statErr := os.Stat(cfg.Memory.DatabasePath); statErr == nil {
		db, err := sql.Open("sqlite3", cfg.Memory.DatabasePath+"?_journal_mode=WAL&_busy_timeout=5000")
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to open database: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()
		ctx := context.Background()
		last, ok, err = getCurationLastRun(ctx, db)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to read curation state: %v\n", err)
			os.Exit(1)
		}
		if v, vErr := getCurationReconciledVersion(ctx, db); vErr == nil {
			reconciledVersion = v
		}
		if c, cErr := countCurationPending(ctx, db); cErr == nil {
			pending = c
		}
	}

	interval := curationInterval(cfg.Curation)
	threshold := cfg.Curation.Threshold
	if threshold <= 0 {
		threshold = memory.RemoteQualityThreshold
	}
	maxUpdates := cfg.Curation.MaxUpdates
	if maxUpdates <= 0 {
		maxUpdates = 500
	}

	fmt.Printf("Curation worker: %s\n", enabledLabel(cfg.Curation.Enabled))
	fmt.Printf("Interval: %s\n", interval)
	fmt.Printf("Threshold: %.2f\n", threshold)
	fmt.Printf("Max updates/run: %d\n", maxUpdates)
	fmt.Printf("Scorer version: %d\n", memory.QualityScorerVersion)
	switch {
	case reconciledVersion < 0:
		fmt.Println("Corpus reconciled: unknown")
	case reconciledVersion >= memory.QualityScorerVersion:
		fmt.Printf("Corpus reconciled: yes (v%d)\n", reconciledVersion)
	default:
		fmt.Printf("Corpus reconciled: pending (at v%d, current v%d — runs on next serve)\n",
			reconciledVersion, memory.QualityScorerVersion)
	}
	if pending >= 0 {
		fmt.Printf("Pending candidates: %d\n", pending)
	}
	if ok {
		fmt.Printf("Last run: %s\n", last.Format(time.RFC3339))
	} else {
		fmt.Println("Last run: never")
	}
}

// countCurationPending counts non-deleted memories whose scorer_version is
// missing or below the current scorer — i.e. what the worker still has to do.
func countCurationPending(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM memories
		WHERE deleted_at IS NULL
		  AND (
		    metadata IS NULL
		    OR json_extract(metadata, '$.scorer_version') IS NULL
		    OR json_extract(metadata, '$.scorer_version') < ?
		  )`, memory.QualityScorerVersion).Scan(&n)
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return 0, nil
	}
	return n, err
}

func runCurationSetEnabled(enabled bool) {
	configFile, cfg := loadWritableConfig()
	cfg.Curation.Enabled = enabled
	writeConfigFile(configFile, cfg)
	fmt.Printf("Curation worker %s. Wrote %s\n", enabledLabel(enabled), configFile)
	fmt.Fprintln(os.Stderr, "Note: restart the MCP server (anchored serve) for this to take effect on a running session.")
}

// runCurationReconcile re-scores every memory with the current scorer formula
// in a single pass, repairing stale curation_status flags left by older
// formula versions. Unlike the serve-time worker (which is throttled by
// max_updates_per_run), this processes the whole corpus at once and is the
// recommended one-shot to run after upgrading.
func runCurationReconcile(args []string) {
	fs := newFlagSet("curation reconcile")
	configPath := fs.String("config", "", "path to config file")
	threshold := fs.Float64("threshold", memory.RemoteQualityThreshold, "quality score below this is marked low_signal")
	category := fs.String("category", "", "limit reconcile to a single category")
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	fs.Parse(args)

	_, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	ctx := context.Background()
	memories, err := listAllLocalMemories(ctx, svc, *category)
	if err != nil {
		fmt.Fprintf(os.Stderr, "curation error: %v\n", err)
		os.Exit(1)
	}

	if !*yes {
		fmt.Fprintf(os.Stderr, "About to re-score %d memories (scorer v%d, threshold %.2f). Continue? [y/N]: ",
			len(memories), memory.QualityScorerVersion, *threshold)
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		if s := strings.ToLower(strings.TrimSpace(line)); s != "y" && s != "yes" {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return
		}
	}

	scanned, updated, flagged, cleared := 0, 0, 0, 0
	for _, m := range memories {
		scanned++
		meta := memory.ParseMetadata(m.Metadata)
		wasLowSignal := meta.CurationStatus == memory.CurationStatusLowSignal
		meta, changed := memory.RecurateMetadata(meta, m.Content, m.Category, m.ProjectID != nil, *threshold)
		if !changed {
			continue
		}
		if err := svc.UpdateMetadata(ctx, m.ID, meta.ToAny()); err != nil {
			fmt.Fprintf(os.Stderr, "metadata update failed for %s: %v\n", m.ID, err)
			continue
		}
		updated++
		isLowSignal := meta.CurationStatus == memory.CurationStatusLowSignal
		switch {
		case isLowSignal && !wasLowSignal:
			flagged++
		case !isLowSignal && wasLowSignal:
			cleared++
		}
	}

	// Record the version so the serve-time bootstrap doesn't redundantly
	// re-drain the corpus we just reconciled.
	if *category == "" {
		if err := setCurationReconciledVersion(ctx, svc.StoreDB(), memory.QualityScorerVersion); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to record reconciled version: %v\n", err)
		}
	}

	fmt.Printf("Reconciled %d memories\n", scanned)
	fmt.Printf("Updated metadata: %d\n", updated)
	fmt.Printf("Newly flagged low_signal: %d\n", flagged)
	fmt.Printf("Cleared stale low_signal: %d\n", cleared)
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func runCurationScore(args []string) {
	if args[0] != "score" {
		printCurationUsage()
		os.Exit(1)
	}

	fs := newFlagSet("curation score")
	configPath := fs.String("config", "", "path to config file")
	apply := fs.Bool("apply", false, "persist quality_score/importance/curation_status metadata")
	threshold := fs.Float64("threshold", 0.55, "quality score below this is marked low_signal")
	limit := fs.Int("limit", 25, "number of low-signal examples to print")
	category := fs.String("category", "", "filter by category")
	fs.Parse(args[1:])

	_, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	ctx := context.Background()
	memories, err := listAllLocalMemories(ctx, svc, *category)
	if err != nil {
		fmt.Fprintf(os.Stderr, "curation error: %v\n", err)
		os.Exit(1)
	}

	type scored struct {
		memory memory.Memory
		score  float64
	}
	low := make([]scored, 0)
	updated := 0
	for _, m := range memories {
		meta := memory.ParseMetadata(m.Metadata)
		meta, changed := memory.RecurateMetadata(meta, m.Content, m.Category, m.ProjectID != nil, *threshold)
		score := meta.QualityScore
		if score < *threshold && !meta.Pinned {
			low = append(low, scored{memory: m, score: score})
		}
		if *apply && changed {
			if err := svc.UpdateMetadata(ctx, m.ID, meta.ToAny()); err != nil {
				fmt.Fprintf(os.Stderr, "metadata update failed for %s: %v\n", m.ID, err)
				continue
			}
			updated++
		}
	}

	sort.Slice(low, func(i, j int) bool { return low[i].score < low[j].score })
	fmt.Printf("Scanned %d memories\n", len(memories))
	fmt.Printf("Low-signal (< %.2f): %d\n", *threshold, len(low))
	if *apply {
		fmt.Printf("Updated metadata: %d\n", updated)
	} else {
		fmt.Println("Dry-run only. Re-run with --apply to persist curation metadata.")
	}

	max := *limit
	if max > len(low) {
		max = len(low)
	}
	if max > 0 {
		fmt.Println("\nLowest-signal examples:")
	}
	for i := 0; i < max; i++ {
		m := low[i].memory
		fmt.Printf("%2d. score=%.2f [%s] id=%s %s\n", i+1, low[i].score, m.Category, m.ID, truncateForCuration(m.Content, 120))
	}
}

func listAllLocalMemories(ctx context.Context, svc *memory.Service, category string) ([]memory.Memory, error) {
	const pageSize = 1000
	var all []memory.Memory
	offset := 0
	for {
		page, err := svc.List(ctx, memory.ListOptions{Limit: pageSize, Offset: offset, Category: category})
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < pageSize {
			return all, nil
		}
		offset += pageSize
	}
}

func truncateForCuration(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
