package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"stackpilot/internal/buildinfo"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		writeUsage(stdout)
		return 0
	}

	if args[0] == "version" || args[0] == "--version" || args[0] == "-v" {
		writeVersion(stdout)
		return 0
	}
	if args[0] == "server" {
		return runServer(ctx, args[1:], stdout, stderr)
	}

	fmt.Fprintf(stderr, "unknown command %q\n", args[0])
	writeUsage(stderr)
	return 2
}

func writeVersion(output io.Writer) {
	info := buildinfo.Current()
	fmt.Fprintf(output, "StackPilot %s\ncommit: %s\nbuilt: %s\n", info.Version, info.Commit, info.BuildTime)
}

func writeUsage(output io.Writer) {
	fprintln(output, "Usage: stackpilot <command>")
	fprintln(output, "")
	fprintln(output, "Commands:")
	fprintln(output, "  server   Start the local StackPilot control plane")
	fprintln(output, "  version  Print build version, commit, and timestamp")
}

func fprintln(output io.Writer, value string) {
	_, _ = fmt.Fprintln(output, value)
}
