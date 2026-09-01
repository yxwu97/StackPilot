package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/driver"
	"stackpilot/internal/health"
	"stackpilot/internal/incident"
	"stackpilot/internal/logs"
)

const (
	reconcileRestartedCode   = "CONTROL_PLANE_RESTARTED"
	reconcileMissingCode     = "SUPERVISOR_EXITED"
	reconcileIdentityCode    = "PROCESS_IDENTITY_MISMATCH"
	reconcileUnavailableCode = "SUPERVISOR_UNAVAILABLE"
	minimumProcessInterval   = 10 * time.Second
	minimumLeaseInterval     = 30 * time.Second
	healthRetentionInterval  = time.Hour
)

type reconciliationLeaseManager interface {
	ExpireReserved(context.Context, time.Time) (int64, error)
}

// Reconcile closes interrupted Operations and reattaches every provable active runtime.
func (service *SingleService) Reconcile(ctx context.Context) error {
	if _, err := service.config.Operations.RecoverInterrupted(ctx); err != nil {
		return fmt.Errorf("recover interrupted Operations: %w", err)
	}
	if err := service.reconcileLeases(ctx); err != nil {
		return err
	}
	return service.ReconcileRuntimes(ctx)
}

// ReconcileRuntimes performs one bounded identity and aggregate-state scan without restarting services.
func (service *SingleService) ReconcileRuntimes(ctx context.Context) error {
	repository, ok := service.config.Runtime.(reconciliationRuntimeRepository)
	if !ok {
		return fmt.Errorf("runtime repository does not support reconciliation")
	}
	systems, err := repository.ListActive(ctx)
	if err != nil {
		return err
	}
	for _, system := range systems {
		active, err := service.hasActiveOperation(ctx, system.WorkspaceID)
		if err != nil {
			return err
		}
		if active {
			continue
		}
		if err := service.reconcileSystem(ctx, repository, system); err != nil {
			return err
		}
	}
	return nil
}

func (service *SingleService) hasActiveOperation(ctx context.Context, workspaceID domain.WorkspaceID) (bool, error) {
	operations, err := service.config.Operations.List(ctx, &workspaceID, 200)
	if err != nil {
		return false, fmt.Errorf("inspect reconciliation Operation: %w", err)
	}
	for _, operation := range operations {
		if !operation.State.Terminal() {
			return true, nil
		}
	}
	return false, nil
}

// StartPeriodicReconciliation starts owned process and lease checks with documented lower bounds.
func (service *SingleService) StartPeriodicReconciliation(processInterval, leaseInterval time.Duration) error {
	if processInterval < minimumProcessInterval || leaseInterval < minimumLeaseInterval {
		return fmt.Errorf("reconciliation intervals must be at least %s and %s", minimumProcessInterval, minimumLeaseInterval)
	}
	service.waiters.Add(1)
	go service.runReconciliationLoop(processInterval, leaseInterval)
	return nil
}

func (service *SingleService) runReconciliationLoop(processInterval, leaseInterval time.Duration) {
	defer service.waiters.Done()
	processTicker := time.NewTicker(processInterval)
	leaseTicker := time.NewTicker(leaseInterval)
	retentionTicker := time.NewTicker(healthRetentionInterval)
	defer processTicker.Stop()
	defer leaseTicker.Stop()
	defer retentionTicker.Stop()
	service.compactHealthResults()
	for {
		select {
		case <-service.config.Context.Done():
			return
		case <-processTicker.C:
			if err := service.ReconcileRuntimes(service.config.Context); err != nil {
				service.config.Logger.Error("runtime reconciliation failed", "error", err)
			}
		case <-leaseTicker.C:
			if err := service.reconcileLeases(service.config.Context); err != nil {
				service.config.Logger.Error("port lease reconciliation failed", "error", err)
			}
		case <-retentionTicker.C:
			service.compactHealthResults()
		}
	}
}

func (service *SingleService) compactHealthResults() {
	if service.config.HealthRetention == nil {
		return
	}
	removed, err := service.config.HealthRetention.CompactDefault(service.config.Context, time.Now().UTC())
	if err != nil {
		service.config.Logger.Error("health result compaction failed", "error", err)
		return
	}
	if removed > 0 {
		service.config.Logger.Info("health result compaction completed", "removed", removed)
	}
}

func (service *SingleService) reconcileLeases(ctx context.Context) error {
	manager, ok := service.config.PortLeases.(reconciliationLeaseManager)
	if !ok {
		return nil
	}
	if _, err := manager.ExpireReserved(ctx, time.Now().UTC()); err != nil {
		return fmt.Errorf("expire stale port reservations: %w", err)
	}
	return nil
}

