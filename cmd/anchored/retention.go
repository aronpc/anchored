package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jholhewres/anchored/pkg/memory"
)

func runRetention(args []string) {
	fs := newFlagSet("retention")
	configPath := fs.String("config", "", "path to config file")
	dryRun := fs.Bool("dry-run", false, "preview without deleting")
	operationalTTL := fs.Int("operational-ttl", 14, "days to keep operational memories")
	episodicTTL := fs.Int("episodic-ttl", 90, "days to keep episodic memories")
	fs.Parse(args)

	if len(fs.Args()) == 0 || fs.Args()[0] != "sweep" {
		fmt.Fprintln(os.Stderr, "usage: anchored retention sweep [--dry-run]")
		os.Exit(1)
	}

	_, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	ctx := context.Background()
	db := svc.StoreDB()

	now := time.Now().UTC()
	opCutoff := now.AddDate(0, 0, -*operationalTTL)
	epCutoff := now.AddDate(0, 0, -*episodicTTL)

	rows, err := db.QueryContext(ctx,
		"SELECT id, metadata, category, created_at FROM memories WHERE deleted_at IS NULL")
	if err != nil {
		slog.Error("query failed", "error", err)
		os.Exit(1)
	}
	defer rows.Close()

	var toArchive []string
	var kept int

	for rows.Next() {
		var id, category string
		var metadataJSON sql.NullString
		var createdAt time.Time
		if err := rows.Scan(&id, &metadataJSON, &category, &createdAt); err != nil {
			continue
		}

		meta := memory.ParseMetadataFromJSON(metadataJSON.String)

		if meta.Pinned {
			kept++
			continue
		}

		if meta.Origin == "remote" {
			kept++
			continue
		}

		switch meta.MemoryType {
		case memory.MemoryTypeOperational:
			if meta.ExpiresAt != "" {
				if meta.IsExpired(now) {
					toArchive = append(toArchive, id)
					continue
				}
			} else if createdAt.Before(opCutoff) {
				toArchive = append(toArchive, id)
				continue
			}
			kept++

		case memory.MemoryTypeEpisodic:
			if createdAt.Before(epCutoff) {
				toArchive = append(toArchive, id)
				continue
			}
			kept++

		default:
			kept++
		}
	}

	if len(toArchive) == 0 {
		fmt.Printf("Retention sweep: nothing to archive. All %d memories are within policy.\n", kept)
		return
	}

	if *dryRun {
		fmt.Printf("Retention sweep [dry-run]: %d memories would be soft-deleted, %d kept.\n",
			len(toArchive), kept)
		return
	}

	archived := 0
	for _, id := range toArchive {
		if err := svc.SoftForget(ctx, id); err != nil {
			slog.Warn("retention soft-delete failed", "id", id, "error", err)
			continue
		}
		archived++
	}

	fmt.Printf("Retention sweep: %d memories archived, %d kept.\n", archived, kept)
}
