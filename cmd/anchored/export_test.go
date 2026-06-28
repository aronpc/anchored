package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunExport_JSONL(t *testing.T) {
	cfgPath := newTestEnv(t)

	_, _, svc, err := initService(cfgPath)
	if err != nil {
		t.Fatalf("initService: %v", err)
	}

	ctx := context.Background()
	svc.Save(ctx, "memory one", "fact", "cli", "")
	svc.Save(ctx, "memory two", "decision", "import", "")
	svc.Close()

	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w

	runExport([]string{"--config", cfgPath, "--format", "jsonl"})

	w.Close()
	os.Stdout = old

	scanner := bufio.NewScanner(r)
	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Fatalf("line %d is not valid JSON: %v\n%s", count, err, line)
		}
		if _, hasEmbedding := parsed["embedding"]; hasEmbedding {
			arr, ok := parsed["embedding"].([]any)
			if ok && len(arr) > 0 {
				t.Errorf("embedding should be stripped, got %d elements", len(arr))
			}
		}
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 JSONL lines, got %d", count)
	}
}

func TestRunExport_JSON(t *testing.T) {
	cfgPath := newTestEnv(t)

	_, _, svc, err := initService(cfgPath)
	if err != nil {
		t.Fatalf("initService: %v", err)
	}

	ctx := context.Background()
	svc.Save(ctx, "json export test", "fact", "test", "")
	svc.Save(ctx, "second memory", "plan", "cli", "")
	svc.Close()

	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w

	runExport([]string{"--config", cfgPath, "--format", "json"})

	w.Close()
	os.Stdout = old

	outputBytes, _ := io.ReadAll(r)
	output := string(outputBytes)

	var parsed []map[string]any
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("output is not valid JSON array: %v\n%s", err, output)
	}
	if len(parsed) != 2 {
		t.Errorf("expected 2 items, got %d", len(parsed))
	}
}

func TestRunExport_ToFile(t *testing.T) {
	cfgPath := newTestEnv(t)
	outFile := filepath.Join(t.TempDir(), "export.jsonl")

	_, _, svc, err := initService(cfgPath)
	if err != nil {
		t.Fatalf("initService: %v", err)
	}

	ctx := context.Background()
	svc.Save(ctx, "file export test", "fact", "test", "")
	svc.Close()

	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w

	runExport([]string{"--config", cfgPath, "--output", outFile})

	w.Close()
	os.Stdout = old
	io.ReadAll(r)

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}

	lines := strings.Count(string(data), "\n")
	if lines != 1 {
		t.Errorf("expected 1 line in output file, got %d", lines)
	}
}
