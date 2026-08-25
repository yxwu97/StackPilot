//go:build windows

// Package usertask installs and controls the Phase 1 per-user Windows background process.
package usertask

import (
	"context"
	"errors"
	"io"
	"time"
)

const (
	Mode             = "user-process"
	DefaultTaskName  = "StackPilot"
	DefaultPort      = 32100
	markerName       = "installation.json"
	recordVersion    = 1
	controlProtocol  = 1
	operationTimeout = 20 * time.Second
)

var (
	ErrNotInstalled = errors.New("StackPilot user process is not installed")
	ErrNotRunning   = errors.New("StackPilot user process is not running")
	ErrInstalled    = errors.New("StackPilot user process is already installed; use service upgrade")
)

// InstallOptions defines one current-user installation without allowing a custom command.
type InstallOptions struct {
	InstallDir       string
	DataDir          string
	TaskName         string
	SourceExecutable string
	Version          string
	Port             int
	Start            bool
}

// Status is the safe public state of the installed background task.
type Status struct {
	Mode           string `json:"mode"`
	State          string `json:"state"`
	Installed      bool   `json:"installed"`
	Running        bool   `json:"running"`
	PID            uint32 `json:"pid,omitempty"`
	TaskName       string `json:"taskName,omitempty"`
	InstallDir     string `json:"installDir,omitempty"`
	DataDir        string `json:"dataDir,omitempty"`
	ExecutablePath string `json:"executablePath,omitempty"`
	Version        string `json:"version,omitempty"`
	Port           int    `json:"port,omitempty"`
}

// ServerFunc runs the existing control plane under the user-task lifecycle context.
type ServerFunc func(context.Context, []string, io.Writer, io.Writer) int

type installRecord struct {
	SchemaVersion  int       `json:"schemaVersion"`
	Mode           string    `json:"mode"`
	InstallationID string    `json:"installationId"`
	InstallDir     string    `json:"installDir"`
	DataDir        string    `json:"dataDir"`
	TaskName       string    `json:"taskName"`
	ExecutablePath string    `json:"executablePath"`
	Version        string    `json:"version"`
	SHA256         string    `json:"sha256"`
	Port           int       `json:"port"`
	InstalledAt    time.Time `json:"installedAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func statusFromRecord(record installRecord, running bool, pid uint32) Status {
	state := "stopped"
	if running {
		state = "running"
	}
	return Status{
		Mode: Mode, State: state, Installed: true, Running: running, PID: pid,
		TaskName: record.TaskName, InstallDir: record.InstallDir, DataDir: record.DataDir,
		ExecutablePath: record.ExecutablePath, Version: record.Version, Port: record.Port,
	}
}
