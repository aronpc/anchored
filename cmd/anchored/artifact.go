package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"

	ctxpkg "github.com/jholhewres/anchored/pkg/context"
)

func runArtifact(args []string) {
	if len(args) == 0 {
		printArtifactUsage()
		return
	}
	switch args[0] {
	case "add":
		runArtifactAdd(args[1:])
	case "search":
		runArtifactSearch(args[1:])
	case "list":
		runArtifactList(args[1:])
	case "prune":
		runArtifactPrune(args[1:])
	default:
		printArtifactUsage()
	}
}

func printArtifactUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  anchored artifact add --type TYPE [--file PATH] [--label LABEL] [--source TOOL] [--project ID] [--ttl-hours N]")
	fmt.Fprintln(os.Stderr, "  anchored artifact search <query> [--project ID]")
	fmt.Fprintln(os.Stderr, "  anchored artifact list [--project ID] [--limit N]")
	fmt.Fprintln(os.Stderr, "  anchored artifact prune")
}

func runArtifactAdd(args []string) {
	fs := newFlagSet("artifact add")
	configPath := fs.String("config", "", "path to config file")
	filePath := fs.String("file", "", "path to file to add (default: read stdin)")
	artifactType := fs.String("type", "", "artifact type (required)")
	label := fs.String("label", "", "human-readable label")
	source := fs.String("source", "", "source tool name")
	projectID := fs.String("project", "", "project ID")
	ttlHours := fs.Int("ttl-hours", 0, "TTL in hours (0 = no expiry)")
	fs.Parse(args)

	if *artifactType == "" {
		fmt.Fprintln(os.Stderr, "error: --type is required")
		os.Exit(1)
	}

	var content []byte
	var err error
	if *filePath != "" {
		content, err = os.ReadFile(*filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading file: %v\n", err)
			os.Exit(1)
		}
	} else {
		content, err = io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading stdin: %v\n", err)
			os.Exit(1)
		}
	}

	if len(content) == 0 {
		fmt.Fprintln(os.Stderr, "error: empty content")
		os.Exit(1)
	}

	_, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	store := ctxpkg.NewStore(svc.StoreDB(), nil)
	if err := store.PrepareStatements(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare statements: %v\n", err)
		os.Exit(1)
	}
	chunker := ctxpkg.NewChunker(4096)

	in := ctxpkg.ArtifactInput{
		ProjectID:   *projectID,
		Type:        *artifactType,
		SourceTool:  *source,
		SourceLabel: *label,
		Content:     string(content),
		TTLHours:    *ttlHours,
	}

	ctx := context.Background()
	id, err := store.AddArtifact(ctx, chunker, in, 336)
	if err != nil {
		fmt.Fprintf(os.Stderr, "add artifact: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("artifact: %s\n", id)
}

func runArtifactSearch(args []string) {
	fs := newFlagSet("artifact search")
	configPath := fs.String("config", "", "path to config file")
	projectID := fs.String("project", "", "project ID filter")
	limit := fs.Int("limit", 10, "max results")
	fs.Parse(reorderArgsForFlag(fs, args))

	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "Usage: anchored artifact search <query>")
		os.Exit(1)
	}
	query := fs.Arg(0)

	_, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	store := ctxpkg.NewStore(svc.StoreDB(), nil)
	if err := store.PrepareStatements(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare statements: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	results, err := store.SearchArtifacts(ctx, query, *limit, *projectID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "search: %v\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		fmt.Println("no results")
		return
	}

	for _, a := range results {
		ttl := "no expiry"
		if a.TTLExpiresAt != nil {
			ttl = a.TTLExpiresAt.Format("2006-01-02T15:04:05Z")
		}
		fmt.Printf("id=%-34s type=%-12s label=%-20s size=%d ttl=%s\n",
			a.ID, a.Type, a.SourceLabel, a.SizeBytes, ttl)
	}
}

func runArtifactList(args []string) {
	fs := newFlagSet("artifact list")
	configPath := fs.String("config", "", "path to config file")
	projectID := fs.String("project", "", "project ID filter")
	limit := fs.Int("limit", 20, "max results")
	jsonOut := fs.Bool("json", false, "emit JSON array")
	fs.Parse(args)

	_, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	store := ctxpkg.NewStore(svc.StoreDB(), nil)
	if err := store.PrepareStatements(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare statements: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	artifacts, err := store.ListArtifacts(ctx, *projectID, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(artifacts)
		return
	}

	if len(artifacts) == 0 {
		fmt.Println("no artifacts")
		return
	}

	for _, a := range artifacts {
		ttl := "no expiry"
		if a.TTLExpiresAt != nil {
			ttl = a.TTLExpiresAt.Format("2006-01-02T15:04:05Z")
		}
		fmt.Printf("%-34s  %-12s  %-20s  %s bytes  %s\n",
			a.ID, a.Type, a.SourceLabel,
			strconv.Itoa(a.SizeBytes), ttl)
	}
}

func runArtifactPrune(args []string) {
	fs := newFlagSet("artifact prune")
	configPath := fs.String("config", "", "path to config file")
	fs.Parse(args)

	_, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	store := ctxpkg.NewStore(svc.StoreDB(), nil)
	if err := store.PrepareStatements(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare statements: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	n, err := store.PruneExpired(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prune: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("pruned %d expired artifact(s)\n", n)
}
