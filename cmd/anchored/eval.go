package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jholhewres/anchored/pkg/eval"
	"github.com/jholhewres/anchored/pkg/memory"
)

// runEval dispatches the local evaluation gates. Each sub-eval prints a report
// and exits non-zero when it fails, so `make eval` (and CI) gate on them.
//
//	anchored eval recall|sync-safety|identity [--fixture PATH] [--json]
func runEval(args []string) {
	if len(args) == 0 {
		printEvalUsage()
		os.Exit(2)
	}
	sub := args[0]
	fs := newFlagSet("eval " + sub)
	fixture := fs.String("fixture", "", "path to a YAML fixture (defaults to the embedded fixture)")
	jsonOut := fs.Bool("json", false, "emit the report as JSON")
	fs.Parse(args[1:])

	var (
		report eval.Report
		err    error
	)
	switch sub {
	case "recall":
		report, err = runEvalRecall(*fixture)
	case "sync-safety":
		var data []byte
		if data, err = eval.FixtureBytes(*fixture, "privacy.yaml"); err == nil {
			report, err = eval.RunSyncSafety(data)
		}
	case "identity":
		var data []byte
		if data, err = eval.FixtureBytes(*fixture, "identity.yaml"); err == nil {
			report, err = eval.RunIdentity(data)
		}
	default:
		printEvalUsage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "eval %s: %v\n", sub, err)
		os.Exit(2)
	}

	emitEvalReport(report, *jsonOut)
	if !report.Passed {
		os.Exit(1)
	}
}

func runEvalRecall(fixturePath string) (eval.Report, error) {
	data, err := eval.FixtureBytes(fixturePath, "recall_basic.yaml")
	if err != nil {
		return eval.Report{}, err
	}
	// A throwaway BM25-only store (no embeddings, no network) keeps the eval
	// deterministic and offline.
	dir, err := os.MkdirTemp("", "anchored-eval-*")
	if err != nil {
		return eval.Report{}, err
	}
	defer os.RemoveAll(dir)

	store, err := memory.NewSQLiteStore(filepath.Join(dir, "recall.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return eval.Report{}, err
	}
	defer store.Close()

	return eval.RunRecall(context.Background(), store, data)
}

func emitEvalReport(r eval.Report, asJSON bool) {
	if asJSON {
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(b))
		return
	}
	status := "PASS"
	if !r.Passed {
		status = "FAIL"
	}
	fmt.Printf("[%s] %s — %s\n", status, r.Name, r.Summary)
	for _, c := range r.Cases {
		mark := "ok"
		if !c.Passed {
			mark = "XX"
		}
		fmt.Printf("  %s %s — %s\n", mark, c.Name, c.Detail)
	}
}

func printEvalUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  anchored eval recall      [--fixture PATH] [--json]   Recall@K over a seeded corpus")
	fmt.Fprintln(os.Stderr, "  anchored eval sync-safety [--fixture PATH] [--json]   privacy filter blocks/redacts sensitive content")
	fmt.Fprintln(os.Stderr, "  anchored eval identity    [--fixture PATH] [--json]   remote-key derivation invariants")
}
