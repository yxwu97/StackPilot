package orchestrator

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/driver"
	"stackpilot/internal/health"
	"stackpilot/internal/incident"
	"stackpilot/internal/logs"
	"stackpilot/internal/manifest"
	"stackpilot/internal/ports"
	"stackpilot/internal/workspace"
)

const maxParallelServices = 4

type systemStartExecution struct {
	system   domain.SystemInstance
	spec     *ResolvedSystemSpec
	graph    *DAG
	plan     *ports.Plan
	policy   FailurePolicy
	runtimes map[string]domain.ServiceInstance
	created  map[string]bool
	mutex    sync.Mutex
}

type systemStartFailure struct{ report FailureReport }

func (failure systemStartFailure) Error() string { return failure.report.Primary.ErrorCode }
func (failure systemStartFailure) Unwrap() error { return failure.report.Primary.Cause }

func systemStartStepKeys(graph *DAG, services map[string]manifest.Service) []string {
	result := []string{"validate-manifest", "preflight-runners", "plan-ports", "resolve-spec", "create-runtime"}
	for _, layer := range graph.Layers() {
		for _, serviceID := range layer {
			if services[serviceID].Driver == string(domain.DriverCompose) {
				result = append(result, "compose-preflight:"+serviceID, "compose-build:"+serviceID, "compose-up:"+serviceID, "wait-ready:"+serviceID)
				continue
			}
			wait := "wait-ready:"
			if services[serviceID].Mode == string(domain.ProcessOneshot) {
				wait = "wait-complete:"
			}
			result = append(result, "start:"+serviceID, wait+serviceID)
		}
	}
	return append(result, "aggregate-state")
}

func (service *SingleService) runSystemStart(ctx context.Context, operation Operation, input StartSingleServiceInput) {
	if _, err := service.config.Operations.Start(ctx, operation.ID); err != nil {
		return
	}
	execution, err := service.prepareSystemStart(ctx, operation, input)
	if execution != nil && execution.plan != nil {
		defer execution.plan.Close()
	}
	if err == nil {
		startCtx, cancel := startDeadline(ctx, execution.spec.StartTimeout)
		err = service.executeSystemStart(startCtx, operation, execution)
		cancel()
	}
	if err == nil {
		_, err = service.config.Operations.Succeed(ctx, operation.ID)
		if err == nil {
			service.startSystemLiveness(execution.system.ID, execution.spec)
		}
	}
	if err != nil {
		service.finishSystemStart(ctx, operation, execution, err)
	}
}

func (service *SingleService) prepareSystemStart(ctx context.Context, operation Operation, input StartSingleServiceInput) (*systemStartExecution, error) {
	record, manifestValue, graph, err := service.loadSystemManifestStep(ctx, operation, input)
	if err != nil {
		return nil, err
	}
	instanceID, err := domain.NewSystemInstanceID(time.Now().UTC(), rand.Reader)
	if err != nil {
		return nil, err
	}
	resolveInput := resolveSystemInput{Workspace: *record, Manifest: *manifestValue, InstanceID: instanceID, DataDir: service.config.DataDir, FailurePolicy: input.FailurePolicy, OperationID: operation.ID, Overrides: service.config.Overrides}
	prepared, err := service.prepareRunnersStep(ctx, operation, resolveInput)
	if err != nil {
		return nil, err
	}
	plan, err := service.planPortsStep(ctx, operation, input, *record, *manifestValue)
	if err != nil {
		return nil, err
	}
	resolveInput.PortPlan = plan
	resolved, err := service.resolveSystemStep(ctx, operation, resolveInput, graph, prepared)
	execution := &systemStartExecution{spec: resolved, graph: graph, plan: plan, policy: ResolveFailurePolicy(manifestValue.Spec.Policies, input.FailurePolicy)}
	if err != nil {
		return execution, err
	}
	if err := service.createSystemRuntimeStep(ctx, operation, execution); err != nil {
		return execution, err
	}
	return execution, nil
}

