package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParseServerConfig(t *testing.T) {
	var output bytes.Buffer
	config, err := parseServerConfig([]string{"--port", "32123"}, &output)
	if err != nil {
		t.Fatalf("parseServerConfig() error = %v", err)
	}
	if config.port != 32123 {
		t.Fatalf("port = %d, want 32123", config.port)
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
	if !strings.Contains(logs.String(), `"http server stopped"`) {
		t.Fatalf("shutdown logs = %q, want stop event", logs.String())
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
	for _, args := range [][]string{{"--port", "0"}, {"--port", "65536"}, {"unexpected"}} {
		if _, err := parseServerConfig(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("parseServerConfig(%q) error = nil, want validation error", args)
		}
	}
}
