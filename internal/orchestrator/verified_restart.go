package orchestrator

import (
	"context"
	"errors"
	"time"

	"stackpilot/internal/changeplan"
	"stackpilot/internal/domain"
	"stackpilot/internal/health"
	"stackpilot/internal/revision"
	"stackpilot/internal/workspace"
)

var verifiedRestartPrefixSteps = []string{"load-plan", "refresh-candidate-revision", "validate-plan"}

// VerifiedRestartInput binds one restart request to an immutable ChangePlan.
type VerifiedRestartInput struct {
	WorkspaceID        domain.WorkspaceID
	SystemID           domain.SystemID
	ChangePlanID       domain.ChangePlanID
	IdempotencySubject string
	IdempotencyKey     string
	Request            []byte
}

// SubmitVerifiedRestart creates a plan-bound restart Operation without accepting runtime overrides.
func (service *SingleService) SubmitVerifiedRestart(ctx context.Context, input VerifiedRestartInput) (*CreateResult, error) {
	plan, err := service.loadVerifiedRestartPlan(ctx, input)
	if err != nil {
		return nil, err
	}
	if err := service.validateVerificationPrerequisites(ctx, input.WorkspaceID); err != nil {
		return nil, err
	}
	steps, err := service.verifiedRestartSteps(ctx, input.WorkspaceID)
	if err != nil {
		return nil, err
	}
	result, err := service.config.Operations.Create(ctx, CreateInput{
		WorkspaceID: input.WorkspaceID, SystemID: input.SystemID, Type: domain.OperationVerifiedRestart,
		IdempotencySubject: input.IdempotencySubject, RouteKey: "verified-restart:" + plan.Record.ID.String(),
		IdempotencyKey: input.IdempotencyKey, Request: input.Request, Cancellable: true, StepKeys: steps,
	})
	if err != nil || !result.Created {
		return result, err
	}
	service.launch(result.Operation.ID, func(worker context.Context) {
		service.runVerifiedRestart(worker, result.Operation, input)
	})
	return result, nil
}