func (service *SingleService) loadSystemManifestStep(ctx context.Context, operation Operation, input StartSingleServiceInput) (*workspace.Record, *manifest.Manifest, *DAG, error) {
	var record *workspace.Record
	var value *manifest.Manifest
	var graph *DAG
	err := service.runStep(ctx, operation.ID, stepNumber(operation, "validate-manifest"), func() error {
		var loadErr error
		record, value, loadErr = service.config.Workspaces.ExecutionManifest(ctx, input.WorkspaceID)
		if loadErr == nil && record.SystemID != input.SystemID {
			return workspace.ErrNotFound
		}
		if loadErr == nil {
			graph, loadErr = NewDAG(value.Spec.Services)
		}
		return loadErr
	})
	return record, value, graph, err
}

func (service *SingleService) prepareRunnersStep(ctx context.Context, operation Operation, input resolveSystemInput) (map[string]preparedService, error) {
	var prepared map[string]preparedService
	err := service.runStep(ctx, operation.ID, stepNumber(operation, "preflight-runners"), func() error {
		var prepareErr error
		prepared, prepareErr = prepareAllServices(ctx, service.config.Runner, input)
		return prepareErr
	})
	return prepared, err
}

func (service *SingleService) planPortsStep(ctx context.Context, operation Operation, input StartSingleServiceInput, record workspace.Record, value manifest.Manifest) (*ports.Plan, error) {
	var plan *ports.Plan
	err := service.runStep(ctx, operation.ID, stepNumber(operation, "plan-ports"), func() error {
		preferences, err := service.config.PortLeases.LoadPreferences(ctx, record.ID)
		if err != nil {
			return err
		}
		if !boolValue(value.Spec.Policies.StickyPorts, true) {
			preferences.Sticky = nil
		}
		plan, err = service.config.PortPlanner.Plan(ctx, ports.Input{
			WorkspaceID: record.ID, OperationID: operation.ID, ManifestDigest: record.LastValidDigest,
			Requirements: systemPortRequirements(value.Spec.Ports), RequestOverrides: input.PortOverrides,
			WorkspaceOverride: preferences.Workspace, Sticky: preferences.Sticky,
		})
		return err
	})
	return plan, err
}

func (service *SingleService) resolveSystemStep(ctx context.Context, operation Operation, input resolveSystemInput, graph *DAG, prepared map[string]preparedService) (*ResolvedSystemSpec, error) {
	var resolved *ResolvedSystemSpec
	err := service.runStep(ctx, operation.ID, stepNumber(operation, "resolve-spec"), func() error {
		var resolveErr error
		resolved, resolveErr = resolvePreparedSystemSpec(ctx, service.config.Driver, input, graph, prepared)
		if resolveErr != nil {
			return resolveErr
		}
		return service.config.ResolvedSpecs.SaveResolvedSpec(ctx, resolved.Digest, resolved.WorkspaceID, resolved.ManifestDigest, resolved.CanonicalJSON, time.Now().UTC())
	})
	return resolved, err
}

func (service *SingleService) createSystemRuntimeStep(ctx context.Context, operation Operation, execution *systemStartExecution) error {
	return service.runStep(ctx, operation.ID, stepNumber(operation, "create-runtime"), func() error {
		system, runtimes, err := newSystemRuntime(execution.spec)
		if err != nil {
			return err
		}
		execution.system, execution.runtimes = system, runtimes
		execution.created = make(map[string]bool, len(runtimes))
		values := make([]domain.ServiceInstance, 0, len(runtimes))
		for _, serviceID := range sortedRuntimeNames(runtimes) {
			values = append(values, runtimes[serviceID])
		}
		return service.config.Runtime.CreateSystem(ctx, operation.ID, system, values)
	})
}

func (service *SingleService) executeSystemStart(ctx context.Context, operation Operation, execution *systemStartExecution) error {
	collector := &FailureCollector{}
	for _, layer := range execution.graph.Layers() {
		runnable := execution.runnableServices(layer)
		failures := service.startSystemLayer(ctx, operation, execution, runnable)
		for _, failure := range failures {
			collector.Add(failure)
		}
		if len(failures) > 0 && execution.policy.FailFast {
			break
		}
	}
	if report, failed := collector.Report(); failed {
		return systemStartFailure{report: report}
	}
	return service.completeSystemStart(ctx, operation, execution)
}

