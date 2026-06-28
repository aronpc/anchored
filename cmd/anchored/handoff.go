package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jholhewres/anchored/pkg/memory"
)

func runHandoff(args []string) {
	fs := newFlagSet("handoff")
	cwd := fs.String("cwd", "", "current working directory for project detection")
	configPath := fs.String("config", "", "path to config file")
	scope := fs.String("scope", "project", "scope: project, team, user")
	ttlHours := fs.Int("ttl", 48, "hours until handoff expires")
	fs.Parse(args)

	content, err := io.ReadAll(os.Stdin)
	if err != nil {
		slog.Error("failed to read stdin", "error", err)
		os.Exit(1)
	}

	text := strings.TrimSpace(string(content))
	if text == "" {
		fmt.Println("No content to capture for handoff.")
		return
	}

	if *ttlHours < 1 {
		fmt.Fprintln(os.Stderr, "ttl must be at least 1 hour")
		os.Exit(1)
	}

	_, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	cwdVal := *cwd
	if cwdVal == "" {
		cwdVal = "."
	}

	expiresAt := time.Now().Add(time.Duration(*ttlHours) * time.Hour).Format(time.RFC3339)
	meta := memory.HandoffMetadata(*scope, expiresAt)

	m, err := svc.SaveWithOptions(context.Background(), memory.SaveOptions{
		Content:   text,
		Category:  "summary",
		Source:    "handoff",
		CWD:       cwdVal,
		Metadata:  meta.ToAny(),
	})
	if err != nil {
		slog.Error("failed to save handoff", "error", err)
		os.Exit(1)
	}

	fmt.Printf("Handoff saved [%s] memory %s (scope=%s, ttl=%dh, %d bytes)\n",
		m.Category, m.ID, *scope, *ttlHours, len(text))
}
