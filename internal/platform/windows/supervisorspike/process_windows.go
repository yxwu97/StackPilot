//go:build windows

package supervisorspike

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processSpec struct {
	executable       string
	arguments        []string
	workingDirectory string
}

func createKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create Job Object: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("set Job Object limits: %w", err)
	}
	return job, nil
}

func createSuspended(job windows.Handle, spec processSpec) (windows.ProcessInformation, error) {
	application, err := windows.UTF16PtrFromString(spec.executable)
	if err != nil {
		return windows.ProcessInformation{}, fmt.Errorf("encode executable path: %w", err)
	}
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{spec.executable}, spec.arguments...)))
	if err != nil {
		return windows.ProcessInformation{}, fmt.Errorf("encode command line: %w", err)
	}
	workingDirectory, err := windows.UTF16PtrFromString(spec.workingDirectory)
	if err != nil {
		return windows.ProcessInformation{}, fmt.Errorf("encode working directory: %w", err)
	}
	startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Flags: windows.STARTF_USESHOWWINDOW, ShowWindow: windows.SW_HIDE}
	var process windows.ProcessInformation
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW | windows.CREATE_UNICODE_ENVIRONMENT)
	if err := windows.CreateProcess(application, commandLine, nil, nil, false, flags, nil, workingDirectory, &startup, &process); err != nil {
		return windows.ProcessInformation{}, fmt.Errorf("create suspended process: %w", err)
	}
	if err := windows.AssignProcessToJobObject(job, process.Process); err != nil {
		_ = windows.TerminateProcess(process.Process, 1)
		windows.CloseHandle(process.Thread)
		windows.CloseHandle(process.Process)
		return windows.ProcessInformation{}, fmt.Errorf("assign process to Job Object: %w", err)
	}
	return process, nil
}

func detachedCommand(executable string, arguments ...string) *exec.Cmd {
	command := exec.Command(executable, arguments...)
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
	return command
}

func identityForHandle(handle windows.Handle, pid uint32) (runtimeIdentity, error) {
	createdAt, err := processCreationTime(handle)
	if err != nil {
		return runtimeIdentity{}, err
	}
	executable, err := processExecutable(handle)
	if err != nil {
		return runtimeIdentity{}, err
	}
	accountSID, err := processAccountSID(handle)
	if err != nil {
		return runtimeIdentity{}, err
	}
	return runtimeIdentity{PID: pid, CreatedAt: createdAt, ExecutablePath: executable, AccountSID: accountSID, ProtocolVersion: protocolVersion}, nil
}

func processCreationTime(handle windows.Handle) (time.Time, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return time.Time{}, fmt.Errorf("read process creation time: %w", err)
	}
	return time.Unix(0, creation.Nanoseconds()).UTC(), nil
}

func processExecutable(handle windows.Handle) (string, error) {
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return "", fmt.Errorf("read process executable: %w", err)
	}
	return filepath.Clean(windows.UTF16ToString(buffer[:size])), nil
}

func processAccountSID(handle windows.Handle) (string, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &token); err != nil {
		return "", fmt.Errorf("open process token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read process user: %w", err)
	}
	return user.User.Sid.String(), nil
}

func currentProcessIdentity(pipeName string) (supervisorIdentity, error) {
	handle := windows.CurrentProcess()
	identity, err := identityForHandle(handle, uint32(os.Getpid()))
	if err != nil {
		return supervisorIdentity{}, err
	}
	return supervisorIdentity{
		PID: identity.PID, CreatedAt: identity.CreatedAt, ExecutablePath: identity.ExecutablePath,
		AccountSID: identity.AccountSID, PipeName: pipeName, ProtocolVersion: protocolVersion,
	}, nil
}

func verifyProcessIdentity(expected runtimeIdentity) error {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, expected.PID)
	if err != nil {
		return fmt.Errorf("open process %d: %w", expected.PID, err)
	}
	defer windows.CloseHandle(handle)
	actual, err := identityForHandle(handle, expected.PID)
	if err != nil {
		return err
	}
	if !actual.CreatedAt.Equal(expected.CreatedAt) || !strings.EqualFold(actual.ExecutablePath, expected.ExecutablePath) || actual.AccountSID != expected.AccountSID {
		return fmt.Errorf("process identity mismatch for PID %d", expected.PID)
	}
	return nil
}
