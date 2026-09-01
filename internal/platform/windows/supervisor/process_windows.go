//go:build windows

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"stackpilot/internal/runner"
	"stackpilot/internal/security"
)

const stillActiveExitCode = 259

type managedService struct {
	serviceID string
	job       windows.Handle
	process   windows.Handle
	stdout    *managedOutput
	stderr    *managedOutput
	identity  ProcessIdentity
}

func startManagedService(instanceDir string, request StartServiceRequest) (_ *managedService, err error) {
	executable, workingDirectory, err := canonicalProcessPaths(request)
	if err != nil {
		return nil, err
	}
	stdout, stderr, err := openManagedOutputs(instanceDir, request)
	if err != nil {
		return nil, err
	}
	service, err := launchManagedService(instanceDir, executable, workingDirectory, request, stdout, stderr)
	if err != nil {
		return nil, errors.Join(err, stdout.abort(), stderr.abort())
	}
	return service, nil
}

func launchManagedService(instanceDir, executable, workingDirectory string, request StartServiceRequest, stdout, stderr *managedOutput) (_ *managedService, err error) {
	secretValues := request.outputSecretValues()
	defer clearOutputSecrets(secretValues)
	defer clearRequestSecrets(request)
	job, err := createKillOnCloseJob()
	if err != nil {
		return nil, err
	}
	cleanupJob := true
	defer func() {
		if cleanupJob {
			err = errors.Join(err, windows.CloseHandle(job))
		}
	}()
	process, err := createSuspendedProcess(job, executable, workingDirectory, request, stdout.writer, stderr.writer)
	if err != nil {
		return nil, err
	}
	cleanupProcess := true
	defer func() {
		if cleanupProcess {
			err = errors.Join(err, cleanupSuspendedProcess(process))
		}
	}()
	identity, err := processIdentity(process.Process, process.ProcessId, request.CommandDigest)
	if err != nil {
		return nil, err
	}
	identityPath := filepath.Join(instanceDir, "services", request.ServiceID, "identity.json")
	if err := writeIdentityAtomic(identityPath, identity); err != nil {
		return nil, err
	}
	if _, err := windows.ResumeThread(process.Thread); err != nil {
		return nil, fmt.Errorf("resume managed service: %w", err)
	}
	closeThreadError := windows.CloseHandle(process.Thread)
	process.Thread = 0
	if closeThreadError != nil {
		return nil, fmt.Errorf("close managed service thread: %w", closeThreadError)
	}
	cleanupJob, cleanupProcess = false, false
	stdout.start(cloneOutputSecrets(secretValues))
	stderr.start(cloneOutputSecrets(secretValues))
	return &managedService{
		serviceID: request.ServiceID, job: job, process: process.Process,
		stdout: stdout, stderr: stderr, identity: identity,
	}, nil
}

func canonicalProcessPaths(request StartServiceRequest) (string, string, error) {
	executable, err := security.CanonicalExistingPath(request.Executable)
	if err != nil {
		return "", "", fmt.Errorf("canonicalize service executable: %w", err)
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("service executable is not a regular file")
	}
	working, err := security.CanonicalExistingPath(request.WorkingDirectory)
	if err != nil {
		return "", "", fmt.Errorf("canonicalize service working directory: %w", err)
	}
	info, err = os.Stat(working)
	if err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("service working directory is not a directory")
	}
	return executable, working, nil
}

func openManagedOutputs(instanceDir string, request StartServiceRequest) (*managedOutput, *managedOutput, error) {
	stdout, err := openManagedOutput(instanceDir, request.StdoutPath)
	if err != nil {
		return nil, nil, err
	}
	stderr, err := openManagedOutput(instanceDir, request.StderrPath)
	if err != nil {
		_ = stdout.abort()
		return nil, nil, err
	}
	return stdout, stderr, nil
}

func openSpoolFile(instanceDir, path string) (*os.File, error) {
	canonicalRoot, err := security.CanonicalExistingPath(instanceDir)
	if err != nil {
		return nil, fmt.Errorf("canonicalize instance directory: %w", err)
	}
	canonicalParent, err := security.CanonicalExistingPath(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("canonicalize spool directory: %w", err)
	}
	inside, err := security.PathWithinRoot(canonicalRoot, canonicalParent)
	if err != nil || !inside {
		return nil, fmt.Errorf("spool path is outside the instance directory")
	}
	base := filepath.Base(path)
	if base == "." || strings.Contains(base, ":") {
		return nil, fmt.Errorf("invalid spool file name")
	}
	file, err := os.OpenFile(filepath.Join(canonicalParent, base), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open service spool: %w", err)
	}
	if err := verifyOpenSpool(canonicalRoot, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := windows.SetHandleInformation(windows.Handle(file.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("make spool handle inheritable: %w", err)
	}
	return file, nil
}

func verifyOpenSpool(canonicalRoot string, file *os.File) error {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("service spool is not a regular file")
	}
	canonicalFile, err := security.CanonicalExistingPath(file.Name())
	if err != nil {
		return fmt.Errorf("canonicalize service spool: %w", err)
	}
	inside, err := security.PathWithinRoot(canonicalRoot, canonicalFile)
	if err != nil || !inside {
		return fmt.Errorf("spool file resolves outside the instance directory")
	}
	return nil
}

func createKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create service Job Object: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("configure service Job Object: %w", err)
	}
	return job, nil
}

