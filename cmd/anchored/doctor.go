package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func runDoctor(args []string) {
	fs := newFlagSet("doctor")
	configPath := fs.String("config", "", "path to config file")
	fs.Parse(args)

	fmt.Printf("anchored doctor — diagnostics for v%s\n\n", Version)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		printCheck(false, "config loaded", err.Error(), "")
		os.Exit(1)
	}

	home, _ := os.UserHomeDir()

	checkBinary(home)
	checkONNX(cfg.Embedding.ModelDir)
	checkDatabase(cfg.Memory.DatabasePath, cfg.Embedding.Dimensions)
	checkMCPRegistration(home)
	checkConfig(home, cfg)

	fmt.Println()
}

func checkBinary(home string) {
	canonical := filepath.Join(home, ".anchored", "bin", "anchored")
	info, err := os.Stat(canonical)
	if err != nil {
		printCheck(false, "binary at ~/.anchored/bin/anchored", "missing", "run install.sh or 'go build -o ~/.anchored/bin/anchored ./cmd/anchored'")
		return
	}
	if info.Mode()&0o111 == 0 {
		printCheck(false, "binary at ~/.anchored/bin/anchored", "not executable", "chmod +x "+canonical)
		return
	}

	out, err := exec.Command(canonical, "--version").Output()
	if err != nil {
		printCheck(false, "binary executable", err.Error(), "")
		return
	}
	v := strings.TrimSpace(strings.TrimPrefix(string(out), "anchored "))
	if v != Version {
		printCheck(false, fmt.Sprintf("installed binary version (%s)", v),
			fmt.Sprintf("source is v%s but installed is v%s", Version, v),
			"rebuild and reinstall: cp bin/anchored ~/.anchored/bin/anchored")
		return
	}
	printCheck(true, fmt.Sprintf("binary v%s at ~/.anchored/bin/anchored", v), "", "")

	if pathHas(canonical) {
		printCheck(true, "~/.anchored/bin in PATH", "", "")
	} else {
		printCheck(false, "~/.anchored/bin in PATH",
			"binary not reachable as 'anchored' from shell",
			"add to your shell rc: export PATH=\"$HOME/.anchored/bin:$PATH\"")
	}
}

func checkONNX(modelDir string) {
	entries, err := os.ReadDir(modelDir)
	if err != nil {
		printCheck(false, "ONNX model dir", err.Error(),
			"run 'anchored serve' once to auto-download the model (~470MB)")
		return
	}

	var modelFound, tokenizerFound bool
	var modelName string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		modelName = e.Name()
		sub := filepath.Join(modelDir, e.Name())
		if _, err := os.Stat(filepath.Join(sub, "model.onnx")); err == nil {
			modelFound = true
		}
		if _, err := os.Stat(filepath.Join(sub, "tokenizer.json")); err == nil {
			tokenizerFound = true
		}
	}

	if !modelFound {
		printCheck(false, "ONNX model.onnx", "missing in "+modelDir,
			"run 'anchored serve' once to trigger auto-download")
		return
	}
	printCheck(true, fmt.Sprintf("ONNX model: %s", modelName), "", "")

	if !tokenizerFound {
		printCheck(false, "tokenizer.json", "missing — embedder will fall back to slow path",
			"delete the model dir and re-run 'anchored serve' to re-download")
	} else {
		printCheck(true, "tokenizer.json present", "", "")
	}
}

func checkDatabase(dbPath string, expectedDims int) {
	if _, err := os.Stat(dbPath); err != nil {
		printCheck(false, "database file", err.Error(),
			"run 'anchored serve' or any subcommand to initialize")
		return
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		printCheck(false, "database open", err.Error(), "")
		return
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var jm string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&jm); err == nil {
		printCheck(strings.EqualFold(jm, "wal"), fmt.Sprintf("journal_mode = %s", jm), "", "")
	}

	var ftsCount int
	err = db.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name='memories_fts'").Scan(&ftsCount)
	if err != nil || ftsCount == 0 {
		printCheck(false, "FTS5 enabled (memories_fts table)", "missing",
			"rebuild binary with -DSQLITE_ENABLE_FTS5 (Makefile already does this for 'make build')")
	} else {
		printCheck(true, "FTS5 enabled", "", "")
	}

	var memCount int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM memories WHERE deleted_at IS NULL").Scan(&memCount); err == nil {
		printCheck(true, fmt.Sprintf("%d active memories", memCount), "", "")
	}

	rows, err := db.QueryContext(ctx,
		"SELECT category, count(*) FROM memories WHERE deleted_at IS NULL GROUP BY category ORDER BY 2 DESC")
	if err == nil {
		defer rows.Close()
		var parts []string
		for rows.Next() {
			var cat string
			var n int
			if err := rows.Scan(&cat, &n); err == nil {
				parts = append(parts, fmt.Sprintf("%s=%d", cat, n))
			}
		}
		if len(parts) > 0 {
			printCheck(true, "categories: "+strings.Join(parts, ", "), "", "")
		}
	}

	var embRows int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM memories WHERE embedding IS NOT NULL AND length(embedding) > 0 AND deleted_at IS NULL").Scan(&embRows); err == nil {
		coverage := 0
		if memCount > 0 {
			coverage = embRows * 100 / memCount
		}
		printCheck(true,
			fmt.Sprintf("%d/%d memories embedded (%d%% — dim=%d)", embRows, memCount, coverage, expectedDims),
			"", "")
	}
}

