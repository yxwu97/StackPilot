package api

import (
	"testing"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/health"
	"stackpilot/internal/orchestrator"
)

func TestMapSystemStatusIncludesComposeContainerProjection(t *testing.T) {
	serviceID := domain.ServiceInstanceID("svi_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	status := orchestrator.RuntimeStatus{
		System: &domain.SystemInstance{
			ID: "si_01ARZ3NDEKTSV4RRFFQ69G5FAV", WorkspaceID: "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV",
			SystemID: "aiws", State: domain.SystemRunning, StartedAt: time.Now().UTC(),
		},
		Services: []domain.ServiceInstance{{
			ID: serviceID, SystemInstanceID: "si_01ARZ3NDEKTSV4RRFFQ69G5FAV",
			ServiceID: "infrastructure", Driver: domain.DriverCompose, ProcessMode: domain.ProcessDaemon,
			State: domain.ServiceReady, StateVersion: 3,
		}},
		Resolved: &orchestrator.ResolvedSystemSpec{Services: map[string]orchestrator.ResolvedService{
			"infrastructure": {ServiceID: "infrastructure", Driver: domain.DriverCompose},
		}},
		ComposeContainers: map[domain.ServiceInstanceID][]orchestrator.ComposeContainerStatus{
			serviceID: {{Service: "postgres", State: "running", Health: "healthy", ExitCode: 0}},
		},
		HealthCoverage: map[domain.ServiceInstanceID]health.CoverageSummary{
			serviceID: {ReadinessKind: health.KindCompose, LivenessKind: health.KindCompose, Coverage: domain.HealthCoverageContainer, SatisfiesVerification: true,
				Latest: &health.Result{Purpose: health.PurposeLiveness, Kind: health.KindCompose, Success: true, CheckedAt: time.Now().UTC()}},
		},
	}
	mapped := mapSystemStatus("aiws", "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV", status)
	if len(mapped.Services) != 1 {
		t.Fatalf("services = %#v", mapped.Services)
	}
	service := mapped.Services[0]
	if service.Driver != "compose" || service.Mode != "daemon" || len(service.Containers) != 1 ||
		service.Containers[0].Service != "postgres" || service.Containers[0].Health != "healthy" {
		t.Fatalf("Compose service projection = %#v", service)
	}
	if len(mapped.HealthCoverage) != 1 || mapped.HealthCoverage[0].Coverage != "container" ||
		!mapped.HealthCoverage[0].SatisfiesVerification || mapped.HealthCoverage[0].LatestSuccess == nil ||
		!*mapped.HealthCoverage[0].LatestSuccess || mapped.HealthCoverage[0].LatestCheckedAt == nil {
		t.Fatalf("health coverage projection = %#v", mapped.HealthCoverage)
	}
}
