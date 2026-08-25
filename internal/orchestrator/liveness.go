package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/health"
	"stackpilot/internal/incident"
)

type livenessSystemReader interface {
	GetSystem(context.Context, domain.SystemInstanceID) (*domain.SystemInstance, error)
}

type runtimeLivenessHandler struct {
	owner             *SingleService
	workspaceID       domain.WorkspaceID
	systemID          domain.SystemInstanceID
	systemName        domain.SystemID
	serviceID         domain.ServiceID
	serviceInstanceID domain.ServiceInstanceID
	restart           ResolvedRestartPolicy
}

func (service *SingleService) startSystemLiveness(systemID domain.SystemInstanceID, spec *ResolvedSystemSpec) {
	if service.config.Liveness == nil || spec == nil {
		return
	}
	runtimes, err := service.config.Runtime.ListServices(service.config.Context, systemID)
	if err != nil {
		service.config.Logger.Error("list runtimes for liveness", "instance_id", systemID.String(), "error", err)
		return
	}
	desired := make(map[domain.ServiceInstanceID]bool, len(runtimes))
	for _, runtime := range runtimes {
		resolved, exists := spec.Services[runtime.ServiceID.String()]
		eligible := exists && runtime.ProcessMode == domain.ProcessDaemon && (runtime.State == domain.ServiceReady || runtime.State == domain.ServiceDegraded)
		if !eligible {
			continue
		}
		service.markRuntimeReady(runtime, resolved.Restart)
		if resolved.Liveness != nil {
			desired[runtime.ID] = true
			service.ensureRuntimeLiveness(*spec, runtime, resolved)
		}
	}
	service.stopUndesiredLiveness(systemID, runtimes, desired)
}

func (service *SingleService) ensureRuntimeLiveness(system ResolvedSystemSpec, runtime domain.ServiceInstance, resolved ResolvedService) {
	spec := *resolved.Liveness
	if runtime.Identity != nil {
		spec.Identity = *runtime.Identity
	}
	if runtime.ComposeIdentity != "" {
		spec.ComposeIdentity = runtime.ComposeIdentity
	}
	service.mutex.Lock()
	previous, exists := service.liveness[runtime.ID]
	if exists && previous.spec == spec && previous.restart == resolved.Restart {
		service.mutex.Unlock()
		return
	}
	monitorContext, cancel := context.WithCancel(service.config.Context)
	service.livenessGeneration[runtime.ID]++
	generation := service.livenessGeneration[runtime.ID]
	service.liveness[runtime.ID] = livenessRegistration{cancel: cancel, spec: spec, restart: resolved.Restart}
	service.mutex.Unlock()
	if exists {
		previous.cancel()
	}
	service.waiters.Add(1)
	handler := runtimeLivenessHandler{
		owner: service, workspaceID: system.WorkspaceID, systemID: system.InstanceID,
		systemName: system.SystemID, serviceID: runtime.ServiceID, serviceInstanceID: runtime.ID, restart: resolved.Restart,
	}
	go service.runLivenessMonitor(monitorContext, runtime, spec, generation, handler)
}

func (service *SingleService) runLivenessMonitor(ctx context.Context, runtime domain.ServiceInstance, spec health.ResolvedSpec, generation uint64, handler runtimeLivenessHandler) {
	defer service.waiters.Done()
	err := service.config.Liveness.MonitorLiveness(ctx, health.LivenessRequest{ServiceInstanceID: runtime.ID, InitialState: runtime.State, Spec: spec}, handler)
	if err != nil && !errors.Is(err, context.Canceled) {
		service.config.Logger.Error("liveness monitor stopped", "instance_id", handler.systemID.String(), "service_id", runtime.ServiceID.String(), "error", err)
	}
	service.mutex.Lock()
	if service.livenessGeneration[runtime.ID] == generation {
		delete(service.liveness, runtime.ID)
	}
	service.mutex.Unlock()
}

func (service *SingleService) stopLiveness(id domain.ServiceInstanceID) {
	service.mutex.Lock()
	registration, exists := service.liveness[id]
	delete(service.liveness, id)
	service.livenessGeneration[id]++
	service.mutex.Unlock()
	if exists {
		registration.cancel()
	}
}

func (service *SingleService) markRuntimeReady(runtime domain.ServiceInstance, policy ResolvedRestartPolicy) {
	if policy.Policy == "never" || service.config.RestartAttempts == nil {
		return
	}
	if err := service.config.RestartAttempts.MarkReady(service.config.Context, runtime.ID, time.Now().UTC()); err != nil {
		service.config.Logger.Error("mark automatic restart stable window", "service_id", runtime.ServiceID.String(), "error", err)
	}
}

func (service *SingleService) stopUndesiredLiveness(systemID domain.SystemInstanceID, runtimes []domain.ServiceInstance, desired map[domain.ServiceInstanceID]bool) {
	for _, runtime := range runtimes {
		if runtime.SystemInstanceID == systemID && !desired[runtime.ID] {
			service.stopLiveness(runtime.ID)
		}
	}
}

