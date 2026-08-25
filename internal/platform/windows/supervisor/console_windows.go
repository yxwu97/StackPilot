//go:build windows

package supervisor

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/windows"
)

func sendConsoleBreak(processGroupID uint32) (err error) {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	attachConsole := kernel32.NewProc("AttachConsole")
	freeConsole := kernel32.NewProc("FreeConsole")
	if err := callConsoleProcedure(attachConsole, uintptr(processGroupID)); err != nil {
		return fmt.Errorf("attach service console: %w", err)
	}
	defer func() {
		if freeErr := callConsoleProcedure(freeConsole); freeErr != nil {
			err = errors.Join(err, fmt.Errorf("release service console: %w", freeErr))
		}
	}()
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, processGroupID); err != nil {
		return fmt.Errorf("send service CTRL_BREAK: %w", err)
	}
	return nil
}

func callConsoleProcedure(procedure *windows.LazyProc, arguments ...uintptr) error {
	result, _, callErr := procedure.Call(arguments...)
	if result != 0 {
		return nil
	}
	if callErr == syscall.Errno(0) {
		return syscall.EINVAL
	}
	return callErr
}