func (service *SingleService) loadVerifiedRestartPlan(ctx context.Context, input VerifiedRestartInput) (*changeplan.Plan, error) {
	if service.config.Revisions == nil || service.config.ChangePlans == nil {
		return nil, ErrVerificationUnavailable
	}
	record, err := service.config.Workspaces.Get(ctx, input.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if record.SystemID != input.SystemID {
		return nil, workspace.ErrNotFound
	}
	plan, err := service.config.ChangePlans.Get(ctx, input.ChangePlanID)
	if err != nil {
		return nil, err
	}
	if plan.Record.WorkspaceID != input.WorkspaceID || plan.Record.SystemID != input.SystemID {
		return nil, changeplan.ErrNotFound
	}
	if plan.Record.State == domain.ChangePlanBlocked || plan.Record.BlockedCount > 0 {
		return nil, ErrChangePlanBlocked
	}
	if plan.Record.State != domain.ChangePlanReady {
		return nil, ErrChangePlanInvalidState
	}
	return plan, nil
}

func (service *SingleService) verifiedRestartSteps(ctx context.Context, workspaceID domain.WorkspaceID) ([]string, error) {
	stop, err := service.restartStopKeys(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	_, value, err := service.config.Workspaces.ExecutionManifest(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	graph, err := NewDAG(value.Spec.Services)
	if err != nil {
		return nil, err
	}
	steps := append([]string(nil), verifiedRestartPrefixSteps...)
	steps = append(steps, stop...)
	steps = append(steps, systemStartStepKeys(graph, value.Spec.Services)...)
	return append(steps, "stability-observation", "persist-verification"), nil
}

func (service *SingleService) runVerifiedRestart(ctx context.Context, operation Operation, input VerifiedRestartInput) {
	if _, err := service.config.Operations.Start(ctx, operation.ID); err != nil {
		return
	}
	err := service.prepareVerifiedRestart(ctx, operation, input)
	if err != nil {
		service.finishVerifiedRestart(ctx, operation, input.ChangePlanID, err)
		return
	}
	stopping, err := service.executeRestartStop(ctx, operation)
	if err != nil {
		service.finishVerifiedStop(ctx, operation, stopping, err)
		return
	}
	execution, err := service.executeVerifiedStart(ctx, operation, input)
	if err != nil {
		service.finishSystemStart(ctx, operation, execution, err)
		return
	}
	service.startSystemLiveness(execution.system.ID, execution.spec)
	if err := service.verifyRestartedSystem(ctx, operation, execution); err != nil {
		service.finishVerifiedRestart(ctx, operation, input.ChangePlanID, err)
		return
	}
	_, _ = service.config.Operations.Succeed(ctx, operation.ID)
}

func (service *SingleService) prepareVerifiedRestart(ctx context.Context, operation Operation, input VerifiedRestartInput) error {
	var plan *changeplan.Plan
	err := service.runStep(ctx, operation.ID, stepNumber(operation, "load-plan"), func() error {
		var loadErr error
		plan, loadErr = service.loadVerifiedRestartPlan(ctx, input)
		return loadErr
	})
	if err != nil {
		return err
	}
	var candidate *revision.Record
	err = service.runStep(ctx, operation.ID, stepNumber(operation, "refresh-candidate-revision"), func() error {
		var collectErr error
		candidate, collectErr = service.config.Revisions.Collect(ctx, input.WorkspaceID, domain.RevisionWorkspace)
		return collectErr
	})
	if err != nil {
		return err
	}
	err = service.runStep(ctx, operation.ID, stepNumber(operation, "validate-plan"), func() error {
		if candidate.Digest != plan.To.Digest {
			return ErrChangePlanStale
		}
		return service.validateVerificationPrerequisites(ctx, input.WorkspaceID)
	})
	return err
}

func (service *SingleService) executeVerifiedStart(ctx context.Context, operation Operation, input VerifiedRestartInput) (*systemStartExecution, error) {
	execution, err := service.prepareSystemStart(ctx, operation, StartSingleServiceInput{
		WorkspaceID: input.WorkspaceID, SystemID: input.SystemID,
	})
	if execution != nil && execution.plan != nil {
		defer execution.plan.Close()
	}
	if err != nil {
		return execution, err
	}
	startCtx, cancel := startDeadline(ctx, execution.spec.StartTimeout)
	defer cancel()
	return execution, service.executeSystemStart(startCtx, operation, execution)
}

func (service *SingleService) validateVerificationPrerequisites(ctx context.Context, workspaceID domain.WorkspaceID) error {
	system, found, err := service.config.Runtime.GetActive(ctx, workspaceID)
	if err != nil {
		return err
	}
	if !found || (system.State != domain.SystemRunning && system.State != domain.SystemDegraded) {
		return ErrVerificationUnavailable
	}
	spec, err := service.loadRuntimeSpec(ctx, system.ResolvedSpecDigest)
	if err != nil {
		return ErrVerificationUnavailable
	}
	runtimes, err := service.config.Runtime.ListServices(ctx, system.ID)
	if err != nil {
		return err
	}
	complete, failed, err := service.verificationSnapshot(ctx, system.ID, spec, runtimes, false)
	if err != nil {
		return err
	}
	if failed || !complete {
		return ErrVerificationHealthIncomplete
	}
	return nil
}

func (service *SingleService) verifyRestartedSystem(ctx context.Context, operation Operation, execution *systemStartExecution) error {
	step := stepNumber(operation, "stability-observation")
	if err := service.runStep(ctx, operation.ID, step, func() error {
		return service.observeVerificationWindow(ctx, execution.system.ID, execution.spec)
	}); err != nil {
		return err
	}
	return service.runStepDetail(ctx, operation.ID, stepNumber(operation, "persist-verification"), func() (string, error) {
		return string(domain.VerificationPassed), nil
	})
}

func (service *SingleService) observeVerificationWindow(ctx context.Context, systemID domain.SystemInstanceID, spec *ResolvedSystemSpec) error {
	deadline := time.NewTimer(service.config.VerificationTimeout)
	ticker := time.NewTicker(service.config.VerificationPollInterval)
	defer deadline.Stop()
	defer ticker.Stop()
	var stableSince time.Time
	for {
		complete, failed, err := service.readVerificationState(ctx, systemID, spec)
		if err != nil {
			return err
		}
		if failed {
			return ErrVerificationFailed
		}
		if complete && stableSince.IsZero() {
			stableSince = time.Now()
		}
		if complete && time.Since(stableSince) >= service.config.VerificationStableWindow {
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-deadline.C:
			return ErrVerificationFailed
		case <-ticker.C:
		}
	}
}

func (service *SingleService) readVerificationState(ctx context.Context, systemID domain.SystemInstanceID, spec *ResolvedSystemSpec) (bool, bool, error) {
	system, found, err := service.config.Runtime.GetActive(ctx, spec.WorkspaceID)
	if err != nil {
		return false, false, err
	}
	if !found || system.ID != systemID || (system.State != domain.SystemRunning && system.State != domain.SystemDegraded) {
		return false, true, nil
	}
	runtimes, err := service.config.Runtime.ListServices(ctx, systemID)
	if err != nil {
		return false, false, err
	}
	return service.verificationSnapshot(ctx, systemID, spec, runtimes, true)
}

func (service *SingleService) verificationSnapshot(ctx context.Context, systemID domain.SystemInstanceID, spec *ResolvedSystemSpec, runtimes []domain.ServiceInstance, requireOwner bool) (bool, bool, error) {
	byService := make(map[string]domain.ServiceInstance, len(runtimes))
	for _, runtime := range runtimes {
		if runtime.SystemInstanceID != systemID {
			return false, true, nil
		}
		byService[runtime.ServiceID.String()] = runtime
	}
	for serviceID, resolved := range spec.Services {
		if !resolved.Required {
			continue
		}
		runtime, exists := byService[serviceID]
		if !exists {
			return false, true, nil
		}
		complete, failed, err := service.requiredServiceVerification(ctx, runtime, resolved, requireOwner)
		if err != nil || failed || !complete {
			return complete, failed, err
		}
	}
	return true, false, nil
}

func (service *SingleService) requiredServiceVerification(ctx context.Context, runtime domain.ServiceInstance, resolved ResolvedService, requireOwner bool) (bool, bool, error) {
	if runtime.ProcessMode == domain.ProcessOneshot {
		return runtime.State == domain.ServiceCompleted, runtime.State != domain.ServiceCompleted, nil
	}
	if runtime.State != domain.ServiceReady {
		return false, true, nil
	}
	latest, err := service.latestLiveness(ctx, runtime.ID)
	if err != nil {
		return false, false, err
	}
	coverage := health.SummarizeCoverage(health.CoverageInput{
		Driver: runtime.Driver, Mode: runtime.ProcessMode, Required: true, State: runtime.State,
		ReadinessKind: resolved.Readiness.Kind, Liveness: resolved.Liveness, Latest: latest,
	})
	if latest != nil && (!latest.Success || latest.CheckedAt.Before(runtime.CreatedAt)) {
		return false, true, nil
	}
	if requireOwner && !service.hasLivenessOwner(runtime.ID) {
		return false, true, nil
	}
	return coverage.SatisfiesVerification, false, nil
}

func (service *SingleService) hasLivenessOwner(id domain.ServiceInstanceID) bool {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	_, exists := service.liveness[id]
	return exists
}

func (service *SingleService) finishVerifiedStop(ctx context.Context, operation Operation, system *domain.SystemInstance, failure error) {
	if errors.Is(context.Cause(ctx), errUserCancellation) {
		service.finishVerifiedCancellation(ctx, operation)
		return
	}
	service.finishSystemStopFailure(ctx, operation, system, failure)
}

func (service *SingleService) finishVerifiedRestart(ctx context.Context, operation Operation, planID domain.ChangePlanID, failure error) {
	if errors.Is(context.Cause(ctx), errUserCancellation) {
		service.finishVerifiedCancellation(ctx, operation)
		return
	}
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workerFinalizationTimeout)
	defer cancel()
	code := verifiedRestartErrorCode(failure)
	service.finishActiveSteps(finalCtx, operation.ID, code)
	if _, err := service.config.Operations.Fail(finalCtx, operation.ID, code); err != nil {
		service.logWorkerError(operation.ID, code, err)
		return
	}
	if code == "VERIFICATION_FAILED" {
		service.reportVerificationIncident(finalCtx, operation, planID, code)
	}
}

func (service *SingleService) finishVerifiedCancellation(ctx context.Context, operation Operation) {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workerFinalizationTimeout)
	defer cancel()
	service.finishCancelledSteps(finalCtx, operation.ID)
	_, _ = service.config.Operations.CompleteCancellation(finalCtx, operation.ID)
}

func verifiedRestartErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrChangePlanStale), errors.Is(err, revision.ErrSourceChanged):
		return "CHANGE_PLAN_STALE"
	case errors.Is(err, ErrChangePlanBlocked):
		return "CHANGE_PLAN_BLOCKED"
	case errors.Is(err, ErrChangePlanInvalidState):
		return "CHANGE_PLAN_INVALID_STATE"
	case errors.Is(err, ErrVerificationHealthIncomplete):
		return "VERIFICATION_HEALTH_INCOMPLETE"
	case errors.Is(err, ErrVerificationUnavailable):
		return "VERIFICATION_UNAVAILABLE"
	case errors.Is(err, ErrVerificationFailed), errors.Is(err, context.DeadlineExceeded):
		return "VERIFICATION_FAILED"
	default:
		return changePlanErrorCode(err)
	}
}
