package updater

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// capturingHandler collects slog records so a test can assert on the exact
// line Run emitted, not merely that it returned.
type capturingHandler struct {
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}
func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

func (h *capturingHandler) messages() string {
	var b strings.Builder
	for _, r := range h.records {
		b.WriteString(r.Level.String() + " " + r.Message + "\n")
	}
	return b.String()
}

// Run is started from a goroutine on every MCP server launch, so its
// short-circuit behaviour is the highest-traffic path in the package. Two
// properties matter per case: it says why it stopped, and a locally-refused
// update never touches the network.
func TestRun_RefusalsAreLoggedAndStayOffline(t *testing.T) {
	cases := []struct {
		name        string
		version     string
		binPath     func(t *testing.T) string
		env         map[string]string
		wantMessage string
	}{
		{
			name:        "dev build",
			version:     "0.17.0-dev+gabc",
			binPath:     canonicalBin,
			wantMessage: "autoupdate: dev build, self-update disabled",
		},
		{
			name:        "outside canonical dir",
			version:     "0.17.0",
			binPath:     func(t *testing.T) string { t.Setenv("HOME", t.TempDir()); return "/usr/local/bin/anchored" },
			wantMessage: "autoupdate: skip, binary outside canonical dir",
		},
		{
			name:        "env kill switch",
			version:     "0.17.0",
			binPath:     canonicalBin,
			env:         map[string]string{"ANCHORED_NO_AUTOUPDATE": "1"},
			wantMessage: "",
		},
		{
			name:        "no version",
			version:     "",
			binPath:     canonicalBin,
			wantMessage: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			bin := tc.binPath(t)

			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
			}))
			defer srv.Close()
			orig := releaseAPIURL
			releaseAPIURL = srv.URL + "?repo=%s"
			defer func() { releaseAPIURL = orig }()

			h := &capturingHandler{}
			Run(context.Background(), Options{
				CurrentVersion: tc.version,
				BinPath:        bin,
				Logger:         slog.New(h),
			})

			if got := requests.Load(); got != 0 {
				t.Errorf("a local guard must refuse before any request, got %d", got)
			}
			if tc.wantMessage == "" {
				if len(h.records) != 0 {
					t.Errorf("expected silence, got:\n%s", h.messages())
				}
				return
			}
			if !strings.Contains(h.messages(), tc.wantMessage) {
				t.Errorf("missing %q, got:\n%s", tc.wantMessage, h.messages())
			}
		})
	}
}

// The guard that motivated inverting the switch to an allowlist: a reason the
// code does not recognize must still stop the unattended path.
func TestRun_UnknownBlockReasonStillRefuses(t *testing.T) {
	h := &capturingHandler{}
	logBlockedUpdate(slog.New(h), Result{Blocked: BlockReason("some-future-guard")})
	msgs := h.messages()
	if !strings.Contains(msgs, "autoupdate: refused") {
		t.Errorf("an unknown reason must be logged as a refusal, got:\n%s", msgs)
	}
	if !strings.Contains(strings.ToLower(msgs), "warn") {
		t.Errorf("an unrecognized guard deserves WARN, not Debug, got:\n%s", msgs)
	}
}

func TestRun_AlreadyOnLatestIsLoggedAfterResolving(t *testing.T) {
	fakeRelease(t, "0.18.0")
	h := &capturingHandler{}
	Run(context.Background(), Options{
		CurrentVersion: "0.18.0",
		BinPath:        filepath.Join(canonicalBin(t)),
		Logger:         slog.New(h),
	})
	if !strings.Contains(h.messages(), "autoupdate: already on latest") {
		t.Errorf("got:\n%s", h.messages())
	}
}
