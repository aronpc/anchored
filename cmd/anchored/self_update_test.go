package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jholhewres/anchored/pkg/updater"
)

func TestSelfUpdateExitCode(t *testing.T) {
	cases := []struct {
		name string
		res  updater.Result
		want int
	}{
		{
			name: "applicable update",
			res:  updater.Result{Current: "0.17.0", Latest: "0.18.0", Newer: true},
			want: exitUpdateAvailable,
		},
		{
			name: "already latest",
			res:  updater.Result{Current: "0.18.0", Latest: "0.18.0", Blocked: updater.BlockNotNewer},
			want: 0,
		},
		// A blocked check exits 0: nothing will happen without --force, so a
		// script polling for "there is work to do" must not be woken up by a
		// refusal it cannot act on.
		{
			name: "blocked on dev build even with a newer release out",
			res:  updater.Result{Current: "0.17.0-dev+gabc", Latest: "0.18.0", Newer: true, Blocked: updater.BlockDevBuild},
			want: 0,
		},
		{
			name: "blocked outside canonical dir",
			res:  updater.Result{Current: "0.17.0", Latest: "0.18.0", Newer: true, Blocked: updater.BlockOutsideCanonical},
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := selfUpdateExitCode(tc.res); got != tc.want {
				t.Fatalf("selfUpdateExitCode = %d, want %d", got, tc.want)
			}
		})
	}
}

// Every refusal must name itself and say what overrides it; a report that
// says "blocked" without the cause is the silent no-op this command exists
// to replace.
func TestRenderSelfUpdateCheck_ExplainsEveryBlockReason(t *testing.T) {
	cases := []struct {
		reason   updater.BlockReason
		mustHave []string
	}{
		{updater.BlockDevBuild, []string{"dev", "--force"}},
		{updater.BlockOutsideCanonical, []string{".anchored/bin", "--force"}},
		{updater.BlockEnvDisabled, []string{"ANCHORED_NO_AUTOUPDATE", "--force"}},
		{updater.BlockNotNewer, []string{"0.18.0"}},
		{updater.BlockNoVersion, []string{"ldflags"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.reason), func(t *testing.T) {
			out := renderSelfUpdateCheck(updater.Result{
				Current: "0.17.0-dev+gabc",
				Latest:  "0.18.0",
				BinPath: "/home/u/.anchored/bin/anchored",
				Blocked: tc.reason,
			})
			for _, want := range tc.mustHave {
				if !strings.Contains(out, want) {
					t.Errorf("report for %q missing %q\n---\n%s", tc.reason, want, out)
				}
			}
		})
	}
}

func TestRenderSelfUpdateCheck_ShowsVersionsAndPath(t *testing.T) {
	out := renderSelfUpdateCheck(updater.Result{
		Current: "0.17.0",
		Latest:  "0.18.0",
		BinPath: "/home/u/.anchored/bin/anchored",
		Newer:   true,
	})
	for _, want := range []string{"0.17.0", "0.18.0", "/home/u/.anchored/bin/anchored"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderSelfUpdateCheck_UnknownLatestWhenUnresolved(t *testing.T) {
	out := renderSelfUpdateCheck(updater.Result{
		Current: "0.17.0",
		BinPath: "/home/u/.anchored/bin/anchored",
		Blocked: updater.BlockEnvDisabled,
	})
	if strings.Contains(out, "→ \n") {
		t.Errorf("empty latest rendered as a dangling arrow\n---\n%s", out)
	}
}

func TestRenderSelfUpdateJSON(t *testing.T) {
	raw := renderSelfUpdateJSON(updater.Result{
		Current: "0.17.0-dev+gabc",
		Latest:  "0.18.0",
		BinPath: "/home/u/.anchored/bin/anchored",
		Newer:   true,
		Blocked: updater.BlockDevBuild,
	})

	var got struct {
		Current         string `json:"current"`
		Latest          string `json:"latest"`
		BinPath         string `json:"bin_path"`
		UpdateAvailable bool   `json:"update_available"`
		Blocked         string `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, raw)
	}
	if got.Current != "0.17.0-dev+gabc" || got.Latest != "0.18.0" {
		t.Errorf("versions = %q / %q", got.Current, got.Latest)
	}
	if got.BinPath != "/home/u/.anchored/bin/anchored" {
		t.Errorf("bin_path = %q", got.BinPath)
	}
	// update_available reports what the command would actually DO, so a
	// blocked result is false even though a newer release exists — that is
	// the distinction the exit code cannot carry.
	if got.UpdateAvailable {
		t.Error("update_available should be false when blocked")
	}
	if got.Blocked != string(updater.BlockDevBuild) {
		t.Errorf("blocked = %q, want %q", got.Blocked, updater.BlockDevBuild)
	}
}

func TestSelfUpdateCurrentVersion_StripsLeadingV(t *testing.T) {
	// Version comes from ldflags as "v0.17.0"; updater compares numerically
	// and Atoi("v0") yields 0, so the "v" has to come off before comparison.
	if got := selfUpdateCurrentVersion("v0.18.0"); got != "0.18.0" {
		t.Fatalf("got %q, want 0.18.0", got)
	}
	if got := selfUpdateCurrentVersion("0.18.0"); got != "0.18.0" {
		t.Fatalf("got %q, want 0.18.0", got)
	}
}

func TestSelfUpdateIsRegisteredInUsage(t *testing.T) {
	out := captureUsage(t)
	if !strings.Contains(out, "self-update") {
		t.Errorf("printUsage does not mention self-update:\n%s", out)
	}
	// `update` is memory update; the two are one keystroke apart and the
	// usage text is the only place a user learns which is which.
	if !strings.Contains(out, "Update a memory") {
		t.Errorf("printUsage lost the memory-update line:\n%s", out)
	}
}

// captureUsage collects what printUsage writes to stderr.
func captureUsage(t *testing.T) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	printUsage()
	os.Stderr = orig
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
