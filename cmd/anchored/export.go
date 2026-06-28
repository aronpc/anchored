package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/jholhewres/anchored/pkg/memory"
)

func runExport(args []string) {
	fs := newFlagSet("export")
	project := fs.String("project", "", "filter by project ID")
	category := fs.String("category", "", "filter by category")
	source := fs.String("source", "", "filter by source")
	includeDeleted := fs.Bool("include-deleted", false, "include soft-deleted memories")
	output := fs.String("output", "", "output file (default: stdout)")
	format := fs.String("format", "jsonl", "output format: json or jsonl")
	configPath := fs.String("config", "", "path to config file")
	fs.Parse(args)

	if *format != "json" && *format != "jsonl" {
		fmt.Fprintf(os.Stderr, "unsupported format %q: use json or jsonl\n", *format)
		os.Exit(1)
	}

	_, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	ctx := context.Background()

	opts := memory.ListOptions{
		Category:       *category,
		ProjectID:      *project,
		Source:         *source,
		IncludeDeleted: *includeDeleted,
		Limit:          0,
	}

	memories, err := svc.List(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "export error: %v\n", err)
		os.Exit(1)
	}

	var w *os.File
	if *output != "" {
		f, err := os.Create(*output)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error creating output file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	} else {
		w = os.Stdout
	}

	// Strip embeddings from output — they're large binary blobs.
	for i := range memories {
		memories[i].Embedding = nil
	}

	switch *format {
	case "jsonl":
		enc := json.NewEncoder(w)
		for _, m := range memories {
			if err := enc.Encode(m); err != nil {
				fmt.Fprintf(os.Stderr, "error encoding memory: %v\n", err)
				os.Exit(1)
			}
		}
	case "json":
		data, err := json.MarshalIndent(memories, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error encoding memories: %v\n", err)
			os.Exit(1)
		}
		w.Write(data)
		w.Write([]byte("\n"))
	}

	if *output != "" {
		fmt.Fprintf(os.Stderr, "Exported %d memories to %s\n", len(memories), *output)
	}
}
