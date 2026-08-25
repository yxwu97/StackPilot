package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"stackpilot/internal/buildinfo"
)

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer

	exitCode := run(context.Background(), []string{"version"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("run(version) exit code = %d, want 0", exitCode)
	}
	if !strings.Contains(stdout.String(), "StackPilot 0.0.0") {
		t.Fatalf("run(version) stdout = %q, want development version", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("run(version) stderr = %q, want empty", stderr.String())
	}
}

func TestWriteVersionUsesCompiledBuildInfo(t *testing.T) {
	originalVersion, originalCommit, originalBuildTime := buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime
	t.Cleanup(func() {
		buildinfo.Version, buildinfo.Commit, buildinfo.BuildTime = originalVersion, originalCommit, originalBuildTime
	})
	buildinfo.Version = "0.1.0"
	buildinfo.Commit = "abc123"
	buildinfo.BuildTime = "2026-08-19T12:00:00Z"

	var output bytes.Buffer
	writeVersion(&output)
	want := "StackPilot 0.1.0\ncommit: abc123\nbuilt: 2026-08-19T12:00:00Z\n"
	if output.String() != want {
		t.Fatalf("writeVersion() = %q, want %q", output.String(), want)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	exitCode := run(context.Background(), []string{"invalid"}, &stdout, &stderr)

	if exitCode != 2 {
		t.Fatalf("run(invalid) exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("run(invalid) stderr = %q, want command error", stderr.String())
	}
}
