//go:build windows

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"stackpilot/internal/buildinfo"
	"stackpilot/internal/platform/windows/usertask"
)

type serviceFlags struct {
	installDir string
	dataDir    string
	taskName   string
	output     string
	port       int
	noStart    bool
}

func runService(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		writeServiceUsage(stdout)
		return 0
	}
	var status usertask.Status
	var err error
	switch args[0] {
	case "install":
		status, err = serviceInstall(ctx, args[1:], stderr)
	case "upgrade":
		status, err = serviceUpgrade(ctx, args[1:], stderr)
	case "start", "stop", "status", "uninstall":
		status, err = serviceLifecycle(ctx, args[0], args[1:], stderr)
	default:
		fmt.Fprintf(stderr, "unknown service command %q\n", args[0])
		writeServiceUsage(stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "service %s failed: %v\n", args[0], err)
		return 1
	}
	return writeServiceStatus(stdout, status, serviceOutput(args[1:]))
}

func serviceInstall(ctx context.Context, args []string, output io.Writer) (usertask.Status, error) {
	flags, err := parseServiceFlags("service install", args, output, true)
	if err != nil {
		return usertask.Status{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return usertask.Status{}, fmt.Errorf("resolve installer executable: %w", err)
	}
	return usertask.Install(ctx, usertask.InstallOptions{
		InstallDir: flags.installDir, DataDir: flags.dataDir, TaskName: flags.taskName,
		SourceExecutable: executable, Version: buildinfo.Current().Version, Port: flags.port, Start: !flags.noStart,
	})
}

func serviceUpgrade(ctx context.Context, args []string, output io.Writer) (usertask.Status, error) {
	flags, err := parseServiceFlags("service upgrade", args, output, false)
	if err != nil {
		return usertask.Status{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return usertask.Status{}, fmt.Errorf("resolve upgrade executable: %w", err)
	}
	return usertask.Upgrade(ctx, flags.installDir, executable, buildinfo.Current().Version)
}

func serviceLifecycle(ctx context.Context, action string, args []string, output io.Writer) (usertask.Status, error) {
	flags, err := parseServiceFlags("service "+action, args, output, false)
	if err != nil {
		return usertask.Status{}, err
	}
	switch action {
	case "start":
		return usertask.Start(ctx, flags.installDir)
	case "stop":
		return usertask.Stop(ctx, flags.installDir)
	case "status":
		return usertask.Inspect(ctx, flags.installDir)
	case "uninstall":
		return usertask.Uninstall(ctx, flags.installDir)
	default:
		return usertask.Status{}, fmt.Errorf("unsupported service action")
	}
}

func parseServiceFlags(name string, args []string, output io.Writer, install bool) (serviceFlags, error) {
	installDir, dataDir, err := usertask.DefaultDirectories()
	if err != nil {
		return serviceFlags{}, err
	}
	values := serviceFlags{installDir: installDir, dataDir: dataDir, taskName: usertask.DefaultTaskName, output: "table", port: usertask.DefaultPort}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&values.installDir, "install-dir", values.installDir, "current-user installation directory")
	flags.StringVar(&values.output, "output", values.output, "output format: table or json")
	if install {
		flags.StringVar(&values.dataDir, "data-dir", values.dataDir, "preserved control-plane data directory")
		flags.StringVar(&values.taskName, "task-name", values.taskName, "current-user startup registration name")
		flags.IntVar(&values.port, "port", values.port, "loopback HTTP port")
		flags.BoolVar(&values.noStart, "no-start", false, "register without starting now")
	}
	if err := flags.Parse(args); err != nil {
		return serviceFlags{}, err
	}
	if flags.NArg() != 0 || (values.output != "table" && values.output != "json") {
		return serviceFlags{}, fmt.Errorf("invalid service arguments")
	}
	return values, nil
}

func serviceOutput(args []string) string {
	for index, argument := range args {
		if argument == "--output" && index+1 < len(args) {
			return args[index+1]
		}
		if len(argument) > len("--output=") && argument[:len("--output=")] == "--output=" {
			return argument[len("--output="):]
		}
	}
	return "table"
}

func writeServiceStatus(output io.Writer, status usertask.Status, format string) int {
	if format == "json" {
		if err := json.NewEncoder(output).Encode(status); err != nil {
			return 1
		}
		return 0
	}
	fmt.Fprintf(output, "%s\t%s\t%s\n", status.Mode, status.State, status.DataDir)
	return 0
}

func runInstalledUserTask(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("internal-user-task-run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	installDir := flags.String("install-dir", "", "registered installation directory")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *installDir == "" {
		return 2
	}
	return usertask.RunInstalled(ctx, *installDir, stdout, stderr, runServer)
}

func writeServiceUsage(output io.Writer) {
	fprintln(output, "Usage: stackpilot service <install|upgrade|start|stop|status|uninstall> [options]")
	fprintln(output, "Phase 1 installs a current-user Windows background process; durable data is preserved on uninstall.")
}
