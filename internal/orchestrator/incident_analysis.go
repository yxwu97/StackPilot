package orchestrator

import (
	"context"
	"fmt"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/incident"
)

const incidentAnalysisFailedCode = "INCIDENT_ANALYSIS_FAILED"

// AnalyzeIncidentInput identifies a read-only evidence refresh with its idempotency scope.
type AnalyzeIncidentInput struct {
	IncidentID         domain.IncidentID
	IdempotencySubject string
	IdempotencyKey     string
	Request            []byte
}

// SubmitIncidentAnalysis queues a durable rules-only analysis Operation.
func (service *SingleService) SubmitIncidentAnalysis(ctx context.Context, input AnalyzeIncidentInput) (*CreateResult, error) {
	if service.config.IncidentAnalyzer == nil {
		return nil, fmt.Errorf("%w: incident analysis is unavailable", ErrInvalidInput)
	}
	record, err := service.config.IncidentAnalyzer.Get(ctx, input.IncidentID)
	if err != nil {
		return nil, err
	}
	workspaceRecord, err := service.config.Workspaces.Get(ctx, record.Context.WorkspaceID)
	if err != nil {
		return nil, err
	}
	result, err := service.config.Operations.Create(ctx, CreateInput{
		WorkspaceID: workspaceRecord.ID, SystemID: workspaceRecord.SystemID, Type: domain.OperationAnalyze,
		IdempotencySubject: input.IdempotencySubject, RouteKey: "incident:analyze:" + input.IncidentID.String(),
		IdempotencyKey: input.IdempotencyKey, Request: input.Request, Cancellable: false,
		StepKeys: []string{"refresh-evidence", "run-rules"},
	})
	if err != nil || !result.Created {
		return result, err
	}
	service.launch(result.Operation.ID, func(worker context.Context) { service.runIncidentAnalysis(worker, result.Operation, input.IncidentID) })
	return result, nil
}

func (service *SingleService) runIncidentAnalysis(ctx context.Context, operation Operation, id domain.IncidentID) {
	if _, err := service.config.Operations.Start(ctx, operation.ID); err != nil {
		return
	}
	var value incident.Context
	err := service.runStep(ctx, operation.ID, 1, func() error {
		record, err := service.config.IncidentAnalyzer.Get(ctx, id)
		if err != nil {
			return err
		}
		value = service.refreshIncidentContext(ctx, *record)
		return nil
	})
	if err == nil {
		err = service.runStep(ctx, operation.ID, 2, func() error {
			_, analyzeErr := service.config.IncidentAnalyzer.Reanalyze(ctx, id, value, time.Now().UTC())
			return analyzeErr
		})
	}
	service.finishIncidentAnalysis(ctx, operation, err)
}

func (service *SingleService) refreshIncidentContext(ctx context.Context, record incident.Record) incident.Context {
	value := record.Context
	value.Logs = []incident.LogLine{}
	value.Evidence = nonLogEvidence(value.Evidence)
	if value.SystemInstanceID == "" || value.ServiceInstanceID == "" {
		return value
	}
	runtime, found, err := service.config.Runtime.GetService(ctx, value.ServiceInstanceID)
	reader, ok := service.config.Runtime.(livenessSystemReader)
	if err != nil || !found || !ok {
		return value
	}
	system, err := reader.GetSystem(ctx, value.SystemInstanceID)
	if err != nil {
		return value
	}
	service.addResolvedIncidentContext(ctx, *system, *runtime, &value)
	return service.addIncidentLogs(ctx, *system, *runtime, record.LastSeenAt, value)
}

func nonLogEvidence(values []incident.EvidenceRef) []incident.EvidenceRef {
	result := make([]incident.EvidenceRef, 0, len(values))
	for _, value := range values {
		if value.Type != "log" {
			result = append(result, value)
		}
	}
	return result
}

func (service *SingleService) finishIncidentAnalysis(ctx context.Context, operation Operation, failure error) {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workerFinalizationTimeout)
	defer cancel()
	if failure == nil {
		if _, err := service.config.Operations.Succeed(finalCtx, operation.ID); err != nil {
			service.logWorkerError(operation.ID, incidentAnalysisFailedCode, err)
		}
		return
	}
	service.finishActiveSteps(finalCtx, operation.ID, incidentAnalysisFailedCode)
	if _, err := service.config.Operations.Fail(finalCtx, operation.ID, incidentAnalysisFailedCode); err != nil {
		service.logWorkerError(operation.ID, incidentAnalysisFailedCode, err)
	}
}