func createSuspendedProcess(job windows.Handle, executable, workingDirectory string, request StartServiceRequest, stdout, stderr *os.File) (windows.ProcessInformation, error) {
	application, commandLine, err := processCommand(executable, request.Arguments, request.Environment)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	directory, err := windows.UTF16PtrFromString(workingDirectory)
	if err != nil {
		return windows.ProcessInformation{}, fmt.Errorf("encode service working directory: %w", err)
	}
	environment, err := environmentBlock(request.Environment)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	stdin, err := openInheritableNull()
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	defer stdin.Close()
	startup := windows.StartupInfo{
		Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Flags: windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW,
		StdInput: windows.Handle(stdin.Fd()), StdOutput: windows.Handle(stdout.Fd()),
		StdErr: windows.Handle(stderr.Fd()), ShowWindow: windows.SW_HIDE,
	}
	var process windows.ProcessInformation
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NEW_CONSOLE | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_UNICODE_ENVIRONMENT)
	if err := windows.CreateProcess(application, commandLine, nil, nil, true, flags, environment, directory, &startup, &process); err != nil {
		return windows.ProcessInformation{}, fmt.Errorf("create suspended service process: %w", err)
	}
	if err := windows.AssignProcessToJobObject(job, process.Process); err != nil {
		_ = windows.TerminateProcess(process.Process, 1)
		_ = windows.CloseHandle(process.Thread)
		_ = windows.CloseHandle(process.Process)
		return windows.ProcessInformation{}, fmt.Errorf("assign service process to Job Object: %w", err)
	}
	return process, nil
}

func openInheritableNull() (*os.File, error) {
	file, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("open null service stdin: %w", err)
	}
	if err := windows.SetHandleInformation(windows.Handle(file.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("make service stdin inheritable: %w", err)
	}
	return file, nil
}

func cleanupSuspendedProcess(process windows.ProcessInformation) error {
	if process.Process != 0 {
		_ = windows.TerminateProcess(process.Process, 1)
	}
	var err error
	if process.Thread != 0 {
		err = errors.Join(err, windows.CloseHandle(process.Thread))
	}
	if process.Process != 0 {
		err = errors.Join(err, windows.CloseHandle(process.Process))
	}
	return err
}

func processCommand(executable string, arguments []string, environment map[string]string) (*uint16, *uint16, error) {
	applicationPath, command := executable, windows.ComposeCommandLine(append([]string{executable}, arguments...))
	if strings.EqualFold(filepath.Ext(executable), ".cmd") {
		comspec := lookupEnvironment(environment, "COMSPEC")
		if comspec == "" {
			return nil, nil, fmt.Errorf("COMSPEC is required for .cmd runners")
		}
		built, err := runner.BuildCmdCommandLine(comspec, executable, arguments)
		if err != nil {
			return nil, nil, err
		}
		applicationPath, command = comspec, built
	}
	application, err := windows.UTF16PtrFromString(applicationPath)
	if err != nil {
		return nil, nil, fmt.Errorf("encode service executable: %w", err)
	}
	commandLine, err := windows.UTF16PtrFromString(command)
	if err != nil {
		return nil, nil, fmt.Errorf("encode service command line: %w", err)
	}
	return application, commandLine, nil
}

func environmentBlock(environment map[string]string) (*uint16, error) {
	keys := make([]string, 0, len(environment))
	seen := make(map[string]struct{}, len(environment))
	for key := range environment {
		normalized := strings.ToUpper(key)
		if _, exists := seen[normalized]; exists {
			return nil, fmt.Errorf("duplicate case-insensitive environment key")
		}
		seen[normalized] = struct{}{}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool { return strings.ToUpper(keys[left]) < strings.ToUpper(keys[right]) })
	var values []uint16
	for _, key := range keys {
		values = append(values, utf16.Encode([]rune(key+"="+environment[key]))...)
		values = append(values, 0)
	}
	values = append(values, 0)
	if len(environment) == 0 {
		values = append(values, 0)
	}
	return &values[0], nil
}

