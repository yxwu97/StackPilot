package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"stackpilot/internal/buildinfo"
	"stackpilot/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "internal-supervisor" {
		return runInternalSupervisor(ctx, args[1:], stderr)
	}
	if len(args) > 0 && args[0] == "internal-user-task-run" {
		return runInstalledUserTask(ctx, args[1:], stdout, stderr)
	}
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
	if args[0] == "service" {
		return runService(ctx, args[1:], stdout, stderr)
	}
	if cli.IsCommand(args[0]) {
		return cli.Run(ctx, args, stdout, stderr)
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
	fprintln(output, "  service  Install and manage the current-user background process")
	fprintln(output, "  open     Open an authenticated local Web console session")
	fprintln(output, "  workspace add  Register a workspace")
	fprintln(output, "  up       Start the current or selected system")
	fprintln(output, "  down     Stop the current or selected system")
	fprintln(output, "  status   Show current runtime status")
	fprintln(output, "  logs     Read or follow service logs")
	fprintln(output, "  wait     Wait for an Operation")
	fprintln(output, "  secret   Set, inspect metadata, or delete a protected Secret")
	fprintln(output, "  version  Print build version, commit, and timestamp")
}

func fprintln(output io.Writer, value string) {
	_, _ = fmt.Fprintln(output, value)
}
