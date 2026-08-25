//go:build !windows

// Package supervisorspike contains the isolated P0-08 Windows supervision experiment.
package supervisorspike

import (
	"context"
	"fmt"
	"io"
)

// Main reports that the executable is only meaningful on Windows.
func Main(_ context.Context, _ []string, _ io.Writer, stderr io.Writer) int {
	fmt.Fprintln(stderr, "supervisor-spike is only supported on Windows")
	return 2
}
