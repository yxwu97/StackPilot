//go:build windows

package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"stackpilot/internal/platform/windows/supervisor"
)

func runInternalSupervisor(ctx context.Context, args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("internal-supervisor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	instanceDir := flags.String("instance-dir", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *instanceDir == "" {
		if err == nil {
			_, _ = fmt.Fprintln(stderr, "internal-supervisor requires --instance-dir")
		}
		return 2
	}
	if err := supervisor.Serve(ctx, supervisor.Config{InstanceDir: *instanceDir}); err != nil {
		_, _ = fmt.Fprintf(stderr, "internal Supervisor failed: %v\n", err)
		return 1
	}
	return 0
}
