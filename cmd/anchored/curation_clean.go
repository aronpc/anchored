package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// runCurationClean removes low-signal memories from the local store.
//
// Default behavior: soft-delete (sets deleted_at). Pass --hard to remove rows
// from disk permanently. The selection criteria mirror what the remote sync
// filter blocks, so what gets cleaned locally is exactly what would never sync
// anyway. Memories with metadata.pinned=true are always preserved.
//
// Usage: anchored curation clean [--hard] [--threshold 0.55] [--dry-run] [--yes]
func runCurationClean(args []string) {
	fs := newFlagSet("curation clean")
	configPath := fs.String("config", "", "path to config file")
	hard := fs.Bool("hard", false, "permanently delete (default: soft-delete via deleted_at)")
	threshold := fs.Float64("threshold", 0.55, "delete memories with quality_score below this (or marked low_signal)")
	dryRun := fs.Bool("dry-run", false, "show what would be deleted without touching the DB")
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	fs.Parse(args)

	_, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	db := svc.StoreDB()

	// Build the WHERE clause once. We delete rows that:
	//   - are not already deleted
	//   - are not pinned
	//   - AND (marked low_signal OR have a quality_score below threshold)
	where := `
		deleted_at IS NULL
		AND (json_extract(metadata, '$.pinned') IS NULL OR json_extract(metadata, '$.pinned') != 1)
		AND (
			json_extract(metadata, '$.curation_status') = 'low_signal'
			OR (
				json_extract(metadata, '$.quality_score') IS NOT NULL
				AND CAST(json_extract(metadata, '$.quality_score') AS REAL) < ?
			)
		)`

	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories WHERE`+where, *threshold).Scan(&total); err != nil {
		fmt.Fprintf(os.Stderr, "count error: %v\n", err)
		os.Exit(1)
	}

	mode := "soft-delete"
	if *hard {
		mode = "HARD delete (permanent)"
	}
	fmt.Printf("Would %s %d memories (threshold=%.2f)\n", mode, total, *threshold)

	if total == 0 {
		fmt.Println("Nothing to clean.")
		return
	}

	// Show a small sample so the user can sanity-check.
	rows, err := db.Query(`
		SELECT id, category,
		       COALESCE(CAST(json_extract(metadata, '$.quality_score') AS REAL), 0),
		       substr(content, 1, 100)
		FROM memories WHERE`+where+` ORDER BY 3 ASC LIMIT 10`, *threshold)
	if err == nil {
		fmt.Println("\nSample (lowest-quality first):")
		for rows.Next() {
			var id, cat, snippet string
			var qs float64
			if err := rows.Scan(&id, &cat, &qs, &snippet); err == nil {
				fmt.Printf("  q=%.2f [%s] %s :: %s\n", qs, cat, id[:8], truncateForCuration(snippet, 80))
			}
		}
		rows.Close()
	}

	if *dryRun {
		fmt.Println("\nDry-run only. Re-run without --dry-run to apply.")
		return
	}

	if !*yes {
		fmt.Printf("\nProceed with %s? [y/N] ", mode)
		reader := bufio.NewReader(os.Stdin)
		ans, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(ans)) != "y" {
			fmt.Println("Aborted.")
			return
		}
	}

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "begin tx: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = tx.Rollback() }()

	var stmt string
	if *hard {
		stmt = `DELETE FROM memories WHERE` + where
	} else {
		stmt = `UPDATE memories SET deleted_at = datetime('now'), updated_at = datetime('now') WHERE` + where
	}
	res, err := tx.ExecContext(ctx, stmt, *threshold)
	if err != nil {
		fmt.Fprintf(os.Stderr, "delete error: %v\n", err)
		os.Exit(1)
	}
	affected, _ := res.RowsAffected()

	if err := tx.Commit(); err != nil {
		fmt.Fprintf(os.Stderr, "commit error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nDone. %s %d memories.\n", mode, affected)
	if !*hard {
		fmt.Println("Tip: re-run with --hard to permanently remove tombstones.")
	}
}

// runCurationRestore swaps the active local DB for a backup file. The current
// DB is backed up under bkps/ with a timestamped suffix before the swap, so
// the operation is reversible.
//
// Usage:
//   anchored curation restore --latest        # pick newest in bkps/
//   anchored curation restore --from PATH     # explicit file
//   anchored curation restore                 # interactive listing
func runCurationRestore(args []string) {
	fs := newFlagSet("curation restore")
	configPath := fs.String("config", "", "path to config file")
	fromPath := fs.String("from", "", "explicit backup file to restore")
	latest := fs.Bool("latest", false, "restore the most recent file in bkps/")
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	fs.Parse(args)

	cfg, _, _, err := initService(*configPath)
	if err != nil {
		// Permit restore even if the current DB is corrupt: fall back to
		// path resolution from environment.
		slog.Warn("initService failed, continuing in restore-only mode", "error", err)
	}

	var dbPath string
	if cfg != nil {
		dbPath = expandPath(cfg.Memory.DatabasePath)
	}
	if dbPath == "" {
		dbPath = expandPath("~/.anchored/data/anchored.db")
	}
	bkpsDir := filepath.Join(filepath.Dir(dbPath), "bkps")

	candidate := *fromPath
	switch {
	case candidate != "":
		// explicit path; nothing else to do here
	case *latest:
		c, err := newestBackup(bkpsDir, dbPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		candidate = c
	default:
		c, err := interactivePickBackup(bkpsDir, dbPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if c == "" {
			fmt.Println("Aborted.")
			return
		}
		candidate = c
	}

	info, err := os.Stat(candidate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "backup not readable: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Restore plan:\n")
	fmt.Printf("  from: %s (%.1f MiB, %s)\n", candidate, float64(info.Size())/1024/1024, info.ModTime().Format(time.RFC3339))
	fmt.Printf("  to:   %s\n", dbPath)

	if !*yes {
		fmt.Print("Proceed? Current DB will be backed up first. [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		ans, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(ans)) != "y" {
			fmt.Println("Aborted.")
			return
		}
	}

	// Snapshot the current DB so a wrong restore is always reversible.
	if _, err := os.Stat(dbPath); err == nil {
		stamp := time.Now().UTC().Format("20060102150405")
		snap := filepath.Join(bkpsDir, "anchored.db.bak.pre-restore-"+stamp)
		if err := copyFile(dbPath, snap); err != nil {
			fmt.Fprintf(os.Stderr, "snapshot current db failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("  snapshot: %s\n", snap)
	}

	// Remove stale -wal / -shm so SQLite opens the restored copy cleanly.
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")

	if err := copyFile(candidate, dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "restore copy failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Restore complete.")
	fmt.Println("Tip: kill any running MCP daemons before continuing (`pkill -f anchored`).")
}

// --- helpers ---

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, _ := os.UserHomeDir(); home != "" {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// newestBackup returns the most-recently-modified .db file in dir, excluding
// SQLite sidecar files (-wal, -shm) and the active live DB itself.
func newestBackup(dir, activeDB string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read bkps dir %s: %w", dir, err)
	}
	var best os.FileInfo
	var bestName string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") {
			continue
		}
		full := filepath.Join(dir, name)
		if full == activeDB {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if best == nil || info.ModTime().After(best.ModTime()) {
			best, bestName = info, full
		}
	}
	if best == nil {
		return "", errors.New("no candidate backup found in bkps/")
	}
	return bestName, nil
}

func interactivePickBackup(dir, activeDB string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read bkps dir %s: %w", dir, err)
	}
	type item struct {
		path string
		size int64
		mod  time.Time
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") {
			continue
		}
		full := filepath.Join(dir, name)
		if full == activeDB {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{full, info.Size(), info.ModTime()})
	}
	if len(items) == 0 {
		return "", errors.New("no backups found in " + dir)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
	fmt.Println("Available backups (newest first):")
	for i, it := range items {
		fmt.Printf("  [%d] %s  (%.1f MiB, %s)\n", i+1, filepath.Base(it.path),
			float64(it.size)/1024/1024, it.mod.Format(time.RFC3339))
	}
	fmt.Print("Pick one [1] or blank to cancel: ")
	reader := bufio.NewReader(os.Stdin)
	ans, _ := reader.ReadString('\n')
	ans = strings.TrimSpace(ans)
	if ans == "" {
		return "", nil
	}
	idx := 1
	if _, err := fmt.Sscanf(ans, "%d", &idx); err != nil {
		return "", fmt.Errorf("invalid selection: %q", ans)
	}
	if idx < 1 || idx > len(items) {
		return "", fmt.Errorf("selection out of range: %d", idx)
	}
	return items[idx-1].path, nil
}

// copyFile is provided by purge.go.

