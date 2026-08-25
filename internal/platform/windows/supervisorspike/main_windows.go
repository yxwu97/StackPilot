//go:build windows

package supervisorspike

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Main runs one of the isolated Spike modes.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: supervisor-spike <run|launcher|supervisor|worker>")
		return 2
	}
	var err error
	switch args[0] {
	case "run":
		err = runExperiment(ctx, args[1:], stdout)
	case "launcher":
		err = runLauncher(args[1:])
	case "supervisor":
		err = runSupervisor(args[1:])
	case "worker":
		err = runWorker(args[1:])
	default:
		fmt.Fprintf(stderr, "unknown mode %q\n", args[0])
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

type experimentConfig struct {
	workDir    string
	profile    string
	fixtureDir string
}

func parseExperimentConfig(args []string) (experimentConfig, error) {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var config experimentConfig
	flags.StringVar(&config.workDir, "work-dir", "", "isolated output directory")
	flags.StringVar(&config.profile, "profile", "generic", "generic, npm, or maven")
	flags.StringVar(&config.fixtureDir, "fixture-dir", "", "runner fixture directory")
	if err := flags.Parse(args); err != nil {
		return experimentConfig{}, err
	}
	if flags.NArg() != 0 || config.workDir == "" {
		return experimentConfig{}, fmt.Errorf("run requires --work-dir and no positional arguments")
	}
	if config.profile != "generic" && config.profile != "npm" && config.profile != "maven" {
		return experimentConfig{}, fmt.Errorf("unsupported profile %q", config.profile)
	}
	absolute, err := filepath.Abs(config.workDir)
	if err != nil {
		return experimentConfig{}, fmt.Errorf("resolve work directory: %w", err)
	}
	config.workDir = absolute
	if config.profile != "generic" && config.fixtureDir == "" {
		return experimentConfig{}, fmt.Errorf("profile %s requires --fixture-dir", config.profile)
	}
	return config, nil
}

func runExperiment(ctx context.Context, args []string, output io.Writer) (err error) {
	config, err := parseExperimentConfig(args)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.workDir, 0o700); err != nil {
		return fmt.Errorf("create Spike work directory: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve Spike executable: %w", err)
	}
	pipeName, err := randomPipeName()
	if err != nil {
		return err
	}
	launcher := exec.CommandContext(ctx, executable, launcherArguments(config, pipeName)...)
	launcherPID := uint32(0)
	if err := launcher.Start(); err != nil {
		return fmt.Errorf("start launcher: %w", err)
	}
	launcherPID = uint32(launcher.Process.Pid)
	if err := launcher.Wait(); err != nil {
		return fmt.Errorf("launcher failed: %w", err)
	}
	report, supervisorPID, err := inspectRunningExperiment(config, pipeName, launcherPID)
	if supervisorPID != 0 {
		defer func() {
			if processAlive(supervisorPID) {
				err = joinError(err, terminateProcess(supervisorPID))
			}
		}()
	}
	if err != nil {
		return err
	}
	if err := terminateAndVerifyTree(&report); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(report)
}

func launcherArguments(config experimentConfig, pipeName string) []string {
	return []string{
		"launcher", "--work-dir", config.workDir, "--pipe", pipeName,
		"--profile", config.profile, "--fixture-dir", config.fixtureDir,
	}
}

func joinError(primary, secondary error) error {
	if primary == nil {
		return secondary
	}
	if secondary == nil {
		return primary
	}
	return fmt.Errorf("%v; cleanup: %w", primary, secondary)
}

func randomPipeName() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate pipe name: %w", err)
	}
	return `\\.\pipe\stackpilot-spike-` + hex.EncodeToString(value), nil
}
