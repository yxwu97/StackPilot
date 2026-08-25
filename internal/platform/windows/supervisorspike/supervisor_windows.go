//go:build windows

package supervisorspike

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

type supervisorConfig struct {
	workDir    string
	pipeName   string
	profile    string
	fixtureDir string
}

func runSupervisor(args []string) error {
	config, err := parseSupervisorConfig(args)
	if err != nil {
		return err
	}
	listener, _, err := listenPipe(config.pipeName)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := writeSupervisorIdentity(config); err != nil {
		return err
	}
	job, err := createKillOnCloseJob()
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)
	spec, err := workerSpec(config)
	if err != nil {
		return err
	}
	process, err := createSuspended(job, spec)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)
	if err := writeWorkerIdentity(config, process); err != nil {
		_ = windows.TerminateProcess(process.Process, 1)
		return err
	}
	if _, err := windows.ResumeThread(process.Thread); err != nil {
		return fmt.Errorf("resume worker process: %w", err)
	}
	if err := writeJSONAtomic(filepath.Join(config.workDir, "resumed.json"), resumeRecord{ResumedAt: time.Now().UTC()}); err != nil {
		return err
	}
	return servePipe(listener, uint32(os.Getpid()), process.ProcessId)
}

func parseSupervisorConfig(args []string) (supervisorConfig, error) {
	flags := flag.NewFlagSet("supervisor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var config supervisorConfig
	flags.StringVar(&config.workDir, "work-dir", "", "")
	flags.StringVar(&config.pipeName, "pipe", "", "")
	flags.StringVar(&config.profile, "profile", "", "")
	flags.StringVar(&config.fixtureDir, "fixture-dir", "", "")
	if err := flags.Parse(args); err != nil {
		return supervisorConfig{}, err
	}
	if config.workDir == "" || config.pipeName == "" {
		return supervisorConfig{}, fmt.Errorf("Supervisor requires work directory and pipe")
	}
	return config, nil
}

func writeSupervisorIdentity(config supervisorConfig) error {
	identity, err := currentProcessIdentity(config.pipeName)
	if err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(config.workDir, "supervisor.json"), identity)
}

func writeWorkerIdentity(config supervisorConfig, process windows.ProcessInformation) error {
	identity, err := identityForHandle(process.Process, process.ProcessId)
	if err != nil {
		return err
	}
	identity.WrittenBeforeResume = true
	identity.IdentityFileWrittenAt = time.Now().UTC()
	return writeJSONAtomic(filepath.Join(config.workDir, "identity.json"), identity)
}

func workerSpec(config supervisorConfig) (processSpec, error) {
	switch config.profile {
	case "npm":
		return commandScriptSpec("npm.cmd", "run spike", config.fixtureDir)
	case "maven":
		return commandScriptSpec("mvn.cmd", "-q validate", config.fixtureDir)
	default:
		executable, err := os.Executable()
		if err != nil {
			return processSpec{}, fmt.Errorf("resolve worker executable: %w", err)
		}
		return processSpec{executable: executable, arguments: []string{"worker", "--depth", "2"}, workingDirectory: config.workDir}, nil
	}
}

func commandScriptSpec(name, arguments, workingDirectory string) (processSpec, error) {
	runner, err := exec.LookPath(name)
	if err != nil {
		return processSpec{}, fmt.Errorf("resolve %s: %w", name, err)
	}
	comspec := os.Getenv("COMSPEC")
	if comspec == "" {
		return processSpec{}, fmt.Errorf("COMSPEC is not set")
	}
	command := runner + " " + arguments
	if strings.ContainsAny(runner, " \t") {
		command = `"` + runner + `" ` + arguments
	}
	return processSpec{executable: comspec, arguments: []string{"/d", "/s", "/c", command}, workingDirectory: workingDirectory}, nil
}
