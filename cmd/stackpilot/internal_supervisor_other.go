//go:build !windows

package main

import (
	"context"
	"fmt"
	"io"
)

func runInternalSupervisor(_ context.Context, _ []string, stderr io.Writer) int {
	_, _ = fmt.Fprintln(stderr, "internal Supervisor is only available on Windows")
	return 1
}