func (service *SingleService) reconcileSystem(ctx context.Context, repository reconciliationRuntimeRepository, system domain.SystemInstance) error {
	runtimes, err := service.config.Runtime.ListServices(ctx, system.ID)
	if err != nil {
		return fmt.Errorf("list services for reconciliation: %w", err)
	}
	var spec *ResolvedSystemSpec
	loadSpec := len(runtimes) > 1
	for _, runtime := range runtimes {
		if runtime.Driver == domain.DriverCompose {
			loadSpec = true
			break
		}
	}
	if service.config.ResolvedSpecs != nil {
		spec, err = service.loadRuntimeSpec(ctx, system.ResolvedSpecDigest)
		if err != nil && loadSpec {
			return fmt.Errorf("load reconciliation spec: %w", err)
		}
		if err != nil {
			spec = nil
		}
	} else if loadSpec {
		return fmt.Errorf("load reconciliation spec: resolved spec store unavailable")
	}
	states := make([]domain.ServiceState, 0, len(runtimes))
	for index := range runtimes {
		updated, err := service.reconcileRuntime(ctx, system, runtimes[index], spec)
		if err != nil {
			superseded, inspectErr := service.reconciliationSuperseded(ctx, runtimes[index], err)
			if inspectErr != nil {
				return inspectErr
			}
			if superseded {
				return nil
			}
			return err
		}
		states = append(states, updated.State)
	}
	if err := service.reconcileAggregate(ctx, system, states); err != nil {
		return err
	}
	service.startSystemLiveness(system.ID, spec)
	if err := repository.MarkReconciled(ctx, system.ID, time.Now().UTC()); err != nil {
		return fmt.Errorf("record reconciliation completion: %w", err)
	}
	return nil
}

func (service *SingleService) reconciliationSuperseded(ctx context.Context, stale domain.ServiceInstance, cause error) (bool, error) {
	classifier, ok := service.config.Runtime.(runtimeStateConflictClassifier)
	if !ok || !classifier.IsRuntimeStateConflict(cause) {
		return false, nil
	}
	current, found, err := service.config.Runtime.GetService(ctx, stale.ID)
	if err != nil {
		return false, fmt.Errorf("reload concurrently changed service %s: %w", stale.ServiceID, err)
	}
	if !found {
		return true, nil
	}
	return current.StateVersion != stale.StateVersion, nil
}

func (service *SingleService) reconcileRuntime(ctx context.Context, system domain.SystemInstance, runtime domain.ServiceInstance, spec *ResolvedSystemSpec) (domain.ServiceInstance, error) {
	if runtime.State == domain.ServiceCompleted {
		return runtime, nil
	}
	if runtime.Driver == domain.DriverCompose {
		return service.reconcileComposeRuntime(ctx, system, runtime, spec)
	}
	if runtime.Identity == nil {
		return service.discoverOrSettleRuntime(ctx, system, runtime, spec)
	}
	recovered, err := service.config.Driver.Recover(ctx, *runtime.Identity)
	if err != nil {
		return service.reconcileDriverError(ctx, system, runtime, spec, err)
	}
	if recovered.Observation.State != "running" {
		return service.reconcileExited(ctx, system, runtime, recovered.Observation.ExitCode, spec)
	}
	if err := service.resumeCapture(system, runtime); err != nil {
		return runtime, fmt.Errorf("resume %s log capture: %w", runtime.ServiceID, err)
	}
	if runtime.State == domain.ServiceStopping {
		return service.reconcileTransition(ctx, runtime, domain.ServiceFailed, reconcileRestartedCode, nil)
	}
	return runtime, nil
}

func (service *SingleService) discoverOrSettleRuntime(ctx context.Context, system domain.SystemInstance, runtime domain.ServiceInstance, spec *ResolvedSystemSpec) (domain.ServiceInstance, error) {
	discoverer, ok := service.config.Driver.(driver.RuntimeDiscoverer)
	if !ok {
		return service.reconcileMissingIdentity(ctx, runtime)
	}
	instanceDir := filepath.Join(service.config.DataDir, "instances", system.ID.String())
	recovered, err := discoverer.Discover(ctx, driver.DiscoveryRequest{InstanceDir: instanceDir, ServiceID: runtime.ServiceID})
	if errors.Is(err, driver.ErrRuntimeNotFound) {
		return service.reconcileMissingIdentity(ctx, runtime)
	}
	if err != nil {
		return service.reconcileDriverError(ctx, system, runtime, spec, err)
	}
	if !runtime.State.CanTransitionTo(domain.ServiceWaitingReady) {
		if recovered.Observation.State != "running" {
			return service.reconcileExited(ctx, system, runtime, recovered.Observation.ExitCode, spec)
		}
		if runtime.State == domain.ServiceStopping {
			return service.reconcileTransition(ctx, runtime, domain.ServiceFailed, reconcileRestartedCode, nil)
		}
		return runtime, nil
	}
	updated, err := service.config.Runtime.AttachIdentity(ctx, "", runtime.ID, runtime.StateVersion, recovered.Identity, time.Now().UTC())
	if err != nil {
		return runtime, fmt.Errorf("attach discovered %s identity: %w", runtime.ServiceID, err)
	}
	runtime = *updated
	if recovered.Observation.State != "running" {
		return service.reconcileExited(ctx, system, runtime, recovered.Observation.ExitCode, spec)
	}
	if err := service.resumeCapture(system, runtime); err != nil {
		return runtime, fmt.Errorf("resume discovered %s log capture: %w", runtime.ServiceID, err)
	}
	return runtime, nil
}

