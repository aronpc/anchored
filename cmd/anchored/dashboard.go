package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// dashboardAssets embeds the SPA (HTML/JS/CSS + vendored Chart.js and
// vis-network) so the binary is fully self-contained and works offline.
//
//go:embed dashboard/assets
var dashboardAssets embed.FS

// runDashboard starts a local-only HTTP server that visualizes the anchored
// SQLite store. It is read-mostly; the only write path is soft-deleting a
// single memory (same semantics as `anchored forget`).
//
// Sub-actions: `anchored dashboard install|uninstall|status` manage a systemd
// user service so the dashboard stays up across reboots without a terminal.
func runDashboard(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "install", "enable":
			installDashboardService()
			return
		case "uninstall", "remove", "disable":
			uninstallDashboardService()
			return
		case "status":
			statusDashboardService()
			return
		}
	}

	flagSet := newFlagSet("dashboard")
	configPath := flagSet.String("config", "", "path to config file")
	addr := flagSet.String("addr", "127.0.0.1:17777", "listen address (host:port)")
	noOpen := flagSet.Bool("no-open", false, "do not open the browser automatically")
	flagSet.Parse(args)

	_, logger, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	api := &dashboardAPI{svc: svc, db: svc.StoreDB(), logger: logger}

	assets, err := fs.Sub(dashboardAssets, "dashboard/assets")
	if err != nil {
		slog.Error("dashboard assets", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", api.routes())                 // JSON endpoints
	mux.Handle("/", http.FileServer(http.FS(assets))) // SPA shell + static assets

	srv := &http.Server{
		Handler:           cacheControl(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		slog.Error("listen", "addr", *addr, "error", err)
		os.Exit(1)
	}
	url := "http://" + listener.Addr().String()
	fmt.Fprintf(os.Stderr, "anchored dashboard → %s  (Ctrl+C to stop)\n", url)

	srvDone := make(chan struct{})
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.Error("serve", "error", err)
		}
		close(srvDone)
	}()

	if !*noOpen {
		go openBrowser(url)
	}

	// Block until interrupted, then shut down cleanly so WAL checkpoints and
	// in-flight writes settle before the process exits.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	fmt.Fprintf(os.Stderr, "shutting down…\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	<-srvDone
}

// cacheControl sets cache headers so the browser always revalidates app assets
// (index.html / app.js / styles.css change with every binary rebuild) while
// still caching the large vendored libs for an hour. Without this, browsers
// heuristically cache a stale app.js and ignore dashboard updates.
func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/vendor/") {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		} else {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

// openBrowser launches the user's default browser. Best-effort: failure is
// silent (the URL is already printed, so the user can open it manually).
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default: // linux, *bsd
		cmd = exec.Command("xdg-open", url)
	}
	if cmd == nil {
		return
	}
	_ = cmd.Start()
}

// --- systemd --user service management (Linux only; best-effort) ---

const dashboardUnitName = "anchored-dashboard"

func dashboardUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", dashboardUnitName+".service"), nil
}

func dashboardUnit(exe string) string {
	return fmt.Sprintf(`[Unit]
Description=anchored dashboard (local memory viewer)
After=network.target

[Service]
Type=simple
ExecStart=%s dashboard --no-open --addr 127.0.0.1:17777
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, exe)
}

// runCmd runs a command streaming output to stderr. Failure is non-fatal —
// service management must degrade gracefully across distros/environments.
func runCmd(name string, args ...string) bool {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  (warning) %s: %v\n", name, err)
		return false
	}
	return true
}

// installDashboardService writes a systemd --user unit pointing at the current
// binary, enables lingering (so it survives logout / starts at boot), and
// enables+starts the service.
func installDashboardService() {
	exe, err := os.Executable()
	if err != nil {
		slog.Error("locate executable", "error", err)
		os.Exit(1)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	unitPath, err := dashboardUnitPath()
	if err != nil {
		slog.Error("resolve unit path", "error", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		slog.Error("create unit dir", "error", err)
		os.Exit(1)
	}
	if err := os.WriteFile(unitPath, []byte(dashboardUnit(exe)), 0o644); err != nil {
		slog.Error("write unit", "error", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "unit written: %s\n", unitPath)

	// Lingering lets the user service run after logout and start at boot;
	// harmless if already enabled or if loginctl is missing.
	if user := os.Getenv("USER"); user != "" {
		runCmd("loginctl", "enable-linger", user)
	}
	runCmd("systemctl", "--user", "daemon-reload")
	runCmd("systemctl", "--user", "enable", "--now", dashboardUnitName)

	fmt.Fprintf(os.Stderr, "\nanchored dashboard service enabled.\n")
	fmt.Fprintf(os.Stderr, "  URL:     http://127.0.0.1:17777\n")
	fmt.Fprintf(os.Stderr, "  logs:    journalctl --user -u %s -f\n", dashboardUnitName)
	fmt.Fprintf(os.Stderr, "  manage:  systemctl --user {status|stop|restart|disable} %s\n", dashboardUnitName)
	fmt.Fprintf(os.Stderr, "  remove:  anchored dashboard uninstall\n")
}

// uninstallDashboardService stops+disables the service and removes the unit.
func uninstallDashboardService() {
	runCmd("systemctl", "--user", "disable", "--now", dashboardUnitName)
	if unitPath, err := dashboardUnitPath(); err == nil {
		_ = os.Remove(unitPath)
	}
	runCmd("systemctl", "--user", "daemon-reload")
	fmt.Fprintf(os.Stderr, "anchored dashboard service removed.\n")
}

// statusDashboardService proxies to `systemctl --user status` (exit code
// preserved so it composes in scripts).
func statusDashboardService() {
	cmd := exec.Command("systemctl", "--user", "status", dashboardUnitName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}
