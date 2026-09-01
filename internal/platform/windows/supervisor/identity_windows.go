//go:build windows

package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const maxIdentityFileSize = 64 * 1024

// ErrSupervisorProcessNotFound indicates that the persisted Supervisor PID no longer exists.
var ErrSupervisorProcessNotFound = errors.New("supervisor process was not found")

// SupervisorIdentity is the persisted reconnection identity.
type SupervisorIdentity struct {
	PID             uint32    `json:"pid"`
	CreatedAt       time.Time `json:"createdAt"`
	ExecutablePath  string    `json:"executablePath"`
	AccountSID      string    `json:"accountSid"`
	PipeName        string    `json:"pipeName"`
	ProtocolVersion int       `json:"protocolVersion"`
}

func currentSupervisorIdentity(pipeName string) (SupervisorIdentity, error) {
	process, err := processIdentity(windows.CurrentProcess(), uint32(os.Getpid()), "")
	if err != nil {
		return SupervisorIdentity{}, err
	}
	return SupervisorIdentity{
		PID: process.PID, CreatedAt: process.CreatedAt, ExecutablePath: process.ExecutablePath,
		AccountSID: process.AccountSID, PipeName: pipeName, ProtocolVersion: ProtocolVersion,
	}, nil
}

// ReadSupervisorIdentity strictly reads a bounded identity file.
func ReadSupervisorIdentity(path string) (SupervisorIdentity, error) {
	var identity SupervisorIdentity
	if err := readIdentity(path, &identity, "Supervisor"); err != nil {
		return SupervisorIdentity{}, err
	}
	return identity, nil
}

// ReadProcessIdentity strictly reads a bounded managed-service identity file.
func ReadProcessIdentity(path string) (ProcessIdentity, error) {
	var identity ProcessIdentity
	if err := readIdentity(path, &identity, "service"); err != nil {
		return ProcessIdentity{}, err
	}
	return identity, nil
}

func readIdentity(path string, target any, kind string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s identity: %w", kind, err)
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxIdentityFileSize+1))
	if err != nil {
		return fmt.Errorf("read %s identity: %w", kind, err)
	}
	if len(contents) > maxIdentityFileSize {
		return fmt.Errorf("%s identity exceeds %d bytes", kind, maxIdentityFileSize)
	}
	if err := strictJSON(contents, target); err != nil {
		return fmt.Errorf("decode %s identity: %w", kind, err)
	}
	return nil
}

// VerifySupervisorIdentity proves that the persisted Supervisor still identifies the live process.
func VerifySupervisorIdentity(expected SupervisorIdentity) error {
	if expected.ProtocolVersion < MinimumProtocolVersion || expected.ProtocolVersion > ProtocolVersion {
		return fmt.Errorf("%w: persisted Supervisor version %d", errVersionMismatch, expected.ProtocolVersion)
	}
	if expected.PID == 0 || !validPipeName(expected.PipeName) {
		return fmt.Errorf("invalid Supervisor identity")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, expected.PID)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return fmt.Errorf("%w: open PID %d", ErrSupervisorProcessNotFound, expected.PID)
	}
	if err != nil {
		return fmt.Errorf("open Supervisor process: %w", err)
	}
	defer windows.CloseHandle(handle)
	actual, err := processIdentity(handle, expected.PID, "")
	if err != nil {
		return err
	}
	if !actual.CreatedAt.Equal(expected.CreatedAt) || !strings.EqualFold(actual.ExecutablePath, expected.ExecutablePath) ||
		actual.AccountSID != expected.AccountSID {
		return fmt.Errorf("Supervisor process identity mismatch")
	}
	return nil
}

func processIdentity(handle windows.Handle, pid uint32, commandDigest string) (ProcessIdentity, error) {
	createdAt, err := processCreationTime(handle)
	if err != nil {
		return ProcessIdentity{}, err
	}
	executable, err := processExecutable(handle)
	if err != nil {
		return ProcessIdentity{}, err
	}
	accountSID, err := processAccountSID(handle)
	if err != nil {
		return ProcessIdentity{}, err
	}
	return ProcessIdentity{
		PID: pid, CreatedAt: createdAt, ExecutablePath: executable, AccountSID: accountSID,
		CommandDigest: commandDigest, ProtocolVersion: ProtocolVersion,
	}, nil
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
		return "", fmt.Errorf("read process account: %w", err)
	}
	return user.User.Sid.String(), nil
}

func writeIdentityAtomic(path string, value any) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create identity directory: %w", err)
	}
	file, err := os.CreateTemp(directory, ".identity-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary identity: %w", err)
	}
	temporary := file.Name()
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, file.Close())
		}
		if err != nil {
			_ = os.Remove(temporary)
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(value); err != nil {
		return fmt.Errorf("encode identity: %w", err)
	}
	if err = file.Sync(); err != nil {
		return fmt.Errorf("flush identity: %w", err)
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("close identity: %w", err)
	}
	closed = true
	from, err := windows.UTF16PtrFromString(temporary)
	if err != nil {
		return fmt.Errorf("encode temporary identity path: %w", err)
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode identity path: %w", err)
	}
	if err = windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Errorf("publish identity: %w", err)
	}
	return nil
}
