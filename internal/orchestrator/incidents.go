package orchestrator

import (
	"context"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/health"
	"stackpilot/internal/incident"
	"stackpilot/internal/logs"
)

type serviceEventReader interface {
	LatestServiceEvent(context.Context, domain.ServiceInstanceID) (domain.EventID, bool, error)
}

func (service *SingleService) reportServiceIncident(ctx context.Context, system domain.SystemInstance, runtime domain.ServiceInstance, kind incident.Kind, severity incident.Severity, code string, result health.Result) {
	if service.config.Incidents == nil {
		return
	}
	at := result.CheckedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	evidence, eventID := service.incidentEvidence(ctx, runtime.ID, result)
	base := incident.Context{
		SchemaVersion: "1", WorkspaceID: system.WorkspaceID, SystemInstanceID: system.ID,
		ServiceInstanceID: runtime.ID, ServiceID: runtime.ServiceID, Kind: kind, TriggerCode: code,
		WindowStart: at.Add(-incident.DefaultBeforeWindow), WindowEnd: at.Add(incident.DefaultAfterWindow),
		Dependencies: map[string]domain.ServiceState{}, Ports: map[string]int{}, Evidence: evidence, Logs: []incident.LogLine{},
	}
	service.addResolvedIncidentContext(ctx, system, runtime, &base)
	contextValue := service.addIncidentLogs(ctx, system, runtime, at, base)
	_, _, err := service.config.Incidents.Report(ctx, incident.ReportInput{
		Context: contextValue, Severity: severity, OccurredAt: at,
		TriggerEventID: eventID, TriggerHealthResultID: result.ID,
	})
	if err != nil {
		service.config.Logger.Error("report service incident", "instance_id", system.ID.String(), "service_id", runtime.ServiceID.String(), "error", err)
	}
}

func (service *SingleService) addResolvedIncidentContext(ctx context.Context, system domain.SystemInstance, runtime domain.ServiceInstance, value *incident.Context) {
	if service.config.ResolvedSpecs == nil || system.ResolvedSpecDigest == "" {
		return
	}
	spec, err := service.loadRuntimeSpec(ctx, system.ResolvedSpecDigest)
	if err != nil {
		return
	}
	if resolved, found := spec.Services[runtime.ServiceID.String()]; found {
		for dependency := range resolved.Dependencies {
			value.Dependencies[dependency] = service.dependencyState(ctx, system.ID, domain.ServiceID(dependency))
		}
	}
	for name, port := range spec.Ports {
		value.Ports[name] = port.Port
	}
}

func (service *SingleService) dependencyState(ctx context.Context, systemID domain.SystemInstanceID, serviceID domain.ServiceID) domain.ServiceState {
	runtimes, err := service.config.Runtime.ListServices(ctx, systemID)
	if err != nil {
		return domain.ServiceUnknown
	}
	for _, runtime := range runtimes {
		if runtime.ServiceID == serviceID {
			return runtime.State
		}
	}
	return domain.ServiceUnknown
}

func (service *SingleService) addIncidentLogs(ctx context.Context, system domain.SystemInstance, runtime domain.ServiceInstance, at time.Time, base incident.Context) incident.Context {
	if service.config.IncidentLogs == nil {
		return base
	}
	from, to := at.Add(-incident.DefaultBeforeWindow), at.Add(incident.DefaultAfterWindow)
	window, err := service.config.IncidentLogs.QueryWindow(ctx, logs.WindowQuery{
		Scope: logs.Scope{SystemID: system.SystemID, InstanceID: system.ID, ServiceID: runtime.ServiceID, ServiceInstanceID: runtime.ID},
		Limit: incident.MaximumLogLines, From: &from, To: &to,
	})
	if err != nil {
		service.config.Logger.Warn("query incident log context", "instance_id", system.ID.String(), "service_id", runtime.ServiceID.String(), "error", err)
		return base
	}
	lines := make([]incident.LogLine, 0, len(window.Entries))
	for _, entry := range window.Entries {
		lines = append(lines, incident.LogLine{ServiceInstanceID: runtime.ID, Sequence: entry.Sequence, Timestamp: entry.Timestamp, Stream: string(entry.Stream), Message: entry.Message})
	}
	built, err := incident.BuildContext(base, at, lines, service.config.IncidentLogs)
	if err != nil {
		service.config.Logger.Error("build incident log context", "instance_id", system.ID.String(), "service_id", runtime.ServiceID.String(), "error", err)
		return base
	}
	for _, line := range built.Logs {
		built.Evidence = append(built.Evidence, incident.EvidenceRef{Type: "log", ServiceInstanceID: runtime.ID, LogSequence: line.Sequence})
	}
	return built
}

func (service *SingleService) incidentEvidence(ctx context.Context, id domain.ServiceInstanceID, result health.Result) ([]incident.EvidenceRef, domain.EventID) {
	if result.ID > 0 {
		return []incident.EvidenceRef{{Type: "health", HealthResultID: result.ID, ServiceInstanceID: id}}, 0
	}
	reader, ok := service.config.Runtime.(serviceEventReader)
	if !ok {
		return []incident.EvidenceRef{}, 0
	}
	eventID, found, err := reader.LatestServiceEvent(ctx, id)
	if err != nil || !found {
		return []incident.EvidenceRef{}, 0
	}
	return []incident.EvidenceRef{{Type: "event", EventID: eventID, ServiceInstanceID: id}}, eventID
}

func (service *SingleService) reportOperationIncident(ctx context.Context, operation Operation, kind incident.Kind, severity incident.Severity, code string) {
	if service.config.Incidents == nil {
		return
	}
	eventID, found, err := service.config.Operations.LatestEvent(ctx, operation.ID)
	if err != nil || !found {
		service.config.Logger.Error("resolve Operation incident evidence", "operation_id", operation.ID.String(), "error", err)
		return
	}
	at := time.Now().UTC()
	contextValue := incident.Context{
		SchemaVersion: "1", WorkspaceID: operation.WorkspaceID, Kind: kind, TriggerCode: code,
		WindowStart: at.Add(-incident.DefaultBeforeWindow), WindowEnd: at.Add(incident.DefaultAfterWindow),
		Dependencies: map[string]domain.ServiceState{}, Ports: map[string]int{},
		Evidence: []incident.EvidenceRef{{Type: "event", EventID: eventID}}, Logs: []incident.LogLine{},
	}
	if _, _, err := service.config.Incidents.Report(ctx, incident.ReportInput{Context: contextValue, Severity: severity, OccurredAt: at, TriggerEventID: eventID}); err != nil {
		service.config.Logger.Error("report Operation incident", "operation_id", operation.ID.String(), "error", err)
	}
}