type mcpProbe struct {
	tool    string
	path    string
	scope   string
	hint    string
}

func checkMCPRegistration(home string) {
	wsVSCode := filepath.Join(".", ".vscode", "mcp.json")
	probes := []mcpProbe{
		{"Claude Code", filepath.Join(home, ".claude.json"), "user", "claude mcp add -s user anchored anchored"},
		{"Cursor", filepath.Join(home, ".cursor", "mcp.json"), "user", "anchored init --tool cursor"},
		{"OpenCode", filepath.Join(home, ".config", "opencode", "opencode.json"), "user", "anchored init --tool opencode"},
		{"Gemini CLI", filepath.Join(home, ".gemini", "settings.json"), "user", "anchored init --tool gemini"},
		{"Antigravity 2.0", filepath.Join(home, ".gemini", "config", "mcp_config.json"), "user", "anchored init --tool agy"},
		{"Antigravity CLI (agy)", filepath.Join(home, ".gemini", "antigravity-cli", "mcp_config.json"), "user", "anchored init --tool agy"},
		{"VS Code Copilot (workspace)", wsVSCode, "workspace", "run from your project root and create .vscode/mcp.json with an 'anchored' entry under mcpServers"},
	}

	for _, p := range probes {
		data, err := os.ReadFile(p.path)
		if err != nil {
			if os.IsNotExist(err) {
				printCheck(false, fmt.Sprintf("MCP registered for %s", p.tool),
					"config file not found ("+p.path+")",
					p.hint)
				continue
			}
			printCheck(false, fmt.Sprintf("MCP registered for %s", p.tool), err.Error(), p.hint)
			continue
		}

		if hasAnchoredEntry(data) {
			printCheck(true, fmt.Sprintf("MCP registered for %s (%s)", p.tool, p.path), "", "")
		} else {
			printCheck(false, fmt.Sprintf("MCP registered for %s", p.tool),
				"anchored not in mcpServers",
				p.hint)
		}
	}
}

func hasAnchoredEntry(data []byte) bool {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	if servers, ok := raw["mcpServers"].(map[string]any); ok {
		if _, found := servers["anchored"]; found {
			return true
		}
	}
	// Claude Code stores some configs under projects/<path>/mcpServers as well
	if projects, ok := raw["projects"].(map[string]any); ok {
		for _, v := range projects {
			if p, ok := v.(map[string]any); ok {
				if servers, ok := p["mcpServers"].(map[string]any); ok {
					if _, found := servers["anchored"]; found {
						return true
					}
				}
			}
		}
	}
	return false
}

func checkConfig(home string, cfg interface{}) {
	configFile := filepath.Join(home, ".anchored", "config.yaml")
	if _, err := os.Stat(configFile); err == nil {
		printCheck(true, "config.yaml present at ~/.anchored/config.yaml", "", "")
	} else {
		printCheck(true, "config.yaml absent (using defaults)", "", "")
	}

	identityFile := filepath.Join(home, ".anchored", "identity.md")
	if _, err := os.Stat(identityFile); err == nil {
		printCheck(true, "identity.md present", "", "")
	} else {
		printCheck(false, "identity.md missing", "L0 layer will be empty",
			"create with: anchored identity edit")
	}
}

func printCheck(ok bool, label, detail, hint string) {
	mark := "[ ]"
	if ok {
		mark = "[x]"
	}
	fmt.Printf("%s %s", mark, label)
	if detail != "" {
		fmt.Printf(" — %s", detail)
	}
	fmt.Println()
	if !ok && hint != "" {
		fmt.Printf("    → %s\n", hint)
	}
}

func pathHas(target string) bool {
	dir := filepath.Dir(target)
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return true
		}
	}
	// Also try resolving via 'which anchored'
	out, err := exec.Command("which", "anchored").Output()
	if err != nil {
		return false
	}
	resolved := strings.TrimSpace(string(out))
	if resolved == "" {
		return false
	}
	if real, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = real
	}
	return resolved == target
}
