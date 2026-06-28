package main

import (
	"encoding/json"
	"strings"
)

// workingSetDelta extracts the files, commands and tests a tool call touched,
// for feeding the session working set. It is intentionally lenient: a parse
// failure yields empty slices (the caller skips the update) rather than an
// error, preserving the fail-safe hook contract.
func workingSetDelta(toolName, inputText string) (files, commands, tests []string) {
	if inputText == "" {
		return nil, nil, nil
	}
	var in struct {
		FilePath string `json:"file_path"`
		Command  string `json:"command"`
	}
	if err := json.Unmarshal([]byte(inputText), &in); err != nil {
		return nil, nil, nil
	}

	switch toolName {
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		if fp := strings.TrimSpace(in.FilePath); fp != "" {
			files = append(files, fp)
		}
	case "Read":
		// A read is weaker focus than an edit, but still signals what the
		// session is looking at.
		if fp := strings.TrimSpace(in.FilePath); fp != "" {
			files = append(files, fp)
		}
	case "Bash":
		cmd := strings.TrimSpace(in.Command)
		if cmd == "" {
			return files, commands, tests
		}
		short := truncateRunes(cmd, 120)
		if isTestCommand(cmd) {
			tests = append(tests, short)
		} else {
			commands = append(commands, short)
		}
	}
	return files, commands, tests
}