func (service *SingleService) reconcileMissingIdentity(ctx context.Context, runtime domain.ServiceInstance) (domain.ServiceInstance, error) {
	switch runtime.State {
	case domain.ServiceFailed, domain.ServiceStopped, domain.ServiceUnknown:
		return runtime, nil
	case domain.ServiceStopping:
		return service.reconcileTransition(ctx, runtime, domain.ServiceStopped, "", nil)
	default:
		return service.reconcileTransition(ctx, runtime, domain.ServiceFailed, reconcileRestartedCode, nil)
	}
}

func (service *SingleService) reconcileDriverError(ctx context.Context, system domain.SystemInstance, runtime domain.ServiceInstance, spec *ResolvedSystemSpec, cause error) (domain.ServiceInstance, error) {
	if errors.Is(cause, driver.ErrRuntimeNotFound) {
		return service.reconcileExited(ctx, system, runtime, nil, spec)
	}
	if runtime.State == domain.ServiceUnknown || runtime.State == domain.ServiceFailed {
		return runtime, nil
	}
	code := reconcileUnavailableCode
	if errors.Is(cause, driver.ErrIdentityMismatch) {
		code = reconcileIdentityCode
	}
	updated, err := service.reconcileTransition(ctx, runtime, domain.ServiceUnknown, code, nil)
	if err == nil && code == reconcileIdentityCode {
		service.reportServiceIncident(ctx, system, updated, incident.KindIdentityMismatch, incident.SeverityCritical, code, health.Result{ErrorCode: health.CodeProcessIdentityMismatch, CheckedAt: time.Now().UTC()})
	}
	return updated, err
}

func (service *SingleService) reconcileExited(ctx context.Context, system domain.SystemInstance, runtime domain.ServiceInstance, exitCode *uint32, spec *ResolvedSystemSpec) (domain.ServiceInstance, error) {
	if runtime.ProcessMode == domain.ProcessOneshot {
		return service.reconcileOneshotExited(ctx, runtime, exitCode)
	}
	wasStopping := runtime.State == domain.ServiceStopping
	updated, err := service.setExitedState(ctx, runtime, exitCode)
	if err == nil {
		service.closeCapture(runtime.ID)
		service.releaseReconciledLeases(ctx, system, runtime.ServiceID.String())
		service.reportServiceIncident(ctx, system, updated, incident.KindProcessExit, incident.SeverityCritical, reconcileMissingCode, health.Result{ErrorCode: health.CodeProcessExited, CheckedAt: time.Now().UTC()})
		if !wasStopping {
			err = service.restartExitedDaemon(ctx, system, updated, exitCode, spec)
		}
	}
	return updated, err
}

func (service *SingleService) restartExitedDaemon(ctx context.Context, system domain.SystemInstance, runtime domain.ServiceInstance, exitCode *uint32, spec *ResolvedSystemSpec) error {
	if spec == nil {
		return nil
	}
	resolved, found := spec.Services[runtime.ServiceID.String()]
	if !found || !restartPolicyMatchesExit(resolved.Restart.Policy, exitCode) {
		return nil
	}
	return service.scheduleAutomaticRestart(ctx, system, runtime, resolved.Restart, "process-exit")
}

func restartPolicyMatchesExit(policy string, exitCode *uint32) bool {
	if policy == "always" {
		return true
	}
	return policy == "on-failure" && (exitCode == nil || *exitCode != 0)
}

