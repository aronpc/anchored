package main

import (
	"strings"
	"testing"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/memory"
	"github.com/jholhewres/anchored/pkg/sync"
)

// TestInvariant_SecretRedactedBeforePush locks invariant (5) end to end: a
// synthetic credential is neutralized by the sanitizer at save time, and the
// remote safety filter independently blocks raw secrets that slipped past it.
// Both layers must hold on their own.
func TestInvariant_SecretRedactedBeforePush(t *testing.T) {
	const secret = "AKIA1234567890ABCDEF" // synthetic AWS-style access key id

	// Layer 1: sanitizer redacts at save time (opt-in; the always-on layer is
	// the sync filter below — both must hold independently when enabled).
	cfg := config.Defaults()
	cfg.Sanitizer.Enabled = true
	san := memory.NewSanitizer(cfg.Sanitizer)
	sanitized := san.Sanitize("aws key is " + secret + " for the deploy")
	if strings.Contains(sanitized, secret) {
		t.Fatalf("sanitizer left the secret intact: %q", sanitized)
	}

	// Layer 2: even an unsanitized memory is blocked by the sync filter.
	preview := sync.ClassifyForPreview([]sync.Memory{
		{ID: "s1", Category: "decision", Content: "aws key is " + secret},
	}, t.TempDir())
	if len(preview.Items) != 1 {
		t.Fatalf("preview items: %d", len(preview.Items))
	}
	if preview.Items[0].Classification == sync.ClassificationSyncable {
		t.Fatal("raw secret must never classify as syncable")
	}

	// The sanitized form (placeholder, no key material) is allowed through —
	// redaction, not memory loss, is the goal.
	preview = sync.ClassifyForPreview([]sync.Memory{
		{ID: "s2", Category: "decision", Content: sanitized},
	}, t.TempDir())
	if preview.Items[0].Classification != sync.ClassificationSyncable {
		t.Fatalf("sanitized content should be syncable, got %v (reason %q)",
			preview.Items[0].Classification, preview.Items[0].Reason)
	}
}