func (execution *systemStartExecution) runnableServices(layer []string) []string {
	execution.mutex.Lock()
	defer execution.mutex.Unlock()
	states := make(map[string]domain.ServiceState, len(execution.runtimes))
	for serviceID, runtime := range execution.runtimes {
		states[serviceID] = runtime.State
	}
	result := make([]string, 0, len(layer))
	for _, serviceID := range layer {
		if satisfied, _ := execution.graph.DependenciesSatisfied(serviceID, states); satisfied {
			result = append(result, serviceID)
		}
	}
	return result
}

func (service *SingleService) startSystemLayer(ctx context.Context, operation Operation, execution *systemStartExecution, serviceIDs []string) []ServiceFailure {
	semaphore := make(chan struct{}, maxParallelServices)
	failures := make(chan ServiceFailure, len(serviceIDs))
	var wait sync.WaitGroup
	for _, serviceID := range serviceIDs {
		serviceID := serviceID
		wait.Add(1)
		go func() {
			defer wait.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			if err := service.startSystemService(ctx, operation, execution, serviceID); err != nil {
				failures <- ServiceFailure{ServiceID: serviceID, ErrorCode: systemStartErrorCode(err), Cause: err}
			}
		}()
	}
	wait.Wait()
	close(failures)
	result := make([]ServiceFailure, 0, len(failures))
	for failure := range failures {
		result = append(result, failure)
	}
	return result
}

func (service *SingleService) startSystemService(ctx context.Context, operation Operation, execution *systemStartExecution, serviceID string) error {
	resolved := execution.spec.Services[serviceID]
	runtime := execution.runtime(serviceID)
	if runtime.State == domain.ServiceWaitingDependency {
		updated, err := service.config.Runtime.TransitionService(ctx, operation.ID, runtime.ID, runtime.StateVersion, domain.ServiceStarting, "", nil, time.Now().UTC())
		if err != nil {
			return err
		}
		runtime = *updated
		execution.updateRuntime(serviceID, runtime)
	}
	if resolved.Driver == domain.DriverCompose {
		if err := service.startComposeService(ctx, operation, execution, serviceID, runtime, resolved); err != nil {
			service.markSystemServiceFailed(context.WithoutCancel(ctx), operation.ID, execution, serviceID, err)
			return err
		}
		return nil
	}
	identity, err := service.startSystemProcess(ctx, operation, execution, serviceID, runtime, resolved)
	if err != nil {
		service.markSystemServiceFailed(context.WithoutCancel(ctx), operation.ID, execution, serviceID, err)
		return err
	}
	if err := service.readySystemProcess(ctx, operation, execution, serviceID, identity); err != nil {
		service.markSystemServiceFailed(context.WithoutCancel(ctx), operation.ID, execution, serviceID, err)
		return err
	}
	return nil
}

func (service *SingleService) startSystemProcess(ctx context.Context, operation Operation, execution *systemStartExecution, serviceID string, runtime domain.ServiceInstance, resolved ResolvedService) (driver.RuntimeIdentity, error) {
	var identity driver.RuntimeIdentity
	step := stepNumber(operation, "start:"+serviceID)
	err := service.runStep(ctx, operation.ID, step, func() error {
		for _, logicalName := range ownedLogicalPorts(execution.spec, serviceID) {
			if err := execution.plan.ReleaseProbe(logicalName); err != nil {
				return err
			}
		}
		launch, err := service.prepareProcessLaunch(ctx, operation.SystemID, runtime.ID, resolved.Process)
		if err != nil {
			return err
		}
		defer launch.clear()
		identity, err = service.config.Driver.Start(ctx, driver.StartRequest{Spec: launch.spec})
		if err != nil {
			return err
		}
		updated, err := service.config.Runtime.AttachIdentity(ctx, operation.ID, runtime.ID, runtime.StateVersion, identity, time.Now().UTC())
		if err != nil {
			return err
		}
		execution.updateRuntime(serviceID, *updated)
		execution.markCreated(serviceID)
		return service.startSystemCapture(operation, execution, serviceID, launch.redactionValues)
	})
	if err != nil {
		service.failStep(context.WithoutCancel(ctx), operation.ID, step, systemStartErrorCode(err))
	}
	return identity, err
}

