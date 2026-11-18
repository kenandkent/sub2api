package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSupervisorWaitsForReadiness(t *testing.T) {
	var ready atomic.Bool
	checked := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case checked <- struct{}{}:
		default:
		}
		if !ready.Load() || r.URL.Path != "/health/ready" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	s := testSupervisor(t, dir, server.URL)
	done, stop := startTestSupervisor(t, s)
	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not check readiness")
	}
	if _, err := os.Stat(filepath.Join(dir, "app-started")); !os.IsNotExist(err) {
		t.Fatalf("app started before worker readiness, stat error: %v", err)
	}
	ready.Store(true)
	waitForFile(t, filepath.Join(dir, "app-started"))
	stop()
	if err := waitForExit(t, done); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, filepath.Join(dir, "app-stopped"))
	waitForFile(t, filepath.Join(dir, "worker-stopped"))
}

func TestSupervisorRestartsOnlyTheWorkerAndEnforcesRestartLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()

	dir := t.TempDir()
	s := testSupervisor(t, dir, server.URL)
	s.workerCommand = func() *exec.Cmd {
		return exec.Command("/bin/sh", "-c", `echo started >> "$1/worker-starts"; sleep 0.1; exit 7`, "worker", dir)
	}
	done, _ := startTestSupervisor(t, s)
	err := waitForExit(t, done)
	if err == nil || !strings.Contains(err.Error(), "restart limit") {
		t.Fatalf("expected restart-limit failure, got %v", err)
	}
	if got := strings.Count(waitForFile(t, filepath.Join(dir, "worker-starts")), "started"); got != 2 {
		t.Fatalf("expected original worker and one restart, got %d", got)
	}
	if got := strings.Count(waitForFile(t, filepath.Join(dir, "app-started")), "started"); got != 1 {
		t.Fatalf("expected to keep original app through worker restart, got %d app starts", got)
	}
	waitForFile(t, filepath.Join(dir, "app-stopped"))
}

func TestSupervisorStopsWorkerWhenAppExits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()

	dir := t.TempDir()
	s := testSupervisor(t, dir, server.URL)
	s.appCommand = exec.Command("/bin/sh", "-c", `until test -e "$1/worker-started"; do sleep 0.02; done; exit 7`, "app", dir)
	done, _ := startTestSupervisor(t, s)
	err := waitForExit(t, done)
	if err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("expected app exit failure, got %v", err)
	}
	waitForFile(t, filepath.Join(dir, "worker-stopped"))
}

func TestSupervisorKeepsWorkerRunningUntilAppDrains(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()

	dir := t.TempDir()
	s := testSupervisor(t, dir, server.URL)
	s.appCommand = exec.Command("/bin/sh", "-c", `
		trap 'sleep 0.2; echo app-drained >> "$1/shutdown-order"; exit 0' TERM
		echo started > "$1/app-started"
		while :; do sleep 0.02; done
	`, "app", dir)
	s.workerCommand = func() *exec.Cmd {
		return exec.Command("/bin/sh", "-c", `
			trap 'echo worker-stopped >> "$1/shutdown-order"; exit 0' TERM
			echo started > "$1/worker-started"
			while :; do sleep 0.02; done
		`, "worker", dir)
	}
	done, stop := startTestSupervisor(t, s)
	waitForFile(t, filepath.Join(dir, "app-started"))
	waitForFile(t, filepath.Join(dir, "worker-started"))
	stop()
	if err := waitForExit(t, done); err != nil {
		t.Fatal(err)
	}
	if order := waitForFile(t, filepath.Join(dir, "shutdown-order")); order != "app-drained\nworker-stopped\n" {
		t.Fatalf("worker stopped before application drained; shutdown events: %q", order)
	}
}

func TestWorkerEnvironmentKeepsOnlyWorkerSettings(t *testing.T) {
	t.Setenv("OPENCODE_FREE_BUN_TOKEN", "internal-test-token")
	t.Setenv("DATABASE_PASSWORD", "db-test-secret")
	t.Setenv("BUN_OPTIONS", "unwanted-runtime-option")
	t.Setenv("LISTEN_HOST", "0.0.0.0")
	t.Setenv("PORT", "12345")
	env := strings.Join(workerEnvironment(), "\n")
	for _, setting := range []string{"OPENCODE_FREE_BUN_TOKEN=internal-test-token", "LISTEN_HOST=127.0.0.1", "PORT=19090", "BUN_RUNTIME_TRANSPILER_CACHE_PATH=0"} {
		if !strings.Contains(env, setting) {
			t.Errorf("worker environment missed %s", setting)
		}
	}
	for _, setting := range []string{"DATABASE_PASSWORD=", "BUN_OPTIONS=", "LISTEN_HOST=0.0.0.0", "PORT=12345"} {
		if strings.Contains(env, setting) {
			t.Errorf("worker inherited %s", setting)
		}
	}
}

func testSupervisor(t *testing.T, dir string, url string) supervisor {
	t.Helper()
	child := func(name string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", `
			trap 'echo stopped >> "$1/$2-stopped"; exit 0' TERM
			echo started >> "$1/$2-started"
			while :; do sleep 0.02; done
		`, name, dir, name)
	}
	return supervisor{
		appCommand:            child("app"),
		workerCommand:         func() *exec.Cmd { return child("worker") },
		readinessURL:          url + "/health/ready",
		readinessTimeout:      3 * time.Second,
		retryDelay:            10 * time.Millisecond,
		retryWindow:           time.Minute,
		restartLimit:          1,
		appShutdownTimeout:    2 * time.Second,
		workerShutdownTimeout: 2 * time.Second,
	}
}

func startTestSupervisor(t *testing.T, s supervisor) (<-chan error, context.CancelFunc) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	done := make(chan error, 1)
	go func() { done <- s.run(ctx) }()
	return done, stop
}

func waitForFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if contents, _ := os.ReadFile(path); len(contents) > 0 {
			return string(contents)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no contents written to %s", path)
	return ""
}

func waitForExit(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not exit")
		return nil
	}
}
