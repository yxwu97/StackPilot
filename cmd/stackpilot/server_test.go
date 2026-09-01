package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stackpilot/internal/events"
	"stackpilot/internal/storage"
)

func TestParseServerConfig(t *testing.T) {
	var output bytes.Buffer
	dataDir := t.TempDir()
	config, err := parseServerConfig([]string{"--port", "32123", "--data-dir", dataDir}, &output)
	if err != nil {
		t.Fatalf("parseServerConfig() error = %v", err)
	}
	if config.port != 32123 {
		t.Fatalf("port = %d, want 32123", config.port)
	}
	if config.dataDir != filepath.Clean(dataDir) {
		t.Fatalf("dataDir = %q, want %q", config.dataDir, filepath.Clean(dataDir))
	}
}

func TestWaitForServerShutsDownAfterCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	server := newHTTPServer(listener.Addr().String(), http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, "ready")
	}))
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()

	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatalf("GET running server error = %v", err)
	}
	_ = response.Body.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var logs bytes.Buffer
	if exitCode := waitForServer(ctx, server, serveErrors, newLogger(&logs)); exitCode != 0 {
		t.Fatalf("waitForServer() exit code = %d, want 0; logs = %s", exitCode, logs.String())
	}
	if !strings.Contains(logs.String(), `"http server stopped"`) || !strings.Contains(logs.String(), `"reason":"context_cancelled"`) {
		t.Fatalf("shutdown logs = %q, want context-cancelled stop event", logs.String())
	}
}

func TestRunServerCancelsWorkersWhenListenerCreationFails(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listener error = %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dataDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runServer(ctx, []string{"--port", fmt.Sprint(port), "--data-dir", dataDir}, &stdout, &stderr)
	}()
	select {
	case exitCode := <-done:
		if exitCode != 1 || !strings.Contains(stderr.String(), `"error_code":"HTTP_LISTENER_CREATE_FAILED"`) {
			t.Fatalf("runServer(listener conflict) = %d, logs = %s", exitCode, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runServer waited for background workers after listener creation failed")
	}
}

func TestShutdownControlPlaneCancelsActiveMetricSampler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dataDir := t.TempDir()
	var logs bytes.Buffer
	logger := newLogger(&logs)
	plane, err := newControlPlane(ctx, serverConfig{dataDir: dataDir}, logger)
	if err != nil {
		cancel()
		t.Fatalf("newControlPlane() error = %v", err)
	}
	dependencies, err := newOrchestrationDependencies(plane.database, events.NewBroker(1), dataDir)
	if err != nil {
		exitCode := 0
		shutdownControlPlane(cancel, plane, logger, &exitCode)
		t.Fatalf("newOrchestrationDependencies() error = %v", err)
	}
	repository, err := storage.NewMetricRepository(plane.database)
	if err != nil {
		exitCode := 0
		shutdownControlPlane(cancel, plane, logger, &exitCode)
		t.Fatalf("NewMetricRepository() error = %v", err)
	}
	plane.metricSampler, err = newMetricSampler(repository, dependencies, logger)
	if err != nil {
		exitCode := 0
		shutdownControlPlane(cancel, plane, logger, &exitCode)
		t.Fatalf("newMetricSampler() error = %v", err)
	}
	if err := plane.metricSampler.Start(ctx); err != nil {
		exitCode := 0
		shutdownControlPlane(cancel, plane, logger, &exitCode)
		t.Fatalf("metric sampler Start() error = %v", err)
	}
	exitCode := 0
	done := make(chan struct{})
	go func() {
		shutdownControlPlane(cancel, plane, logger, &exitCode)
		close(done)
	}()
	select {
	case <-done:
		if exitCode != 0 {
			t.Fatalf("shutdown exit code = %d, logs = %s", exitCode, logs.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown waited on the active metric sampler before canceling its context")
	}
}

func TestLoggerUsesUTC(t *testing.T) {
	var output bytes.Buffer
	newLogger(&output).LogAttrs(context.Background(), slog.LevelInfo, "test")
	if !strings.Contains(output.String(), "Z\"") {
		t.Fatalf("log timestamp is not UTC: %s", output.String())
	}
}

func TestParseServerConfigRejectsInvalidPortAndArguments(t *testing.T) {
	dataDir := t.TempDir()
	for _, args := range [][]string{{"--port", "0", "--data-dir", dataDir}, {"--port", "65536", "--data-dir", dataDir}, {"--data-dir", dataDir, "unexpected"}} {
		if _, err := parseServerConfig(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("parseServerConfig(%q) error = nil, want validation error", args)
		}
	}
}

func TestOrchestrationAssemblyDoesNotRequireDocker(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := exec.LookPath("docker.exe"); err == nil {
		t.Fatal("docker.exe unexpectedly remained available in the isolated PATH")
	}
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := storage.OpenDataDir(ctx, dataDir)
	if err != nil {
		t.Fatalf("OpenDataDir() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := newOrchestrationDependencies(database, events.NewBroker(1), dataDir); err != nil {
		t.Fatalf("non-Compose orchestration assembly required Docker: %v", err)
	}
}
