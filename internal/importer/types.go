// Package importer performs bounded, read-only workspace startup discovery.
package importer

import (
	"errors"
	"time"

	"stackpilot/internal/manifest"
)

const (
	StateReadyToRegister      = "ready_to_register"
	StateInitializationNeeded = "initialization_required"
)

var (
	ErrPathInvalid           = errors.New("workspace import path is invalid")
	ErrScriptNotFound        = errors.New("workspace import script was not found")
	ErrScriptOutside         = errors.New("workspace import script is outside the workspace")
	ErrScriptType            = errors.New("workspace import script type is unsupported")
	ErrScriptEncoding        = errors.New("workspace import script encoding is unsupported")
	ErrScriptTooLarge        = errors.New("workspace import script is too large")
	ErrScriptDangerous       = errors.New("workspace import script contains dangerous syntax")
	ErrScriptUnsupported     = errors.New("workspace import script syntax is unsupported")
	ErrReferenceCycle        = errors.New("workspace import script reference cycle")
	ErrImportIncomplete      = errors.New("workspace import analysis is incomplete")
	ErrSourceChanged         = errors.New("workspace import source changed")
	ErrPortUnconfirmed       = errors.New("workspace import port is unconfirmed")
	ErrDependencyUnconfirmed = errors.New("workspace import dependency is unconfirmed")
	ErrComposeBuildConfig    = errors.New("workspace Compose build configuration is invalid")
)

type ScriptCandidate struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type ProbeResult struct {
	State      string            `json:"state"`
	Path       string            `json:"path"`
	Candidates []ScriptCandidate `json:"candidates"`
}

type Confidence string

const (
	Confirmed  Confidence = "confirmed"
	Inferred   Confidence = "inferred"
	Unresolved Confidence = "unresolved"
)

type Evidence struct {
	Path  string `json:"path"`
	Line  int    `json:"line,omitempty"`
	Field string `json:"field,omitempty"`
}

type Finding struct {
	Code       string     `json:"code"`
	Severity   string     `json:"severity"`
	Message    string     `json:"message"`
	Field      string     `json:"field,omitempty"`
	Confidence Confidence `json:"confidence"`
	Evidence   []Evidence `json:"evidence"`
}

type PortDraft struct {
	Name       string     `json:"name"`
	Preferred  int        `json:"preferred"`
	Exposure   string     `json:"exposure"`
	Confidence Confidence `json:"confidence"`
}

type ServiceDraft struct {
	ID               string            `json:"id"`
	DisplayName      string            `json:"displayName"`
	Driver           string            `json:"driver"`
	Runner           string            `json:"runner"`
	Mode             string            `json:"mode"`
	WorkingDirectory string            `json:"workingDirectory"`
	Arguments        []string          `json:"arguments"`
	Environment      map[string]string `json:"environment"`
	ReadinessType    string            `json:"readinessType"`
	ReadinessTarget  string            `json:"readinessTarget,omitempty"`
	Confidence       Confidence        `json:"confidence"`
	Compose          *ComposeDraft     `json:"compose,omitempty"`
}

type ComposeDraft struct {
	File          string                      `json:"file"`
	Services      []string                    `json:"services"`
	BuildPolicy   string                      `json:"buildPolicy"`
	BuildServices []string                    `json:"buildServices"`
	Readiness     map[string]string           `json:"readiness"`
	Ports         map[string]ComposePortDraft `json:"ports"`
}

type ComposePortDraft struct {
	Service string `json:"service"`
	Target  int    `json:"target"`
}

type CandidateDraft struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	Description          string            `json:"description"`
	Applyable            bool              `json:"applyable"`
	RequiredCapabilities []string          `json:"requiredCapabilities"`
	Services             []ServiceDraft    `json:"services"`
	Ports                []PortDraft       `json:"ports"`
	Findings             []Finding         `json:"findings"`
	Manifest             manifest.Manifest `json:"manifest"`
	ManifestYAML         string            `json:"manifestYaml"`
	ManifestDigest       string            `json:"manifestDigest"`
}

type Draft struct {
	SystemID     string           `json:"systemId"`
	SystemName   string           `json:"systemName"`
	Description  string           `json:"description,omitempty"`
	SourceScript string           `json:"sourceScript"`
	SourceDigest string           `json:"sourceDigest"`
	AnalyzedAt   time.Time        `json:"analyzedAt"`
	Candidates   []CandidateDraft `json:"candidates"`
}

func (candidate CandidateDraft) Blockers() []Finding {
	result := make([]Finding, 0)
	for _, finding := range candidate.Findings {
		if finding.Severity == "blocking" {
			result = append(result, finding)
		}
	}
	return result
}
