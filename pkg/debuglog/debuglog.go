// Package debuglog provides a lightweight, opt-in NDJSON event logger for
// auditing how anchored is actually being used by the host AI tool.
//
// The goal is observability, not telemetry: when the user wants to understand
// why memory tools fired (or didn't), they enable Debug.Enabled in
// ~/.anchored/config.yaml (or set ANCHORED_DEBUG=1) and get an append-only
// NDJSON file they can grep, jq, or replay.
//
// Design rules:
//   - Never fail the caller. Open/Write errors are swallowed; the worst case
//     is a missing log line, never a broken hook or MCP call.
//   - One JSON object per line ({ts, event, ...fields}) so the file is
//     trivially parsable with jq -c or Python's json.loads per line.
//   - Append-only with O_APPEND so concurrent processes (multiple hook
//     invocations + the MCP server) don't clobber each other; we still take a
//     mutex per *Logger to keep individual writes intact.
package debuglog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jholhewres/anchored/pkg/config"

	util "github.com/jholhewres/anchored/pkg/util"
)

// Logger is a single-file NDJSON appender. The zero value is a valid no-op
// Logger; callers can safely call Event on a nil receiver.
type Logger struct {
	mu      sync.Mutex
	file    *os.File
	enabled bool
	path    string
}

// Open returns a Logger configured from cfg, with environment overrides.
//
// Env overrides (highest precedence):
//   - ANCHORED_DEBUG=1|true|on  → force enable
//   - ANCHORED_DEBUG=0|false|off → force disable
//   - ANCHORED_DEBUG_PATH=/path  → override log file path
//
// When disabled, Open returns a zero-value Logger that no-ops on Event so
// call sites stay branch-free.
func Open(cfg *config.Config) *Logger {
	enabled, path := resolve(cfg)
	if !enabled {
		return &Logger{}
	}

	if path == "" {
		// Should not happen because resolve fills a default, but stay safe.
		return &Logger{}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		// Surface the failure once on stderr so the user isn't left wondering
		// why their explicitly-enabled log is silent. Hook stderr is captured
		// by Claude Code, so the message is reachable.
		fmt.Fprintf(os.Stderr, "anchored: debug log disabled (mkdir %s: %v)\n", filepath.Dir(path), err)
		return &Logger{}
	}

	// 0o600 because debug events embed prompt heads and tool args, which can
	// contain pasted secrets, file paths, and other private text. The log is
	// owner-only by design.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "anchored: debug log disabled (open %s: %v)\n", path, err)
		return &Logger{}
	}

	return &Logger{file: f, enabled: true, path: path}
}

// resolve returns (enabled, path) honoring env > config > defaults.
func resolve(cfg *config.Config) (bool, string) {
	enabled := false
	path := ""
	if cfg != nil {
		enabled = cfg.Debug.Enabled
		path = cfg.Debug.Path
	}

	if v, ok := os.LookupEnv("ANCHORED_DEBUG"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "on", "yes":
			enabled = true
		case "0", "false", "off", "no", "":
			enabled = false
		}
	}

	if v := strings.TrimSpace(os.Getenv("ANCHORED_DEBUG_PATH")); v != "" {
		path = v
	}

	if enabled && path == "" {
		// Default: a per-user file inside ~/.anchored so debug data lives
		// alongside the rest of anchored's state.
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, ".anchored", "debug.log")
		}
	}

	return enabled, util.ExpandHome(path)
}

// Path returns the resolved log path, or "" when disabled.
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Enabled reports whether this logger will actually write.
func (l *Logger) Enabled() bool {
	return l != nil && l.enabled && l.file != nil
}

// Event appends an NDJSON line. fields are merged with built-in {ts, event}
// keys; collisions are won by built-ins to keep the schema predictable.
func (l *Logger) Event(name string, fields map[string]any) {
	if !l.Enabled() {
		return
	}

	rec := make(map[string]any, len(fields)+3)
	for k, v := range fields {
		rec[k] = v
	}
	rec["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	rec["event"] = name
	rec["pid"] = os.Getpid()

	line, err := json.Marshal(rec)
	if err != nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.file.Write(append(line, '\n'))
}

// Close flushes and releases the underlying file handle. Safe on nil.
func (l *Logger) Close() error {
	if !l.Enabled() {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.file.Close()
	l.file = nil
	l.enabled = false
	return err
}

// Snippet truncates s to n runes, appending an ellipsis when trimmed. Use
// this on user-facing strings (prompt bodies, tool args) so the log doesn't
// balloon with multi-KB payloads.
//
// Rune-aware (not byte-aware) on purpose: PT-BR / multilingual prompts
// would otherwise be cut mid-codepoint, producing invalid UTF-8 that
// json.Marshal silently replaces with U+FFFD — corrupting the very
// evidence the debug log exists to capture.
func Snippet(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	out := make([]rune, 0, n)
	for _, r := range s {
		out = append(out, r)
		if len(out) == n {
			break
		}
	}
	return string(out) + "…"
}
