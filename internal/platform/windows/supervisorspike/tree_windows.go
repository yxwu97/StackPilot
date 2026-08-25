//go:build windows

package supervisorspike

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const stillActiveExitCode = 259

func processTree(root uint32) (map[uint32]string, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("snapshot process tree: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	parents := make(map[uint32][]uint32)
	names := make(map[uint32]string)
	err = windows.Process32First(snapshot, &entry)
	for err == nil {
		parents[entry.ParentProcessID] = append(parents[entry.ParentProcessID], entry.ProcessID)
		names[entry.ProcessID] = windows.UTF16ToString(entry.ExeFile[:])
		err = windows.Process32Next(snapshot, &entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, fmt.Errorf("enumerate process tree: %w", err)
	}
	result := make(map[uint32]string)
	queue := append([]uint32(nil), parents[root]...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		result[pid] = names[pid]
		queue = append(queue, parents[pid]...)
	}
	return result, nil
}

func processAlive(pid uint32) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	return windows.GetExitCodeProcess(handle, &exitCode) == nil && exitCode == stillActiveExitCode
}

func terminateProcess(pid uint32) error {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return fmt.Errorf("open process %d for termination: %w", pid, err)
	}
	defer windows.CloseHandle(handle)
	if err := windows.TerminateProcess(handle, 137); err != nil {
		return fmt.Errorf("terminate process %d: %w", pid, err)
	}
	_, err = windows.WaitForSingleObject(handle, 10_000)
	if err != nil {
		return fmt.Errorf("wait for process %d: %w", pid, err)
	}
	return nil
}

func waitUntil(timeout time.Duration, check func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ready, err := check()
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("condition not met within %s", timeout)
}

func containsExecutable(tree map[uint32]string, expected string) bool {
	for _, name := range tree {
		if strings.EqualFold(name, expected) {
			return true
		}
	}
	return false
}
