//go:build windows

package supervisorspike

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func runLauncher(args []string) error {
	flags := flag.NewFlagSet("launcher", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workDir, pipeName, profile, fixtureDir string
	flags.StringVar(&workDir, "work-dir", "", "")
	flags.StringVar(&pipeName, "pipe", "", "")
	flags.StringVar(&profile, "profile", "", "")
	flags.StringVar(&fixtureDir, "fixture-dir", "", "")
	if err := flags.Parse(args); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve Spike executable: %w", err)
	}
	command := detachedCommand(executable,
		"supervisor", "--work-dir", workDir, "--pipe", pipeName,
		"--profile", profile, "--fixture-dir", fixtureDir,
	)
	stdout, err := os.OpenFile(filepath.Join(workDir, "supervisor.stdout.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open Supervisor stdout: %w", err)
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(workDir, "supervisor.stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open Supervisor stderr: %w", err)
	}
	defer stderr.Close()
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start detached Supervisor: %w", err)
	}
	record := launchRecord{SupervisorPID: uint32(command.Process.Pid)}
	if err := writeJSONAtomic(filepath.Join(workDir, "launch.json"), record); err != nil {
		return err
	}
	return command.Process.Release()
}
