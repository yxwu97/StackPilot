package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestReadSecretValueUsesStdinAndTrimsOneLineEnding(t *testing.T) {
	value, err := readSecretValue(strings.NewReader("safe-value\r\n"), &bytes.Buffer{})
	if err != nil || string(value) != "safe-value" {
		t.Fatalf("readSecretValue() = (%q, %v)", value, err)
	}
	erase(value)
}

func TestSecretSetRejectsPlaintextPositionalArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	run := runner{ctx: context.Background(), stdin: strings.NewReader("stdin-value"), stdout: &stdout, stderr: &stderr}
	code := run.secret([]string{"set", "aiws", "database-password", "plaintext-argument"})
	if code != 2 || stdout.Len() != 0 {
		t.Fatalf("secret set with plaintext argument = (code %d, stdout %q, stderr %q)", code, stdout.String(), stderr.String())
	}
}

func TestReadSecretValueRejectsEmptyAndOversizedInput(t *testing.T) {
	for _, input := range []string{"\n", strings.Repeat("x", 64*1024+1)} {
		value, err := readSecretValue(strings.NewReader(input), &bytes.Buffer{})
		if err == nil || value != nil {
			t.Fatalf("readSecretValue(%d bytes) = (%d bytes, %v)", len(input), len(value), err)
		}
	}
}