func lookupEnvironment(environment map[string]string, name string) string {
	for key, value := range environment {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func (service *managedService) status() (ServiceStatus, error) {
	var exitCode uint32
	if err := windows.GetExitCodeProcess(service.process, &exitCode); err != nil {
		return ServiceStatus{}, fmt.Errorf("read service exit code: %w", err)
	}
	state := "running"
	var persistedExit *uint32
	if exitCode != stillActiveExitCode {
		state, persistedExit = "exited", &exitCode
	}
	identity := service.identity
	return ServiceStatus{ServiceID: service.serviceID, State: state, Identity: &identity, ExitCode: persistedExit}, nil
}

func (service *managedService) verifyIdentity() error {
	actual, err := processIdentity(service.process, service.identity.PID, service.identity.CommandDigest)
	if err != nil {
		return err
	}
	if !actual.CreatedAt.Equal(service.identity.CreatedAt) || !strings.EqualFold(actual.ExecutablePath, service.identity.ExecutablePath) ||
		actual.AccountSID != service.identity.AccountSID || actual.CommandDigest != service.identity.CommandDigest {
		return fmt.Errorf("%w: managed service", errIdentityMismatch)
	}
	return nil
}

func (service *managedService) stop(timeout time.Duration, graceful func(uint32) error) (ServiceStatus, error) {
	status, err := service.status()
	if err != nil {
		return ServiceStatus{}, err
	}
	if status.State == "exited" {
		return service.reapExited(status)
	}
	if err := service.verifyIdentity(); err != nil {
		return ServiceStatus{}, err
	}
	forced := timeout <= 0 || graceful == nil
	if !forced {
		if err := graceful(service.identity.PID); err != nil {
			forced = true
		} else {
			empty, err := service.waitForJobEmpty(timeout)
			if err != nil {
				return ServiceStatus{}, err
			}
			forced = !empty
		}
	}
	if forced {
		if err := windows.TerminateJobObject(service.job, 137); err != nil {
			return ServiceStatus{}, fmt.Errorf("terminate service Job Object: %w", err)
		}
		empty, err := service.waitForJobEmpty(10 * time.Second)
		if err != nil {
			return ServiceStatus{}, fmt.Errorf("wait for forced service exit: %w", err)
		}
		if !empty {
			return ServiceStatus{}, fmt.Errorf("forced service Job did not become empty")
		}
	}
	status, err = service.status()
	if err != nil {
		return ServiceStatus{}, err
	}
	status.Forced = forced
	return status, nil
}

func (service *managedService) reapExited(status ServiceStatus) (ServiceStatus, error) {
	empty, err := jobIsEmpty(service.job)
	if err != nil {
		return ServiceStatus{}, err
	}
	if empty {
		return status, nil
	}
	if err := windows.TerminateJobObject(service.job, 137); err != nil {
		return ServiceStatus{}, fmt.Errorf("terminate exited service Job Object: %w", err)
	}
	empty, err = service.waitForJobEmpty(10 * time.Second)
	if err != nil || !empty {
		return ServiceStatus{}, errors.Join(err, fmt.Errorf("exited service Job did not become empty"))
	}
	status.Forced = true
	return status, nil
}

type jobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

type jobMemoryUsage struct {
	JobMemory         uint64
	PeakJobMemoryUsed uint64
}

const jobObjectMemoryUsageInformation int32 = 28

func (service *managedService) resources() (ResourceObservation, error) {
	status, err := service.status()
	if err != nil {
		return ResourceObservation{}, err
	}
	accounting := jobAccounting{}
	if err := windows.QueryInformationJobObject(service.job, windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil {
		return ResourceObservation{}, fmt.Errorf("query service Job accounting: %w", err)
	}
	memory := jobMemoryUsage{}
	if err := windows.QueryInformationJobObject(service.job, jobObjectMemoryUsageInformation,
		uintptr(unsafe.Pointer(&memory)), uint32(unsafe.Sizeof(memory)), nil); err != nil {
		return ResourceObservation{}, fmt.Errorf("query service Job memory: %w", err)
	}
	cpu100ns := accounting.TotalUserTime + accounting.TotalKernelTime
	if cpu100ns < 0 {
		return ResourceObservation{}, fmt.Errorf("service Job CPU counter is invalid")
	}
	return ResourceObservation{
		ServiceID: service.serviceID, ObservedAt: time.Now().UTC(), CPUTotalMillis: cpu100ns / 10_000,
		MemoryBytes: memory.JobMemory, ActiveProcesses: accounting.ActiveProcesses, Identity: status.Identity,
	}, nil
}

func (service *managedService) waitForJobEmpty(timeout time.Duration) (bool, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		empty, err := jobIsEmpty(service.job)
		if err != nil || empty {
			return empty, err
		}
		select {
		case <-deadline.C:
			return false, nil
		case <-ticker.C:
		}
	}
}

func jobIsEmpty(job windows.Handle) (bool, error) {
	accounting := jobAccounting{}
	err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil)
	if err != nil {
		return false, fmt.Errorf("query service Job accounting: %w", err)
	}
	return accounting.ActiveProcesses == 0, nil
}

func (service *managedService) close() error {
	handleErr := errors.Join(windows.CloseHandle(service.process), windows.CloseHandle(service.job))
	return errors.Join(handleErr, service.stdout.close(), service.stderr.close())
}

func (request StartServiceRequest) outputSecretValues() [][]byte {
	values := make([][]byte, 0, len(request.SecretEnvironmentNames))
	for _, name := range request.SecretEnvironmentNames {
		values = append(values, []byte(request.Environment[name]))
	}
	return values
}

func cloneOutputSecrets(values [][]byte) [][]byte {
	result := make([][]byte, len(values))
	for index, value := range values {
		result[index] = append([]byte(nil), value...)
	}
	return result
}

func clearRequestSecrets(request StartServiceRequest) {
	for _, name := range request.SecretEnvironmentNames {
		request.Environment[name] = ""
		delete(request.Environment, name)
	}
}