func (service *SingleService) reconcileOneshotExited(ctx context.Context, runtime domain.ServiceInstance, exitCode *uint32) (domain.ServiceInstance, error) {
	if runtime.Identity != nil {
		err := service.config.Driver.Stop(ctx, driver.StopRequest{Identity: *runtime.Identity, GracefulTimeout: 0})
		if err != nil && !errors.Is(err, driver.ErrRuntimeNotFound) {
			return runtime, err
		}
	}
	service.closeCapture(runtime.ID)
	if exitCode == nil {
		return service.reconcileTransition(ctx, runtime, domain.ServiceFailed, reconcileMissingCode, nil)
	}
	if *exitCode == 0 {
		return service.reconcileTransition(ctx, runtime, domain.ServiceCompleted, "", exitCode)
	}
	return service.reconcileTransition(ctx, runtime, domain.ServiceFailed, "PROCESS_EXITED", exitCode)
}

func (service *SingleService) setExitedState(ctx context.Context, runtime domain.ServiceInstance, exitCode *uint32) (domain.ServiceInstance, error) {
	if runtime.State == domain.ServiceStopping {
		return service.reconcileTransition(ctx, runtime, domain.ServiceStopped, "", exitCode)
	}
	if runtime.State == domain.ServiceFailed || runtime.State == domain.ServiceStopped {
		return runtime, nil
	}
	return service.reconcileTransition(ctx, runtime, domain.ServiceFailed, reconcileMissingCode, exitCode)
}

func (service *SingleService) releaseReconciledLeases(ctx context.Context, system domain.SystemInstance, serviceID string) {
	if service.config.ResolvedSpecs == nil || service.config.PortLeases == nil {
		return
	}
	spec, err := service.loadRuntimeSpec(ctx, system.ResolvedSpecDigest)
	if err != nil {
		service.config.Logger.Warn("resolved spec unavailable during lease reconciliation", "instance_id", system.ID.String())
		return
	}
	service.releaseServiceLeases(ctx, spec, serviceID)
}

func (service *SingleService) reconcileTransition(ctx context.Context, runtime domain.ServiceInstance, target domain.ServiceState, code string, exitCode *uint32) (domain.ServiceInstance, error) {
	updated, err := service.config.Runtime.TransitionService(ctx, "", runtime.ID, runtime.StateVersion, target, code, exitCode, time.Now().UTC())
	if err != nil {
		return runtime, fmt.Errorf("reconcile service %s: %w", runtime.ServiceID, err)
	}
	return *updated, nil
}

func (service *SingleService) resumeCapture(system domain.SystemInstance, runtime domain.ServiceInstance) error {
	service.mutex.Lock()
	_, exists := service.captures[runtime.ID]
	service.mutex.Unlock()
	if exists {
		return nil
	}
	secretValues, err := service.resumeProcessSecretValues(service.config.Context, system, runtime)
	if err != nil {
		return err
	}
	defer clearSecretValues(secretValues)
	instanceDir := filepath.Join(service.config.DataDir, "instances", system.ID.String())
	request := logs.CaptureRequest{
		Scope: logs.Scope{
			SystemID: system.SystemID, InstanceID: system.ID, ServiceID: runtime.ServiceID,
			ServiceInstanceID: runtime.ID,
		},
		Spools: map[logs.Stream]string{
			logs.StreamStdout: filepath.Join(instanceDir, runtime.ServiceID.String()+".stdout.spool"),
			logs.StreamStderr: filepath.Join(instanceDir, runtime.ServiceID.String()+".stderr.spool"),
		},
		SecretValues: secretValues,
	}
	session, err := service.config.ResumeLogs(service.config.Context, request)
	if err != nil {
		return err
	}
	service.mutex.Lock()
	service.captures[runtime.ID] = session
	service.mutex.Unlock()
	return nil
}

func (service *SingleService) reconcileAggregate(ctx context.Context, system domain.SystemInstance, states []domain.ServiceState) error {
	target := aggregateReconciledState(system.State, states)
	if target == system.State {
		return nil
	}
	if !system.State.CanTransitionTo(target) {
		return fmt.Errorf("reconciled aggregate transition %s -> %s is invalid", system.State, target)
	}
	_, err := service.config.Runtime.TransitionSystem(ctx, "", system.ID, target, time.Now().UTC())
	return err
}

func aggregateReconciledState(current domain.SystemState, states []domain.ServiceState) domain.SystemState {
	allStopped, allReady, degraded := len(states) > 0, len(states) > 0, false
	for _, state := range states {
		if state == domain.ServiceFailed || state == domain.ServiceUnknown {
			return domain.SystemFailed
		}
		allStopped = allStopped && state == domain.ServiceStopped
		allReady = allReady && (state == domain.ServiceReady || state == domain.ServiceCompleted)
		degraded = degraded || state == domain.ServiceDegraded
	}
	if current == domain.SystemStopping && allStopped {
		return domain.SystemStopped
	}
	if allReady {
		return domain.SystemRunning
	}
	if degraded {
		return domain.SystemDegraded
	}
	return current
}
