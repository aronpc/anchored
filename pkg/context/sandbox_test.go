//go:build !windows

package ctx

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSandbox_ShellSuccess(t *testing.T) {
	sb := NewSandbox(5*time.Second, 1<<20, "")
	result, err := sb.Execute(context.Background(), "shell", "echo hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if strings.TrimSpace(result.Stdout) != "hello" {
		t.Errorf("Stdout = %q, want %q", result.Stdout, "hello")
	}
	if result.TimedOut {
		t.Error("TimedOut = true, want false")
	}
}

func TestSandbox_Timeout(t *testing.T) {
	sb := NewSandbox(2*time.Second, 1<<20, "")
	result, err := sb.Execute(context.Background(), "shell", "sleep 30")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.TimedOut {
		t.Error("TimedOut = false, want true")
	}
	if result.Duration >= 10*time.Second {
		t.Errorf("Duration = %v, should be well under 10s", result.Duration)
	}
}

func TestSandbox_OutputTruncation(t *testing.T) {
	sb := NewSandbox(5*time.Second, 100, "")
	result, err := sb.Execute(context.Background(), "shell",
		`for i in $(seq 1 1000); do echo "line $i"; done`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Truncated {
		t.Error("Truncated = false, want true")
	}
	if len(result.Stdout) > 100 {
		t.Errorf("Stdout len = %d, want <= 100", len(result.Stdout))
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
}

func TestSandbox_NonZeroExitCode(t *testing.T) {
	sb := NewSandbox(5*time.Second, 1<<20, "")
	result, err := sb.Execute(context.Background(), "shell", "exit 42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 42 {
		t.Errorf("ExitCode = %d, want 42", result.ExitCode)
	}
}

func TestSandbox_PythonExecution(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not found")
	}
	sb := NewSandbox(5*time.Second, 1<<20, "")
	result, err := sb.Execute(context.Background(), "python", `print("hello from python")`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if strings.TrimSpace(result.Stdout) != "hello from python" {
		t.Errorf("Stdout = %q, want %q", result.Stdout, "hello from python")
	}
}

func TestSandbox_ContextCancellation(t *testing.T) {
	sb := NewSandbox(30*time.Second, 1<<20, "")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(1 * time.Second)
		cancel()
	}()

	result, err := sb.Execute(ctx, "shell", "sleep 30")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Duration >= 10*time.Second {
		t.Errorf("Duration = %v, should be well under 10s", result.Duration)
	}
}

func TestSandbox_ConcurrentExecution(t *testing.T) {
	sb := NewSandbox(5*time.Second, 1<<20, "")
	var wg sync.WaitGroup
	results := make([]*ExecuteResult, 3)
	errors := make([]error, 3)

	for i := 0; i < 3; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			results[idx], errors[idx] = sb.Execute(
				context.Background(),
				"shell",
				`echo "concurrent `+string(rune('A'+idx))+`"`,
			)
		}()
	}
	wg.Wait()

	for i, err := range errors {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}
	for i, r := range results {
		if r == nil {
			t.Errorf("goroutine %d: nil result", i)
			continue
		}
		if r.ExitCode != 0 {
			t.Errorf("goroutine %d: ExitCode = %d, want 0", i, r.ExitCode)
		}
	}
}

func TestFilePrelude_PythonInjectsPathAndContent(t *testing.T) {
	tmp := t.TempDir() + "/sample.txt"
	if err := writeFileForTest(tmp, "alpha\nbeta\n"); err != nil {
		t.Fatal(err)
	}

	prelude := FilePrelude("python", tmp)
	if prelude == "" {
		t.Fatal("python prelude should not be empty")
	}

	code := prelude + "print('len=' + str(len(FILE_CONTENT)))\nprint('first=' + FILE_CONTENT.split(chr(10))[0])\n"
	sb := NewSandbox(5*time.Second, 1<<20, "")
	r, err := sb.Execute(context.Background(), "python", code)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if r.ExitCode != 0 {
		t.Fatalf("exit %d, stderr=%s", r.ExitCode, r.Stderr)
	}
	if !strings.Contains(r.Stdout, "len=11") || !strings.Contains(r.Stdout, "first=alpha") {
		t.Errorf("expected len=11 and first=alpha, got: %q", r.Stdout)
	}
}

func TestFilePrelude_ShellExportsPath(t *testing.T) {
	tmp := t.TempDir() + "/shell.txt"
	if err := writeFileForTest(tmp, "shell-content\n"); err != nil {
		t.Fatal(err)
	}

	prelude := FilePrelude("shell", tmp)
	code := prelude + `echo "path=$FILE_PATH"; echo "content=$FILE_CONTENT"`
	sb := NewSandbox(5*time.Second, 1<<20, "")
	r, err := sb.Execute(context.Background(), "shell", code)
	if err != nil || r.ExitCode != 0 {
		t.Fatalf("exec failed: %v exit=%d stderr=%s", err, r.ExitCode, r.Stderr)
	}
	if !strings.Contains(r.Stdout, "path="+tmp) {
		t.Errorf("FILE_PATH not exported: %q", r.Stdout)
	}
	if !strings.Contains(r.Stdout, "content=shell-content") {
		t.Errorf("FILE_CONTENT not loaded: %q", r.Stdout)
	}
}

func TestSanitizedEnv_BlocksDenylistedVars(t *testing.T) {
	t.Setenv("LD_PRELOAD", "/should/be/stripped.so")
	t.Setenv("BASH_ENV", "/should/be/stripped.sh")
	t.Setenv("PYTHONSTARTUP", "/should/be/stripped.py")

	got := sanitizedEnv()
	for _, kv := range got {
		for _, blocked := range []string{"LD_PRELOAD=", "BASH_ENV=", "PYTHONSTARTUP="} {
			if strings.HasPrefix(kv, blocked) {
				t.Errorf("denylisted var leaked: %s", kv)
			}
		}
	}

	// Forced vars must be present.
	want := map[string]string{
		"PYTHONUNBUFFERED": "1",
		"NO_COLOR":         "1",
		"TERM":             "dumb",
	}
	for k, v := range want {
		if !contains(got, k+"="+v) {
			t.Errorf("missing forced env: %s=%s", k, v)
		}
	}
}

func writeFileForTest(path, content string) error {
	return exec.Command("sh", "-c", "printf '%s' "+shellQuoteForTest(content)+" > "+shellQuoteForTest(path)).Run()
}

func shellQuoteForTest(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func contains(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}
