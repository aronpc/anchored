package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jholhewres/anchored/pkg/config"
)

func probeEntry(url, key string) config.RemoteEntry {
	return config.RemoteEntry{Name: "test", ServerURL: url, APIKey: key}
}

func TestProbeRemote_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"service":"anchored-oss","version":"0.5.0","status":"ok"}`))
		case "/v1/me":
			if r.Header.Get("Authorization") != "Bearer good-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := probeRemote(context.Background(), probeEntry(srv.URL, "good-key"))
	if p.Class != "ok" {
		t.Fatalf("class = %q, want ok", p.Class)
	}
	if p.Version != "0.5.0" {
		t.Fatalf("version = %q, want 0.5.0", p.Version)
	}
	if p.Latency <= 0 {
		t.Fatalf("latency not measured: %v", p.Latency)
	}
}

func TestProbeRemote_AuthRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			w.Write([]byte(`{"version":"0.5.0"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	p := probeRemote(context.Background(), probeEntry(srv.URL, "bad-key"))
	if p.Class != "auth" {
		t.Fatalf("class = %q, want auth", p.Class)
	}
}

func TestProbeRemote_Timeout(t *testing.T) {
	old := probeTimeout
	probeTimeout = 150 * time.Millisecond
	defer func() { probeTimeout = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
	}))
	defer srv.Close()

	p := probeRemote(context.Background(), probeEntry(srv.URL, "k"))
	if p.Class != "timeout" {
		t.Fatalf("class = %q, want timeout", p.Class)
	}
}

func TestProbeRemote_TLSUntrusted(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// Default client does not trust httptest's self-signed CA.
	p := probeRemote(context.Background(), probeEntry(srv.URL, "k"))
	if p.Class != "tls" {
		t.Fatalf("class = %q, want tls", p.Class)
	}
}

func TestProbeRemote_DNSFailure(t *testing.T) {
	p := probeRemote(context.Background(), probeEntry("http://nonexistent-host-zz.invalid", "k"))
	// Some resolvers wrap NXDOMAIN differently; dns is expected, unreachable
	// acceptable only if the platform resolver hides the DNS error type.
	if p.Class != "dns" {
		t.Fatalf("class = %q, want dns", p.Class)
	}
}

func TestKeyPrefix_NeverFullKey(t *testing.T) {
	full := "anc_live_0123456789abcdef0123456789abcdef"
	got := keyPrefix(full)
	if strings.Contains(got, full[8:16]) {
		t.Fatalf("prefix leaked key body: %q", got)
	}
	if len([]rune(got)) > 9 { // 8 chars + ellipsis
		t.Fatalf("prefix too long: %q", got)
	}
	// Short keys are fully masked — never printed verbatim.
	if got := keyPrefix("short"); got != "*****" {
		t.Fatalf("short key not masked: %q", got)
	}
}

func TestSanitizeURL_StripsUserinfo(t *testing.T) {
	got := sanitizeURL("https://user:secretpass@host.example.com:8443/base")
	if strings.Contains(got, "secretpass") || strings.Contains(got, "user") {
		t.Fatalf("userinfo leaked: %q", got)
	}
	if !strings.Contains(got, "host.example.com:8443") {
		t.Fatalf("host lost: %q", got)
	}
	if sanitizeURL("https://plain.example.com") != "https://plain.example.com" {
		t.Fatal("plain URL must pass through")
	}
}

func TestDoctorJSONShape(t *testing.T) {
	doctorChecks = nil
	doctorJSONMode = true // suppress prints; exercise the REAL collector
	defer func() { doctorChecks = nil; doctorJSONMode = false }()

	recordCheck("ok", "binary", "v0.6.14", "", false)
	recordCheck("failed", "remote \"team\"", "connectivity failed: dns", "check DNS", true)
	recordCheck("skipped", "project identity", "offline", "", false)

	out := struct {
		Version string        `json:"version"`
		Checks  []checkResult `json:"checks"`
	}{Version: "test", Checks: doctorChecks}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed struct {
		Version string `json:"version"`
		Checks  []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Detail     string `json:"detail"`
			FixCommand string `json:"fix_command"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Checks) != 3 {
		t.Fatalf("checks = %d, want 3", len(parsed.Checks))
	}
	if parsed.Checks[1].Status != "failed" || parsed.Checks[1].FixCommand != "check DNS" {
		t.Fatalf("failed check shape: %+v", parsed.Checks[1])
	}
	if parsed.Checks[2].Status != "skipped" {
		t.Fatalf("skipped check shape: %+v", parsed.Checks[2])
	}
	doctorChecks = nil
}

func TestRemoteConfigSanity_DefaultDetection(t *testing.T) {
	doctorChecks = nil
	doctorJSONMode = true // suppress prints
	defer func() { doctorChecks = nil; doctorJSONMode = false }()

	cfg := &config.Config{Remotes: map[string]config.RemoteEntry{
		"a": {Name: "a", ServerURL: "https://a.example.com", APIKey: "k"},
		"b": {Name: "b", ServerURL: "https://b.example.com", APIKey: "k"},
	}}
	checkRemoteConfigSanity(cfg)

	var noDefault bool
	for _, c := range doctorChecks {
		if c.Name == "default remote" && c.Status == "failed" &&
			strings.Contains(c.Detail, "no remote has default") {
			noDefault = true
		}
	}
	if !noDefault {
		t.Fatalf("missing no-default finding: %+v", doctorChecks)
	}
}
