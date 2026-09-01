// Package capability owns the stable capability names advertised by StackPilot.
package capability

import "slices"

const (
	Phase2AutoRestart  = "phase2.auto-restart"
	Phase2Compose      = "phase2.compose"
	Phase2ComposeBuild = "phase2.compose-build"
	Phase2Incidents    = "phase2.incidents"
	Phase2Liveness     = "phase2.liveness"
	Phase2Oneshot      = "phase2.oneshot"
	Phase2PythonVenv   = "phase2.python-venv"
	Phase2Secret       = "phase2.secret"

	RunnerGo   = "workspace.runner.go"
	RunnerNode = "workspace.runner.node"

	Phase3ResourceMonitoring = "phase3.resource-monitoring"
	Phase3ChangePlanning     = "phase3.change-planning"
	Phase3VerifiedRestart    = "phase3.verified-restart"
)

type definition struct {
	name          string
	published     bool
	manifestAlias string
}

var registry = []definition{
	{name: Phase2AutoRestart, published: true, manifestAlias: "auto-restart"},
	{name: Phase2Compose, published: true, manifestAlias: "compose"},
	{name: Phase2ComposeBuild, published: true, manifestAlias: "compose-build"},
	{name: Phase2Incidents, published: true},
	{name: Phase2Liveness, published: true, manifestAlias: "liveness"},
	{name: Phase2Oneshot, published: true},
	{name: Phase2PythonVenv, published: true},
	{name: Phase2Secret, published: true},
	{name: Phase3ChangePlanning, published: true},
	{name: Phase3ResourceMonitoring, published: true},
	{name: Phase3VerifiedRestart},
	{name: RunnerGo, published: true, manifestAlias: "go"},
	{name: RunnerNode, published: true},
}

// Published returns a sorted copy of the capabilities that passed their release Gate.
func Published() []string {
	result := make([]string, 0, len(registry))
	for _, item := range registry {
		if item.published {
			result = append(result, item.name)
		}
	}
	slices.Sort(result)
	return result
}

// PublishedManifestAliases returns the internal aliases understood by the Manifest validator.
func PublishedManifestAliases() []string {
	result := make([]string, 0, len(registry))
	for _, item := range registry {
		if item.published && item.manifestAlias != "" {
			result = append(result, item.manifestAlias)
		}
	}
	slices.Sort(result)
	return result
}

// Known reports whether name is reserved by the capability registry.
func Known(name string) bool {
	return slices.ContainsFunc(registry, func(item definition) bool { return item.name == name })
}

// PublishedName reports whether a known capability passed its release Gate.
func PublishedName(name string) bool {
	return slices.ContainsFunc(registry, func(item definition) bool { return item.name == name && item.published })
}
