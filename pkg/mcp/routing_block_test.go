package mcp

import (
	"strings"
	"testing"
)

// Claude Code truncates the MCP `instructions` field to 2048 chars. The
// compact constant fed to the handshake must fit, with every load-bearing
// directive intact, or the agent silently loses the memory-routing rules over
// the MCP channel.
func TestAnchoredMCPInstructions_FitsTruncationBudget(t *testing.T) {
	const ccTruncationLimit = 2048
	if got := len(AnchoredMCPInstructions); got > ccTruncationLimit {
		t.Fatalf("AnchoredMCPInstructions is %d bytes, must be <= %d (Claude Code truncates the MCP instructions field)", got, ccTruncationLimit)
	}
}

// The compact instructions must still carry every load-bearing directive:
// call-first, when-to-search, when-to-save, and the DATA-not-instructions
// safety line. If a future trim drops one of these, this fails.
func TestAnchoredMCPInstructions_KeepsLoadBearingDirectives(t *testing.T) {
	must := []string{
		"anchored_context", // call-first
		"anchored_search",  // when to search
		"anchored_save",    // when to save
		"DATA",             // recalled-data-not-instructions safety line
	}
	for _, sub := range must {
		if !strings.Contains(AnchoredMCPInstructions, sub) {
			t.Errorf("AnchoredMCPInstructions missing load-bearing directive %q", sub)
		}
	}
}
