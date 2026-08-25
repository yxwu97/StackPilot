package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/events"
)

var (
	// ErrRuntimeInstanceNotFound identifies a missing persisted runtime.
	ErrRuntimeInstanceNotFound = errors.New("runtime instance was not found")
	// ErrRuntimeStateConflict identifies a stale version or illegal runtime transition.
	ErrRuntimeStateConflict = errors.New("runtime state changed concurrently")
	// ErrActiveSystemInstance identifies the one-active-instance workspace invariant.
	ErrActiveSystemInstance = errors.New("workspace already has an active system instance")
)

// RuntimeInstanceRepository persists runtime identity and state/event transactions.
type RuntimeInstanceRepository struct {
	database *sql.DB
	notifier events.Notifier
}

// NewRuntimeInstanceRepository constructs an instance repository over migration 3 or later.
func NewRuntimeInstanceRepository(database *sql.DB, notifier events.Notifier) (*RuntimeInstanceRepository, error) {
	if database == nil {
		return nil, fmt.Errorf("runtime instance repository database is required")
	}
	return &RuntimeInstanceRepository{database: database, notifier: notifier}, nil
}

// Create atomically inserts one system/service pair and their creation events.
func (repository *RuntimeInstanceRepository) Create(ctx context.Context, operationID domain.OperationID, system domain.SystemInstance, service domain.ServiceInstance) error {
	return repository.CreateSystem(ctx, operationID, system, []domain.ServiceInstance{service})
}

// CreateSystem atomically inserts one system, all service instances, and their creation events.
func (repository *RuntimeInstanceRepository) CreateSystem(ctx context.Context, operationID domain.OperationID, system domain.SystemInstance, services []domain.ServiceInstance) error {
	if err := validateNewSystemRuntime(operationID, system, services); err != nil {
		return err
	}
	var eventIDs []domain.EventID
	err := executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		if err := requireNoActiveSystem(ctx, connection, system.WorkspaceID); err != nil {
			return err
		}
		if err := insertSystemInstance(ctx, connection, system); err != nil {
			return err
		}
		systemEvent, err := events.SystemInstanceCreated(operationID, system)
		if err != nil {
			return err
		}
		id, err := insertEvent(ctx, connection, systemEvent)
		if err != nil {
			return err
		}
		eventIDs = append(eventIDs, id)
		return insertNewServices(ctx, connection, operationID, system, services, &eventIDs)
	})
	if err != nil {
		return err
	}
	repository.notify(eventIDs)
	return nil
}

func insertNewServices(ctx context.Context, connection *sql.Conn, operationID domain.OperationID, system domain.SystemInstance, services []domain.ServiceInstance, eventIDs *[]domain.EventID) error {
	for _, service := range services {
		if err := insertServiceInstance(ctx, connection, service); err != nil {
			return err
		}
		event, err := events.ServiceInstanceCreated(operationID, system, service)
		if err != nil {
			return err
		}
		id, err := insertEvent(ctx, connection, event)
		if err != nil {
			return err
		}
		*eventIDs = append(*eventIDs, id)
	}
	return nil
}

