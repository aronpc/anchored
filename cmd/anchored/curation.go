package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"

	"github.com/jholhewres/anchored/pkg/memory"
)

func runCuration(args []string) {
	if len(args) == 0 {
		printCurationUsage()
		os.Exit(1)
	}
	switch args[0] {
	case "score":
		runCurationScore(args)
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
	fmt.Fprintln(os.Stderr, "  anchored curation score   [--apply] [--threshold 0.55] [--limit 20]")
	fmt.Fprintln(os.Stderr, "  anchored curation clean   [--hard] [--threshold 0.55] [--dry-run] [--yes]")
	fmt.Fprintln(os.Stderr, "  anchored curation restore [--from PATH] [--latest] [--yes]")
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
		score := memory.ScoreQuality(m.Content, m.Category, m.ProjectID != nil)
		meta := memory.ParseMetadata(m.Metadata)
		meta.QualityScore = score
		if meta.Importance == 0 || meta.Importance > score {
			meta.Importance = score
		}
		if score < *threshold && !meta.Pinned {
			meta.CurationStatus = memory.CurationStatusLowSignal
			low = append(low, scored{memory: m, score: score})
		}
		if *apply {
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
