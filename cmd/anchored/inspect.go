package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
)

func runInspect(args []string) {
	fs := newFlagSet("inspect")
	configPath := fs.String("config", "", "path to config file")
	fs.Parse(args)

	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "Usage: anchored inspect <memory-id>")
		os.Exit(1)
	}

	id := fs.Arg(0)

	_, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	ctx := context.Background()

	m, err := svc.Get(ctx, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error fetching memory: %v\n", err)
		os.Exit(1)
	}
	if m == nil {
		fmt.Fprintf(os.Stderr, "memory %s not found\n", id)
		os.Exit(1)
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error marshaling memory: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(string(data))
}
