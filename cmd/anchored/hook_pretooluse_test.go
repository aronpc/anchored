package main

import "testing"

func TestMemoryFileSecretBlock(t *testing.T) {
	// A synthetic AWS-style access key id — never a real credential.
	const secret = "AKIA1234567890ABCDEF"

	cases := []struct {
		name      string
		tool      string
		args      map[string]any
		wantBlock bool
	}{
		{
			name:      "secret into CLAUDE.md blocked",
			tool:      "Write",
			args:      map[string]any{"file_path": "/repo/CLAUDE.md", "content": "deploy key is " + secret},
			wantBlock: true,
		},
		{
			name:      "secret into AGENTS.md via edit blocked",
			tool:      "Edit",
			args:      map[string]any{"file_path": "/repo/AGENTS.md", "new_string": "token " + secret},
			wantBlock: true,
		},
		{
			name:      "secret into .cursor/rules blocked",
			tool:      "Write",
			args:      map[string]any{"file_path": "/repo/.cursor/rules/main.mdc", "content": secret},
			wantBlock: true,
		},
		{
			name:      "clean content into CLAUDE.md allowed",
			tool:      "Write",
			args:      map[string]any{"file_path": "/repo/CLAUDE.md", "content": "We use Go 1.22 and pnpm."},
			wantBlock: false,
		},
		{
			name:      "secret into a normal source file is not this hook's job",
			tool:      "Write",
			args:      map[string]any{"file_path": "/repo/main.go", "content": "k := \"" + secret + "\""},
			wantBlock: false,
		},
		{
			name:      "non-write tool ignored",
			tool:      "Bash",
			args:      map[string]any{"command": "echo " + secret + " >> CLAUDE.md"},
			wantBlock: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			blocked, reason := memoryFileSecretBlock(c.tool, c.args)
			if blocked != c.wantBlock {
				t.Fatalf("memoryFileSecretBlock(%q) blocked = %v, want %v (reason %q)", c.tool, blocked, c.wantBlock, reason)
			}
			if blocked && reason == "" {
				t.Error("a block must carry an actionable reason")
			}
			if blocked && !contains(reason, "anchored_save") {
				t.Errorf("block reason should point to anchored_save: %q", reason)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