func (handler runtimeLivenessHandler) HandleLiveness(ctx context.Context, id domain.ServiceInstanceID, transition health.LivenessTransition) error {
	runtime, found, err := handler.owner.config.Runtime.GetService(ctx, id)
	if err != nil || !found {
		return fmt.Errorf("load liveness runtime: %w", err)
	}
	if runtime.State != transition.From {
		return fmt.Errorf("liveness runtime state changed from %s to %s", transition.From, runtime.State)
	}
	code := ""
	if transition.To == domain.ServiceDegraded {
		code = string(transition.Result.ErrorCode)
	}
	if _, err := handler.owner.config.Runtime.TransitionService(ctx, "", id, runtime.StateVersion, transition.To, code, nil, time.Now().UTC()); err != nil {
		return err
	}
	if err := handler.updateAggregate(ctx); err != nil {
		return err
	}
	if transition.To == domain.ServiceReady {
		return handler.markReady(ctx, id)
	}
	handler.reportIncident(ctx, incident.KindLivenessFailure, incident.SeverityWarning, transition.Result)
	return handler.scheduleRestart(ctx, id)
}

func (handler runtimeLivenessHandler) markReady(ctx context.Context, id domain.ServiceInstanceID) error {
	if handler.owner.config.RestartAttempts == nil {
		return nil
	}
	return handler.owner.config.RestartAttempts.MarkReady(ctx, id, time.Now().UTC())
}

func (handler runtimeLivenessHandler) scheduleRestart(ctx context.Context, id domain.ServiceInstanceID) error {
	if handler.restart.Policy == "never" {
		return nil
	}
	runtime, found, err := handler.owner.config.Runtime.GetService(ctx, id)
	reader, ok := handler.owner.config.Runtime.(livenessSystemReader)
	if err != nil || !found || !ok {
		return err
	}
	system, err := reader.GetSystem(ctx, handler.systemID)
	if err != nil {
		return err
	}
	return handler.owner.scheduleAutomaticRestart(ctx, *system, *runtime, handler.restart, "liveness")
}

func (service *SingleService) scheduleAutomaticRestart(ctx context.Context, system domain.SystemInstance, runtime domain.ServiceInstance, policy ResolvedRestartPolicy, source string) error {
	if policy.Policy == "never" || service.config.RestartAttempts == nil {
		return nil
	}
	attempt, allowed, err := service.config.RestartAttempts.Claim(ctx, runtime.ID, time.Now().UTC(), policy.StableWindow, policy.MaxAttempts)
	if err != nil || !allowed {
		if !allowed && err == nil {
			service.config.Logger.Error("automatic restart limit reached", "instance_id", system.ID.String(), "service_id", runtime.ServiceID.String(), "error_code", "RESTART_LIMIT_REACHED")
			service.reportServiceIncident(ctx, system, runtime, incident.KindRestartLimit, incident.SeverityCritical, "RESTART_LIMIT_REACHED", health.Result{CheckedAt: time.Now().UTC()})
		}
		return err
	}
	payload, _ := json.Marshal(map[string]any{"source": source, "attempt": attempt})
	_, err = service.SubmitServiceRestart(ctx, RestartServiceInput{
		WorkspaceID: system.WorkspaceID, SystemID: system.SystemID, ServiceID: runtime.ServiceID,
		IdempotencySubject: "internal", IdempotencyKey: fmt.Sprintf("auto-%s-%d", runtime.ID.String(), attempt),
		Request: payload, Delay: restartBackoff(policy, attempt),
	})
	if err != nil {
		if releaseErr := service.config.RestartAttempts.ReleaseClaim(context.WithoutCancel(ctx), runtime.ID, attempt); releaseErr != nil {
			service.config.Logger.Error("release automatic restart claim", "instance_id", system.ID.String(), "service_id", runtime.ServiceID.String(), "error", releaseErr)
		}
	}
	return err
}

func (handler runtimeLivenessHandler) reportIncident(ctx context.Context, kind incident.Kind, severity incident.Severity, result health.Result) {
	if handler.owner.config.Incidents == nil {
		return
	}
	runtime, found, runtimeErr := handler.owner.config.Runtime.GetService(ctx, handler.serviceInstanceID)
	reader, ok := handler.owner.config.Runtime.(livenessSystemReader)
	if runtimeErr != nil || !found || !ok {
		return
	}
	system, systemErr := reader.GetSystem(ctx, handler.systemID)
	if systemErr != nil {
		return
	}
	handler.owner.reportServiceIncident(ctx, *system, *runtime, kind, severity, string(result.ErrorCode), result)
}

func restartBackoff(policy ResolvedRestartPolicy, attempt int) time.Duration {
	delay := policy.InitialBackoff
	for index := 1; index < attempt && delay < policy.MaxBackoff; index++ {
		if delay > policy.MaxBackoff/2 {
			return policy.MaxBackoff
		}
		delay *= 2
	}
	if delay > policy.MaxBackoff {
		return policy.MaxBackoff
	}
	return delay
}

func waitDelay(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (handler runtimeLivenessHandler) updateAggregate(ctx context.Context) error {
	reader, ok := handler.owner.config.Runtime.(livenessSystemReader)
	if !ok {
		return nil
	}
	system, err := reader.GetSystem(ctx, handler.systemID)
	if err != nil {
		return err
	}
	runtimes, err := handler.owner.config.Runtime.ListServices(ctx, handler.systemID)
	if err != nil {
		return err
	}
	states := make([]domain.ServiceState, 0, len(runtimes))
	for _, runtime := range runtimes {
		states = append(states, runtime.State)
	}
	return handler.owner.reconcileAggregate(ctx, *system, states)
}