func (service *SingleService) readySystemProcess(ctx context.Context, operation Operation, execution *systemStartExecution, serviceID string, identity driver.RuntimeIdentity) error {
	resolved := execution.spec.Services[serviceID]
	waitKey := "wait-ready:" + serviceID
	if resolved.Process.Mode == domain.ProcessOneshot {
		waitKey = "wait-complete:" + serviceID
	}
	step := stepNumber(operation, waitKey)
	err := service.runStep(ctx, operation.ID, step, func() error {
		if resolved.Process.Mode == domain.ProcessOneshot {
			runtime := execution.runtime(serviceID)
			updated, waitErr := service.awaitOneshot(ctx, operation.ID, runtime, identity)
			execution.updateRuntime(serviceID, updated)
			if waitErr == nil {
				service.releaseServiceLeases(context.WithoutCancel(ctx), execution.spec, serviceID)
			}
			return waitErr
		}
		resolved.Readiness.Identity = identity
		runtime := execution.runtime(serviceID)
		outcome, err := service.config.Readiness.Await(ctx, health.Request{ServiceInstanceID: runtime.ID, Spec: resolved.Readiness})
		if err != nil {
			return err
		}
		if !outcome.Ready {
			service.reportServiceIncident(ctx, execution.system, runtime, incident.KindReadinessTimeout, incident.SeverityCritical, string(outcome.ErrorCode), outcome.LastResult)
			return readinessFailure{code: string(outcome.ErrorCode)}
		}
		updated, err := service.config.Runtime.TransitionService(ctx, operation.ID, runtime.ID, runtime.StateVersion, domain.ServiceReady, "", nil, time.Now().UTC())
		if err != nil {
			return err
		}
		execution.updateRuntime(serviceID, *updated)
		return service.bindServiceLeases(ctx, execution, serviceID)
	})
	if err != nil {
		service.failStep(context.WithoutCancel(ctx), operation.ID, step, systemStartErrorCode(err))
	}
	return err
}

