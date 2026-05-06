package main

import (
	"io"
	"os"
)

// runHookUserPromptSubmit re-injects the anchored routing block on every user
// prompt so the model is reminded to call anchored_search/anchored_kg_query
// before answering — even after long sessions where the SessionStart reminder
// has been compacted away. The Claude Code contract here is the same shape as
// SessionStart but with hookEventName="UserPromptSubmit".
func runHookUserPromptSubmit(args []string) {
	fs := newFlagSet("hook userpromptsubmit")
	fs.Parse(args)

	// Drain stdin so Claude Code doesn't observe a closed pipe; we don't act
	// on the prompt text yet, but reading prevents EPIPE noise.
	body, _ := io.ReadAll(os.Stdin)
	_ = body

	// We could parse `body` here to gate injection on memory triggers ("do you
	// remember", "we decided", etc.). For now the routing block is small
	// enough that re-injecting unconditionally is the safer default — context-
	// mode does the same with its own block.

	outputJSON(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "UserPromptSubmit",
			"additionalContext": AnchoredRoutingBlock,
		},
	})
}