func requireNoActiveSystem(ctx context.Context, connection *sql.Conn, workspaceID domain.WorkspaceID) error {
	var existing string
	err := connection.QueryRowContext(ctx, `SELECT id FROM system_instances
        WHERE workspace_id = ? AND state <> 'stopped'`, workspaceID.String()).Scan(&existing)
	if err == nil {
		return ErrActiveSystemInstance
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return fmt.Errorf("check active system instance: %w", err)
}

// GetSystem returns one system instance and validates its persisted identifiers and state.
func (repository *RuntimeInstanceRepository) GetSystem(ctx context.Context, id domain.SystemInstanceID) (*domain.SystemInstance, error) {
	row := repository.database.QueryRowContext(ctx, systemInstanceSelect+` WHERE si.id = ?`, id.String())
	instance, err := scanSystemInstance(row)
	return runtimeSystemResult(instance, err)
}

// GetActive returns the non-stopped instance for a workspace, if one exists.
func (repository *RuntimeInstanceRepository) GetActive(ctx context.Context, workspaceID domain.WorkspaceID) (*domain.SystemInstance, bool, error) {
	row := repository.database.QueryRowContext(ctx, systemInstanceSelect+` WHERE si.workspace_id = ? AND si.state <> 'stopped'`, workspaceID.String())
	instance, err := scanSystemInstance(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get active system instance: %w", err)
	}
	return &instance, true, nil
}

// ListActive returns every non-stopped system instance in stable start order for reconciliation.
func (repository *RuntimeInstanceRepository) ListActive(ctx context.Context) ([]domain.SystemInstance, error) {
	rows, err := repository.database.QueryContext(ctx, systemInstanceSelect+` WHERE si.state <> 'stopped' ORDER BY si.started_at, si.id`)
	if err != nil {
		return nil, fmt.Errorf("list active system instances: %w", err)
	}
	defer rows.Close()
	instances := make([]domain.SystemInstance, 0)
	for rows.Next() {
		instance, err := scanSystemInstance(rows)
		if err != nil {
			return nil, fmt.Errorf("scan active system instance: %w", err)
		}
		instances = append(instances, instance)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active system instances: %w", err)
	}
	return instances, nil
}

// MarkReconciled records completion of a runtime identity scan.
func (repository *RuntimeInstanceRepository) MarkReconciled(ctx context.Context, id domain.SystemInstanceID, now time.Time) error {
	if _, err := domain.ParseSystemInstanceID(id.String()); err != nil || now.IsZero() || now.Location() != time.UTC {
		return fmt.Errorf("invalid reconciliation marker")
	}
	result, err := repository.database.ExecContext(ctx, `UPDATE system_instances SET last_reconciled_at = ? WHERE id = ?`,
		formatDatabaseTime(now), id.String())
	if err != nil {
		return fmt.Errorf("mark system instance reconciled: %w", err)
	}
	return requireRuntimeTransition(result)
}

// GetService returns one service runtime by its concrete identifier.
func (repository *RuntimeInstanceRepository) GetService(ctx context.Context, id domain.ServiceInstanceID) (*domain.ServiceInstance, bool, error) {
	service, err := scanServiceInstance(repository.database.QueryRowContext(ctx, serviceInstanceSelect+` WHERE id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get service instance: %w", err)
	}
	return &service, true, nil
}

// LatestServiceEvent returns the newest durable event cursor for one service instance.
func (repository *RuntimeInstanceRepository) LatestServiceEvent(ctx context.Context, id domain.ServiceInstanceID) (domain.EventID, bool, error) {
	if _, err := domain.ParseServiceInstanceID(id.String()); err != nil {
		return 0, false, err
	}
	var value int64
	err := repository.database.QueryRowContext(ctx, `SELECT id FROM events WHERE service_instance_id=? ORDER BY id DESC LIMIT 1`, id.String()).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read latest service event: %w", err)
	}
	eventID, err := domain.NewEventID(value)
	return eventID, err == nil, err
}

// ListServices returns stable service runtime order for one system instance.
func (repository *RuntimeInstanceRepository) ListServices(ctx context.Context, id domain.SystemInstanceID) ([]domain.ServiceInstance, error) {
	rows, err := repository.database.QueryContext(ctx, serviceInstanceSelect+` WHERE system_instance_id = ? ORDER BY service_id`, id.String())
	if err != nil {
		return nil, fmt.Errorf("list service instances: %w", err)
	}
	defer rows.Close()
	result := make([]domain.ServiceInstance, 0)
	for rows.Next() {
		service, err := scanServiceInstance(rows)
		if err != nil {
			return nil, fmt.Errorf("scan service instance: %w", err)
		}
		result = append(result, service)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate service instances: %w", err)
	}
	return result, nil
}

// AttachIdentity stores proven process identity and enters waiting_ready atomically with an event.
func (repository *RuntimeInstanceRepository) AttachIdentity(ctx context.Context, operationID domain.OperationID, id domain.ServiceInstanceID, version int64, identity domain.ProcessIdentity, now time.Time) (*domain.ServiceInstance, error) {
	return repository.transitionService(ctx, operationID, id, version, domain.ServiceWaitingReady, "", nil, &identity, "", now)
}

// AttachComposeIdentity stores one opaque Compose project identity and enters waiting_ready atomically.
func (repository *RuntimeInstanceRepository) AttachComposeIdentity(ctx context.Context, operationID domain.OperationID, id domain.ServiceInstanceID, version int64, identity string, now time.Time) (*domain.ServiceInstance, error) {
	if identity == "" || len(identity) > 65536 {
		return nil, fmt.Errorf("Compose project identity is invalid")
	}
	return repository.transitionService(ctx, operationID, id, version, domain.ServiceWaitingReady, "", nil, nil, identity, now)
}

// AttachComposeStopIdentity stores a discovered project identity while atomically entering stopping.
func (repository *RuntimeInstanceRepository) AttachComposeStopIdentity(ctx context.Context, operationID domain.OperationID, id domain.ServiceInstanceID, version int64, identity string, now time.Time) (*domain.ServiceInstance, error) {
	if identity == "" || len(identity) > 65536 {
		return nil, fmt.Errorf("Compose project identity is invalid")
	}
	return repository.transitionService(ctx, operationID, id, version, domain.ServiceStopping, "", nil, nil, identity, now)
}

// TransitionService applies one legal optimistic state transition and commits its event.
func (repository *RuntimeInstanceRepository) TransitionService(ctx context.Context, operationID domain.OperationID, id domain.ServiceInstanceID, version int64, target domain.ServiceState, code string, exitCode *uint32, now time.Time) (*domain.ServiceInstance, error) {
	return repository.transitionService(ctx, operationID, id, version, target, code, exitCode, nil, "", now)
}

func (repository *RuntimeInstanceRepository) transitionService(ctx context.Context, operationID domain.OperationID, id domain.ServiceInstanceID, version int64, target domain.ServiceState, code string, exitCode *uint32, identity *domain.ProcessIdentity, composeIdentity string, now time.Time) (*domain.ServiceInstance, error) {
	var eventID domain.EventID
	err := executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		service, system, err := runtimePair(ctx, connection, id)
		if err != nil {
			return err
		}
		if service.StateVersion != version || !service.State.CanTransitionTo(target) {
			return ErrRuntimeStateConflict
		}
		if err := updateServiceInstance(ctx, connection, service, target, exitCode, identity, composeIdentity, now); err != nil {
			return err
		}
		event, err := events.ServiceStateChanged(operationID, system, service, target, code, now)
		if err != nil {
			return err
		}
		eventID, err = insertEvent(ctx, connection, event)
		return err
	})
	if err != nil {
		return nil, err
	}
	repository.notify([]domain.EventID{eventID})
	return repository.getServiceRequired(ctx, id)
}

func (repository *RuntimeInstanceRepository) getServiceRequired(ctx context.Context, id domain.ServiceInstanceID) (*domain.ServiceInstance, error) {
	service, found, err := repository.GetService(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrRuntimeInstanceNotFound
	}
	return service, nil
}

// TransitionSystem applies one legal aggregate transition and commits its event.
func (repository *RuntimeInstanceRepository) TransitionSystem(ctx context.Context, operationID domain.OperationID, id domain.SystemInstanceID, target domain.SystemState, now time.Time) (*domain.SystemInstance, error) {
	var eventID domain.EventID
	err := executeImmediate(ctx, repository.database, func(connection *sql.Conn) error {
		current, err := getSystemOnConnection(ctx, connection, id)
		if err != nil {
			return err
		}
		if !current.State.CanTransitionTo(target) {
			return ErrRuntimeStateConflict
		}
		if err := updateSystemInstance(ctx, connection, current, target, now); err != nil {
			return err
		}
		event, err := events.SystemStateChanged(operationID, current, target, now)
		if err != nil {
			return err
		}
		eventID, err = insertEvent(ctx, connection, event)
		return err
	})
	if err != nil {
		return nil, err
	}
	repository.notify([]domain.EventID{eventID})
	return repository.GetSystem(ctx, id)
}

func validateNewRuntime(operationID domain.OperationID, system domain.SystemInstance, service domain.ServiceInstance) error {
	return validateNewSystemRuntime(operationID, system, []domain.ServiceInstance{service})
}

func validateNewSystemRuntime(operationID domain.OperationID, system domain.SystemInstance, services []domain.ServiceInstance) error {
	if _, err := domain.ParseOperationID(operationID.String()); err != nil {
		return err
	}
	if _, err := domain.ParseSystemInstanceID(system.ID.String()); err != nil {
		return err
	}
	if _, err := domain.ParseWorkspaceID(system.WorkspaceID.String()); err != nil {
		return err
	}
	if _, err := domain.ParseSystemID(system.SystemID.String()); err != nil {
		return err
	}
	if len(services) == 0 || system.State != domain.SystemStarting {
		return ErrRuntimeStateConflict
	}
	seen := make(map[domain.ServiceID]bool, len(services))
	for _, service := range services {
		if err := validateNewServiceRuntime(system, service, seen); err != nil {
			return err
		}
	}
	if len(system.ManifestDigest) != 64 || len(system.ResolvedSpecDigest) != 64 || system.StartedAt.Location() != time.UTC {
		return fmt.Errorf("runtime instance metadata is invalid")
	}
	return nil
}

func validateNewServiceRuntime(system domain.SystemInstance, service domain.ServiceInstance, seen map[domain.ServiceID]bool) error {
	if _, err := domain.ParseServiceInstanceID(service.ID.String()); err != nil {
		return err
	}
	if _, err := domain.ParseServiceID(service.ServiceID.String()); err != nil {
		return err
	}
	if seen[service.ServiceID] || service.SystemInstanceID != system.ID || service.StateVersion != 1 {
		return ErrRuntimeStateConflict
	}
	seen[service.ServiceID] = true
	if service.State != domain.ServiceStarting && service.State != domain.ServiceWaitingDependency {
		return ErrRuntimeStateConflict
	}
	if service.GracefulTimeout <= 0 || service.GracefulTimeout > 10*time.Minute {
		return fmt.Errorf("runtime stop policy is invalid")
	}
	if err := service.ProcessMode.Validate(); err != nil {
		return fmt.Errorf("runtime process mode is invalid: %w", err)
	}
	if err := service.Driver.Validate(); err != nil {
		return fmt.Errorf("runtime driver is invalid: %w", err)
	}
	if service.Identity != nil || service.ComposeIdentity != "" {
		return fmt.Errorf("new runtime identity must be empty")
	}
	if service.CreatedAt.Location() != time.UTC || service.UpdatedAt.Location() != time.UTC {
		return fmt.Errorf("runtime service metadata is invalid")
	}
	return nil
}

func insertSystemInstance(ctx context.Context, connection *sql.Conn, instance domain.SystemInstance) error {
	_, err := connection.ExecContext(ctx, `INSERT INTO system_instances
        (id, workspace_id, manifest_digest, resolved_spec_digest, state, started_at)
        VALUES (?, ?, ?, ?, ?, ?)`, instance.ID.String(), instance.WorkspaceID.String(), instance.ManifestDigest,
		instance.ResolvedSpecDigest, string(instance.State), formatDatabaseTime(instance.StartedAt))
	if err != nil {
		return fmt.Errorf("insert system instance: %w", err)
	}
	return nil
}

func insertServiceInstance(ctx context.Context, connection *sql.Conn, instance domain.ServiceInstance) error {
	_, err := connection.ExecContext(ctx, `INSERT INTO service_instances
		(id, system_instance_id, service_id, driver, process_mode, state, graceful_timeout_ms, state_version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, instance.ID.String(), instance.SystemInstanceID.String(), instance.ServiceID.String(),
		string(instance.Driver), string(instance.ProcessMode), string(instance.State), instance.GracefulTimeout.Milliseconds(), instance.StateVersion,
		formatDatabaseTime(instance.CreatedAt), formatDatabaseTime(instance.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert service instance: %w", err)
	}
	return nil
}

const systemInstanceSelect = `SELECT si.id, si.workspace_id, w.system_id, si.manifest_digest,
    si.resolved_spec_digest, si.state, si.started_at, si.stopped_at, si.last_reconciled_at
    FROM system_instances si JOIN workspaces w ON w.id = si.workspace_id`

func scanSystemInstance(scanner rowScanner) (domain.SystemInstance, error) {
	var instance domain.SystemInstance
	var id, workspaceID, systemID, state, startedAt string
	var stoppedAt, reconciledAt sql.NullString
	err := scanner.Scan(&id, &workspaceID, &systemID, &instance.ManifestDigest, &instance.ResolvedSpecDigest,
		&state, &startedAt, &stoppedAt, &reconciledAt)
	if err != nil {
		return domain.SystemInstance{}, err
	}
	instance.ID, instance.WorkspaceID, instance.SystemID = domain.SystemInstanceID(id), domain.WorkspaceID(workspaceID), domain.SystemID(systemID)
	instance.State = domain.SystemState(state)
	if instance.StartedAt, err = parseDatabaseTime(startedAt); err != nil {
		return domain.SystemInstance{}, err
	}
	if instance.StoppedAt, err = parseNullableDatabaseTime(stoppedAt); err != nil {
		return domain.SystemInstance{}, err
	}
	instance.LastReconciledAt, err = parseNullableDatabaseTime(reconciledAt)
	return instance, err
}

const serviceInstanceSelect = `SELECT id, system_instance_id, service_id, driver, process_mode, state, pid, process_started_at,
    executable_path, command_digest, platform_token, exit_code, graceful_timeout_ms, state_version,
    created_at, updated_at, compose_project_token FROM service_instances`

func scanServiceInstance(scanner rowScanner) (domain.ServiceInstance, error) {
	var service domain.ServiceInstance
	var id, systemID, serviceID, driverKind, processMode, state, createdAt, updatedAt string
	var pid, exitCode sql.NullInt64
	var gracefulTimeoutMillis int64
	var processStarted, executable, digest, token, composeToken sql.NullString
	err := scanner.Scan(&id, &systemID, &serviceID, &driverKind, &processMode, &state, &pid, &processStarted, &executable,
		&digest, &token, &exitCode, &gracefulTimeoutMillis, &service.StateVersion, &createdAt, &updatedAt, &composeToken)
	if err != nil {
		return domain.ServiceInstance{}, err
	}
	service.ID, service.SystemInstanceID, service.ServiceID = domain.ServiceInstanceID(id), domain.SystemInstanceID(systemID), domain.ServiceID(serviceID)
	service.Driver = domain.DriverKind(driverKind)
	service.ProcessMode = domain.ProcessMode(processMode)
	service.ComposeIdentity = composeToken.String
	service.State = domain.ServiceState(state)
	service.GracefulTimeout = time.Duration(gracefulTimeoutMillis) * time.Millisecond
	if pid.Valid {
		started, err := parseDatabaseTime(processStarted.String)
		if err != nil {
			return domain.ServiceInstance{}, err
		}
		service.Identity = &domain.ProcessIdentity{PID: int(pid.Int64), StartedAt: started, ExecutablePath: executable.String, CommandDigest: digest.String, PlatformToken: token.String}
	}
	if exitCode.Valid {
		value := uint32(exitCode.Int64)
		service.ExitCode = &value
	}
	if service.CreatedAt, err = parseDatabaseTime(createdAt); err != nil {
		return domain.ServiceInstance{}, err
	}
	service.UpdatedAt, err = parseDatabaseTime(updatedAt)
	return service, err
}

func runtimePair(ctx context.Context, connection *sql.Conn, id domain.ServiceInstanceID) (domain.ServiceInstance, domain.SystemInstance, error) {
	service, err := scanServiceInstance(connection.QueryRowContext(ctx, serviceInstanceSelect+` WHERE id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ServiceInstance{}, domain.SystemInstance{}, ErrRuntimeInstanceNotFound
	}
	if err != nil {
		return domain.ServiceInstance{}, domain.SystemInstance{}, fmt.Errorf("read service transition state: %w", err)
	}
	system, err := getSystemOnConnection(ctx, connection, service.SystemInstanceID)
	return service, system, err
}

func getSystemOnConnection(ctx context.Context, connection *sql.Conn, id domain.SystemInstanceID) (domain.SystemInstance, error) {
	instance, err := scanSystemInstance(connection.QueryRowContext(ctx, systemInstanceSelect+` WHERE si.id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SystemInstance{}, ErrRuntimeInstanceNotFound
	}
	if err != nil {
		return domain.SystemInstance{}, fmt.Errorf("read system transition state: %w", err)
	}
	return instance, nil
}

func updateServiceInstance(ctx context.Context, connection *sql.Conn, current domain.ServiceInstance, target domain.ServiceState, exitCode *uint32, identity *domain.ProcessIdentity, composeIdentity string, now time.Time) error {
	pid, processStarted, executable, digest, token := identityValues(current.Identity, identity)
	composeToken := nullableComposeIdentity(current.ComposeIdentity, composeIdentity)
	if target == domain.ServiceCompleted || target == domain.ServiceStopped || (target == domain.ServiceFailed && exitCode != nil) {
		pid, processStarted, executable, digest, token = nil, nil, nil, nil, nil
		composeToken = nil
	}
	result, err := connection.ExecContext(ctx, `UPDATE service_instances SET state = ?, pid = ?, process_started_at = ?,
        executable_path = ?, command_digest = ?, platform_token = ?, compose_project_token = ?, exit_code = ?, state_version = state_version + 1,
        updated_at = ? WHERE id = ? AND state_version = ?`, string(target), pid, processStarted, executable, digest, token, composeToken,
		nullableExitCode(exitCode), formatDatabaseTime(now), current.ID.String(), current.StateVersion)
	if err != nil {
		return fmt.Errorf("update service instance: %w", err)
	}
	return requireRuntimeTransition(result)
}

func nullableComposeIdentity(current, replacement string) any {
	if replacement != "" {
		return replacement
	}
	if current != "" {
		return current
	}
	return nil
}

func identityValues(current, replacement *domain.ProcessIdentity) (any, any, any, any, any) {
	identity := current
	if replacement != nil {
		identity = replacement
	}
	if identity == nil {
		return nil, nil, nil, nil, nil
	}
	return identity.PID, formatDatabaseTime(identity.StartedAt), identity.ExecutablePath, identity.CommandDigest, identity.PlatformToken
}

func updateSystemInstance(ctx context.Context, connection *sql.Conn, current domain.SystemInstance, target domain.SystemState, now time.Time) error {
	var stoppedAt any
	if target == domain.SystemStopped {
		stoppedAt = formatDatabaseTime(now)
	}
	result, err := connection.ExecContext(ctx, `UPDATE system_instances SET state = ?, stopped_at = ? WHERE id = ? AND state = ?`,
		string(target), stoppedAt, current.ID.String(), string(current.State))
	if err != nil {
		return fmt.Errorf("update system instance: %w", err)
	}
	return requireRuntimeTransition(result)
}

func requireRuntimeTransition(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect runtime transition: %w", err)
	}
	if affected != 1 {
		return ErrRuntimeStateConflict
	}
	return nil
}

func nullableExitCode(code *uint32) any {
	if code == nil {
		return nil
	}
	return int64(*code)
}

func runtimeSystemResult(instance domain.SystemInstance, err error) (*domain.SystemInstance, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRuntimeInstanceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get system instance: %w", err)
	}
	return &instance, nil
}

func (repository *RuntimeInstanceRepository) notify(ids []domain.EventID) {
	if repository.notifier == nil {
		return
	}
	for _, id := range ids {
		if id > 0 {
			repository.notifier.Notify(id)
		}
	}
}
