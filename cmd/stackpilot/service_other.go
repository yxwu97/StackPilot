//go:build !windows

package main

import (
	"context"
	"fmt"
	"io"
)

func runService(_ context.Context, _ []string, _ io.Writer, stderr io.Writer) int {
	fprintln(stderr, "service management is not enabled on this platform")
	return 1
}

func runInstalledUserTask(_ context.Context, _ []string, _ io.Writer, stderr io.Writer) int {
	fmt.Fprintln(stderr, "internal user task is available only on Windows")
	return 1
}
