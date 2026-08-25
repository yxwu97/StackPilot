//go:build windows

package usertask

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"stackpilot/internal/security"
)

var taskNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func defaultPaths() (string, string, error) {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return "", "", fmt.Errorf("LOCALAPPDATA is not set")
	}
	return filepath.Join(root, "Programs", "StackPilot"), filepath.Join(root, "StackPilot"), nil
}

// DefaultDirectories returns the Phase 1 current-user installation and data roots.
func DefaultDirectories() (string, string, error) {
	return defaultPaths()
}

func normalizeInstallOptions(options InstallOptions) (InstallOptions, error) {
	defaultInstall, defaultData, err := defaultPaths()
	if err != nil {
		return InstallOptions{}, err
	}
	if options.InstallDir == "" {
		options.InstallDir = defaultInstall
	}
	if options.DataDir == "" {
		options.DataDir = defaultData
	}
	if options.TaskName == "" {
		options.TaskName = DefaultTaskName
	}
	if options.Port == 0 {
		options.Port = DefaultPort
	}
	if !taskNamePattern.MatchString(options.TaskName) || options.Port < 1 || options.Port > 65535 {
		return InstallOptions{}, fmt.Errorf("task name or port is invalid")
	}
	if options.SourceExecutable == "" {
		return InstallOptions{}, fmt.Errorf("source executable is required")
	}
	return options, nil
}

func prepareRoots(installDir, dataDir string) (string, string, error) {
	for _, path := range []string{installDir, dataDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return "", "", fmt.Errorf("create installation directory: %w", err)
		}
	}
	installRoot, err := security.CanonicalExistingPath(installDir)
	if err != nil {
		return "", "", err
	}
	dataRoot, err := security.CanonicalExistingPath(dataDir)
	if err != nil {
		return "", "", err
	}
	if err := rejectOverlappingRoots(installRoot, dataRoot); err != nil {
		return "", "", err
	}
	return installRoot, dataRoot, nil
}

func rejectOverlappingRoots(installRoot, dataRoot string) error {
	dataInInstall, err := security.PathWithinRoot(installRoot, dataRoot)
	if err != nil {
		return err
	}
	installInData, err := security.PathWithinRoot(dataRoot, installRoot)
	if err != nil {
		return err
	}
	if dataInInstall || installInData {
		return fmt.Errorf("installation and data directories must not overlap")
	}
	return nil
}

func newInstallationID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate installation ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open executable for checksum: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash executable: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func loadRecord(installDir string) (installRecord, error) {
	path, err := markerPath(installDir)
	if err != nil {
		return installRecord{}, err
	}
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return installRecord{}, ErrNotInstalled
	}
	if err != nil {
		return installRecord{}, fmt.Errorf("read installation record: %w", err)
	}
	var record installRecord
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return installRecord{}, fmt.Errorf("decode installation record: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return installRecord{}, fmt.Errorf("installation record contains multiple JSON values")
	}
	if err := validateRecord(record, filepath.Dir(path)); err != nil {
		return installRecord{}, err
	}
	return record, nil
}

func markerPath(installDir string) (string, error) {
	absolute, err := filepath.Abs(installDir)
	if err != nil {
		return "", fmt.Errorf("resolve installation directory: %w", err)
	}
	return filepath.Join(filepath.Clean(absolute), markerName), nil
}

func validateRecord(record installRecord, markerParent string) error {
	if record.SchemaVersion != recordVersion || record.Mode != Mode ||
		!taskNamePattern.MatchString(record.TaskName) || len(record.InstallationID) != 32 || len(record.SHA256) != 64 ||
		record.Port < 1 || record.Port > 65535 || record.InstalledAt.IsZero() || record.UpdatedAt.IsZero() {
		return fmt.Errorf("installation record fields are invalid")
	}
	if _, err := hex.DecodeString(record.InstallationID); err != nil {
		return fmt.Errorf("installation ID is invalid")
	}
	if _, err := hex.DecodeString(record.SHA256); err != nil {
		return fmt.Errorf("installation checksum is invalid")
	}
	return validateRecordPaths(record, markerParent)
}

func validateRecordPaths(record installRecord, markerParent string) error {
	installRoot, err := security.CanonicalExistingPath(markerParent)
	if err != nil {
		return err
	}
	if !strings.EqualFold(installRoot, record.InstallDir) {
		return fmt.Errorf("installation record root does not match its location")
	}
	dataRoot, err := security.CanonicalExistingPath(record.DataDir)
	if err != nil {
		return fmt.Errorf("canonicalize registered data directory: %w", err)
	}
	if err := rejectOverlappingRoots(installRoot, dataRoot); err != nil {
		return err
	}
	executable, err := security.CanonicalExistingPath(record.ExecutablePath)
	if err != nil {
		return fmt.Errorf("canonicalize installed executable: %w", err)
	}
	versionsRoot := filepath.Join(installRoot, "versions")
	inside, err := security.PathWithinRoot(versionsRoot, executable)
	if err != nil || !inside || strings.EqualFold(versionsRoot, executable) {
		return fmt.Errorf("installed executable is outside the version directory")
	}
	actual, err := hashFile(executable)
	if err != nil || !strings.EqualFold(actual, record.SHA256) {
		return fmt.Errorf("installed executable checksum does not match the record")
	}
	return nil
}

func writeRecord(record installRecord) error {
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode installation record: %w", err)
	}
	payload = append(payload, '\n')
	temporary, err := os.CreateTemp(record.InstallDir, ".installation-*.tmp")
	if err != nil {
		return fmt.Errorf("create installation record temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write installation record: %w", err)
	}
	if err := errors.Join(temporary.Sync(), temporary.Close()); err != nil {
		return fmt.Errorf("flush installation record: %w", err)
	}
	return replaceFile(temporaryPath, filepath.Join(record.InstallDir, markerName))
}

func replaceFile(from, to string) error {
	fromPointer, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPointer, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(fromPointer, toPointer, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Errorf("publish installation file: %w", err)
	}
	return nil
}

func newRecord(options InstallOptions, installRoot, dataRoot, installationID, executable, checksum string, now time.Time) installRecord {
	return installRecord{
		SchemaVersion: recordVersion, Mode: Mode, InstallationID: installationID,
		InstallDir: installRoot, DataDir: dataRoot, TaskName: options.TaskName,
		ExecutablePath: executable, Version: options.Version, SHA256: checksum, Port: options.Port,
		InstalledAt: now.UTC(), UpdatedAt: now.UTC(),
	}
}
