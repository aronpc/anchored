package memory

import (
	"strings"
	"testing"

	"github.com/jholhewres/anchored/pkg/config"
)

// TestSanitizePlaceholderNamesSurvive locks the placeholder guard: ENV_VAR-style
// names that merely REFERENCE a secret must not be redacted, while real
// credential-shaped values still are.
func TestSanitizePlaceholderNamesSurvive(t *testing.T) {
	s := NewSanitizer(config.SanitizerConfig{Enabled: true})

	kept := []string{
		"the webhook authenticates with Bearer CREDITS_WEBHOOK_BEARER from vault",
		"set token=MY_SERVICE_API_TOKEN in the environment",
		"api_key: PAYMENTS_PROVIDER_API_KEY rotates monthly",
	}
	for _, in := range kept {
		if got := s.Sanitize(in); got != in {
			t.Errorf("placeholder redacted:\n in:  %s\n out: %s", in, got)
		}
	}

	redacted := []string{
		"Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9abcdef",
		"token=a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6",
		"password=hunter2hunter2",
	}
	for _, in := range redacted {
		if got := s.Sanitize(in); !strings.Contains(got, "[REDACTED]") {
			t.Errorf("real secret survived:\n in:  %s\n out: %s", in, got)
		}
	}
}