func (service *SingleService) bindServiceLeases(ctx context.Context, execution *systemStartExecution, serviceID string) error {
	for _, logicalName := range ownedLogicalPorts(execution.spec, serviceID) {
		if err := service.config.PortLeases.MarkBound(ctx, execution.spec.Ports[logicalName].LeaseID, execution.system.ID, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func (service *SingleService) completeSystemStart(ctx context.Context, operation Operation, execution *systemStartExecution) error {
	step := stepNumber(operation, "aggregate-state")
	return service.runStep(ctx, operation.ID, step, func() error {
		if len(execution.spec.Ports) > 0 {
			if err := service.config.PortLeases.RecordSuccessfulPlan(ctx, execution.spec.PortPlanID, time.Now().UTC()); err != nil {
				return err
			}
		}
		_, err := service.config.Runtime.TransitionSystem(ctx, operation.ID, execution.system.ID, domain.SystemRunning, time.Now().UTC())
		return err
	})
}

func (service *SingleService) startSystemCapture(operation Operation, execution *systemStartExecution, serviceID string, secretValues [][]byte) error {
	runtime, resolved := execution.runtime(serviceID), execution.spec.Services[serviceID]
	session, err := service.config.StartLogs(service.config.Context, logs.CaptureRequest{
		Scope:  logs.Scope{SystemID: operation.SystemID, InstanceID: execution.system.ID, ServiceID: runtime.ServiceID, ServiceInstanceID: runtime.ID, OperationID: operation.ID},
		Spools: map[logs.Stream]string{logs.StreamStdout: resolved.Process.StdoutPath, logs.StreamStderr: resolved.Process.StderrPath}, SecretValues: secretValues,
	})
	if err != nil {
		return err
	}
	service.mutex.Lock()
	service.captures[runtime.ID] = session
	service.mutex.Unlock()
	return nil
}

func (service *SingleService) markSystemServiceFailed(ctx context.Context, operationID domain.OperationID, execution *systemStartExecution, serviceID string, failure error) {
	runtime := execution.runtime(serviceID)
	if runtime.State.CanTransitionTo(domain.ServiceFailed) {
		updated, err := service.config.Runtime.TransitionService(ctx, operationID, runtime.ID, runtime.StateVersion, domain.ServiceFailed, systemStartErrorCode(failure), oneshotFailureExitCode(failure), time.Now().UTC())
		if err == nil {
			execution.updateRuntime(serviceID, *updated)
		}
	}
}

func (service *SingleService) finishSystemStart(ctx context.Context, operation Operation, execution *systemStartExecution, failure error) {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workerFinalizationTimeout)
	defer cancel()
	if execution == nil {
		service.finishUnresolvedSystemFailure(finalCtx, operation, failure)
		return
	}
	if errors.Is(context.Cause(ctx), errUserCancellation) {
		service.cancelSystemStart(finalCtx, operation, execution)
		return
	}
	code := systemStartErrorCode(failure)
	service.finishActiveSteps(finalCtx, operation.ID, code)
	if execution.spec != nil && execution.policy.CleanupOnFailure {
		service.compensateSystemStart(finalCtx, operation, execution, failure)
	}
	if execution.system.ID != "" {
		_, _ = service.config.Runtime.TransitionSystem(finalCtx, operation.ID, execution.system.ID, domain.SystemFailed, time.Now().UTC())
	}
	if _, err := service.config.Operations.Fail(finalCtx, operation.ID, code); err != nil {
		service.logWorkerError(operation.ID, code, err)
	} else if code == "PORT_CONFLICT" {
		service.reportOperationIncident(finalCtx, operation, incident.KindPortConflict, incident.SeverityCritical, code)
	}
}

func (service *SingleService) finishUnresolvedSystemFailure(ctx context.Context, operation Operation, failure error) {
	code := systemStartErrorCode(failure)
	service.finishActiveSteps(ctx, operation.ID, code)
	if _, err := service.config.Operations.Fail(ctx, operation.ID, code); err != nil {
		service.logWorkerError(operation.ID, code, err)
	} else if code == "PORT_CONFLICT" {
		service.reportOperationIncident(ctx, operation, incident.KindPortConflict, incident.SeverityCritical, code)
	}
}

func (service *SingleService) finishActiveSteps(ctx context.Context, operationID domain.OperationID, code string) {
	current, err := service.config.Operations.Get(ctx, operationID)
	if err != nil {
		return
	}
	for _, step := range current.Steps {
		if step.State == domain.OperationStepRunning {
			service.failStep(ctx, operationID, step.Number, code)
		}
		if step.State == domain.OperationStepPending {
			_, _ = service.config.Operations.TransitionStep(ctx, operationID, step.Number, domain.OperationStepSkipped, "", "")
		}
	}
}

func (service *SingleService) compensateSystemStart(ctx context.Context, operation Operation, execution *systemStartExecution, failure error) {
	failed := failureServiceIDs(failure)
	layers := execution.graph.CompensationLayers(execution.policy, execution.createdSnapshot(), failed)
	for _, layer := range layers {
		for _, serviceID := range layer {
			runtime := execution.runtime(serviceID)
			if err := service.stopResolvedRuntime(ctx, operation.ID, execution.spec, serviceID, &runtime); err == nil {
				execution.updateRuntime(serviceID, runtime)
				service.releaseServiceLeases(ctx, execution.spec, serviceID)
			}
		}
	}
}

func (service *SingleService) cancelSystemStart(ctx context.Context, operation Operation, execution *systemStartExecution) {
	service.finishCancelledSteps(ctx, operation.ID)
	if err := service.cleanupCancelledSystem(ctx, operation, execution); err != nil {
		code := singleServiceStopErrorCode(err)
		service.markCancelledSystemFailed(ctx, operation, execution, code)
		if _, failErr := service.config.Operations.Fail(ctx, operation.ID, code); failErr != nil {
			service.logWorkerError(operation.ID, code, failErr)
		}
		return
	}
	service.completeCancellation(ctx, operation.ID)
}

func (service *SingleService) cleanupCancelledSystem(ctx context.Context, operation Operation, execution *systemStartExecution) error {
	if execution.system.ID == "" {
		return nil
	}
	if _, err := service.config.Runtime.TransitionSystem(ctx, operation.ID, execution.system.ID, domain.SystemStopping, time.Now().UTC()); err != nil {
		return err
	}
	for _, layer := range execution.graph.ReverseLayers() {
		for _, serviceID := range layer {
			if err := service.stopCancelledService(ctx, operation.ID, execution, serviceID); err != nil {
				return err
			}
		}
	}
	_, err := service.config.Runtime.TransitionSystem(ctx, operation.ID, execution.system.ID, domain.SystemStopped, time.Now().UTC())
	return err
}

func (service *SingleService) stopCancelledService(ctx context.Context, operationID domain.OperationID, execution *systemStartExecution, serviceID string) error {
	runtime := execution.runtime(serviceID)
	if err := service.stopResolvedRuntime(ctx, operationID, execution.spec, serviceID, &runtime); err != nil {
		return err
	}
	execution.updateRuntime(serviceID, runtime)
	service.releaseServiceLeases(ctx, execution.spec, serviceID)
	return nil
}

func (service *SingleService) markCancelledSystemFailed(ctx context.Context, operation Operation, execution *systemStartExecution, code string) {
	for _, layer := range execution.graph.Layers() {
		for _, serviceID := range layer {
			runtime := execution.runtime(serviceID)
			if runtime.State.CanTransitionTo(domain.ServiceFailed) {
				updated, err := service.config.Runtime.TransitionService(ctx, operation.ID, runtime.ID, runtime.StateVersion, domain.ServiceFailed, code, nil, time.Now().UTC())
				if err == nil {
					execution.updateRuntime(serviceID, *updated)
				}
			}
		}
	}
	_, _ = service.config.Runtime.TransitionSystem(ctx, operation.ID, execution.system.ID, domain.SystemFailed, time.Now().UTC())
}

func (service *SingleService) finishCancelledSteps(ctx context.Context, operationID domain.OperationID) {
	current, err := service.config.Operations.Get(ctx, operationID)
	if err != nil {
		return
	}
	for _, step := range current.Steps {
		if step.State == domain.OperationStepRunning || step.State == domain.OperationStepPending {
			_, _ = service.config.Operations.TransitionStep(ctx, operationID, step.Number, domain.OperationStepCancelled, "", "")
		}
	}
}

func (service *SingleService) releaseServiceLeases(ctx context.Context, spec *ResolvedSystemSpec, serviceID string) {
	for _, logicalName := range ownedLogicalPorts(spec, serviceID) {
		resolved := spec.Ports[logicalName]
		if endpointAvailable(resolved.Port) {
			_ = service.config.PortLeases.Release(ctx, resolved.LeaseID, time.Now().UTC())
		}
	}
}

func endpointAvailable(port int) bool {
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	return listener.Close() == nil
}

func ownedLogicalPorts(spec *ResolvedSystemSpec, serviceID string) []string {
	readinessPrefix := "services." + serviceID + ".readiness."
	composePrefix := "services." + serviceID + ".compose."
	result := make([]string, 0)
	for logicalName, references := range spec.PortReferences {
		for _, reference := range references {
			if strings.HasPrefix(reference, readinessPrefix) || strings.HasPrefix(reference, composePrefix) {
				result = append(result, logicalName)
				break
			}
		}
	}
	sort.Strings(result)
	return result
}

func systemPortRequirements(definitions map[string]manifest.Port) map[string]ports.Requirement {
	result := make(map[string]ports.Requirement, len(definitions))
	for logicalName, definition := range definitions {
		requirement := ports.Requirement{LogicalName: logicalName, Protocol: definition.Protocol, Host: "127.0.0.1", Preferred: copyIntPointer(definition.Preferred), ConflictPolicy: definition.ConflictPolicy}
		if definition.FallbackRange != "" {
			parts := strings.Split(definition.FallbackRange, "-")
			start, _ := strconv.Atoi(parts[0])
			end, _ := strconv.Atoi(parts[1])
			requirement.Fallback = &ports.Range{Start: start, End: end}
		}
		result[logicalName] = requirement
	}
	return result
}

func newSystemRuntime(spec *ResolvedSystemSpec) (domain.SystemInstance, map[string]domain.ServiceInstance, error) {
	now := time.Now().UTC()
	system := domain.SystemInstance{ID: spec.InstanceID, WorkspaceID: spec.WorkspaceID, SystemID: spec.SystemID, ManifestDigest: spec.ManifestDigest, ResolvedSpecDigest: spec.Digest, State: domain.SystemStarting, StartedAt: now}
	runtimes := make(map[string]domain.ServiceInstance, len(spec.Services))
	for _, serviceID := range sortedResolvedNames(spec.Services) {
		id, err := domain.NewServiceInstanceID(now, rand.Reader)
		if err != nil {
			return domain.SystemInstance{}, nil, err
		}
		state := domain.ServiceStarting
		if len(spec.Services[serviceID].Dependencies) > 0 {
			state = domain.ServiceWaitingDependency
		}
		resolved := spec.Services[serviceID]
		mode, timeout := resolved.Process.Mode, resolved.Process.GracefulTimeout
		if resolved.Driver == domain.DriverCompose && resolved.Compose != nil {
			mode, timeout = domain.ProcessDaemon, resolved.Compose.StopTimeout
		}
		runtimes[serviceID] = domain.ServiceInstance{ID: id, SystemInstanceID: spec.InstanceID, ServiceID: domain.ServiceID(serviceID), Driver: resolved.Driver, ProcessMode: mode, State: state, GracefulTimeout: timeout, StateVersion: 1, CreatedAt: now, UpdatedAt: now}
	}
	return system, runtimes, nil
}

func sortedResolvedNames(values map[string]ResolvedService) []string {
	result := make([]string, 0, len(values))
	for serviceID := range values {
		result = append(result, serviceID)
	}
	sort.Strings(result)
	return result
}

func sortedRuntimeNames(values map[string]domain.ServiceInstance) []string {
	result := make([]string, 0, len(values))
	for serviceID := range values {
		result = append(result, serviceID)
	}
	sort.Strings(result)
	return result
}

func stepNumber(operation Operation, key string) int {
	for _, step := range operation.Steps {
		if step.Key == key {
			return step.Number
		}
	}
	return 0
}

func (execution *systemStartExecution) runtime(serviceID string) domain.ServiceInstance {
	execution.mutex.Lock()
	defer execution.mutex.Unlock()
	return execution.runtimes[serviceID]
}

func (execution *systemStartExecution) updateRuntime(serviceID string, runtime domain.ServiceInstance) {
	execution.mutex.Lock()
	execution.runtimes[serviceID] = runtime
	execution.mutex.Unlock()
}

func (execution *systemStartExecution) markCreated(serviceID string) {
	execution.mutex.Lock()
	execution.created[serviceID] = true
	execution.mutex.Unlock()
}

func (execution *systemStartExecution) createdSnapshot() map[string]bool {
	execution.mutex.Lock()
	defer execution.mutex.Unlock()
	result := make(map[string]bool, len(execution.created))
	for serviceID, created := range execution.created {
		result[serviceID] = created
	}
	return result
}

func failureServiceIDs(failure error) []string {
	var systemFailure systemStartFailure
	if !errors.As(failure, &systemFailure) {
		return nil
	}
	result := []string{systemFailure.report.Primary.ServiceID}
	for _, concurrent := range systemFailure.report.Concurrent {
		result = append(result, concurrent.ServiceID)
	}
	return result
}

func firstService(values map[string]bool) string {
	result := ""
	for serviceID, exists := range values {
		if exists && (result == "" || serviceID < result) {
			result = serviceID
		}
	}
	return result
}

func systemStartErrorCode(err error) string {
	var failure systemStartFailure
	if errors.As(err, &failure) && failure.report.Primary.ErrorCode != "" {
		return failure.report.Primary.ErrorCode
	}
	if errors.Is(err, ports.ErrExhausted) || errors.Is(err, ports.ErrLeaseConflict) {
		return "PORT_CONFLICT"
	}
	return singleServiceErrorCode(err)
}
