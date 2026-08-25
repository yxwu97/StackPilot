//go:build windows

// Package supervisorspike contains the isolated P0-08 Windows supervision experiment.
package supervisorspike

import "time"

const protocolVersion = 1

type runtimeIdentity struct {
	PID                   uint32    `json:"pid"`
	CreatedAt             time.Time `json:"createdAt"`
	ExecutablePath        string    `json:"executablePath"`
	AccountSID            string    `json:"accountSid"`
	ProtocolVersion       int       `json:"protocolVersion"`
	WrittenBeforeResume   bool      `json:"writtenBeforeResume"`
	IdentityFileWrittenAt time.Time `json:"identityFileWrittenAt"`
}

type supervisorIdentity struct {
	PID             uint32    `json:"pid"`
	CreatedAt       time.Time `json:"createdAt"`
	ExecutablePath  string    `json:"executablePath"`
	AccountSID      string    `json:"accountSid"`
	PipeName        string    `json:"pipeName"`
	ProtocolVersion int       `json:"protocolVersion"`
}

type resumeRecord struct {
	ResumedAt time.Time `json:"resumedAt"`
}

type launchRecord struct {
	SupervisorPID uint32 `json:"supervisorPid"`
}

type pipeRequest struct {
	Type string `json:"type"`
}

type pipeResponse struct {
	OK              bool   `json:"ok"`
	ProtocolVersion int    `json:"protocolVersion"`
	SupervisorPID   uint32 `json:"supervisorPid"`
	WorkerPID       uint32 `json:"workerPid"`
	Error           string `json:"error,omitempty"`
}

type spikeReport struct {
	Profile               string            `json:"profile"`
	LauncherPID           uint32            `json:"launcherPid"`
	SupervisorPID         uint32            `json:"supervisorPid"`
	WorkerPID             uint32            `json:"workerPid"`
	DescendantPIDs        []uint32          `json:"descendantPids"`
	DescendantExecutables map[uint32]string `json:"descendantExecutables"`
	PipeAllowedSIDs       []string          `json:"pipeAllowedSids"`
	LauncherExited        bool              `json:"launcherExited"`
	IdentityRecovered     bool              `json:"identityRecovered"`
	PipeReconnected       bool              `json:"pipeReconnected"`
	IdentityBeforeResume  bool              `json:"identityBeforeResume"`
	TreeTerminated        bool              `json:"treeTerminated"`
}
