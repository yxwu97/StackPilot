package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer

	exitCode := run(context.Background(), []string{"version"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("run(version) exit code = %d, want 0", exitCode)
	}
	if !strings.Contains(stdout.String(), "StackPilot dev") {
		t.Fatalf("run(version) stdout = %q, want development version", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("run(version) stderr = %q, want empty", stderr.String())
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
