package domain

// OperationType identifies a persisted long-running action.
type OperationType string

const (
	OperationStart          OperationType = "start"
	OperationStop           OperationType = "stop"
	OperationRestart        OperationType = "restart"
	OperationServiceRestart OperationType = "service-restart"
	OperationPortPlan       OperationType = "port-plan"
	OperationRefresh        OperationType = "refresh"
	OperationAnalyze        OperationType = "analyze"
)

// Valid reports whether the operation type is part of the versioned domain contract.
func (value OperationType) Valid() bool {
	switch value {
	case OperationStart, OperationStop, OperationRestart, OperationServiceRestart,
		OperationPortPlan, OperationRefresh, OperationAnalyze:
		return true
	default:
		return false
	}
}

// Validate rejects values outside the operation type contract.
func (value OperationType) Validate() error {
	return validateEnum("operationType", string(value), value.Valid())
}

// OperationState is the lifecycle state of an Operation.
type OperationState string

const (
	OperationQueued     OperationState = "queued"
	OperationRunning    OperationState = "running"
	OperationCancelling OperationState = "cancelling"
	OperationSucceeded  OperationState = "succeeded"
	OperationFailed     OperationState = "failed"
	OperationCancelled  OperationState = "cancelled"
)

// Valid reports whether the operation state is defined.
func (value OperationState) Valid() bool {
	switch value {
	case OperationQueued, OperationRunning, OperationCancelling,
		OperationSucceeded, OperationFailed, OperationCancelled:
		return true
	default:
		return false
	}
}

// Terminal reports whether no later non-terminal state is allowed.
func (value OperationState) Terminal() bool {
	return value == OperationSucceeded || value == OperationFailed || value == OperationCancelled
}

// CanTransitionTo reports whether the Operation state machine permits the target state.
func (value OperationState) CanTransitionTo(target OperationState) bool {
	switch value {
	case OperationQueued:
		return target == OperationRunning || target == OperationCancelled || target == OperationFailed
	case OperationRunning:
		return target == OperationCancelling || target == OperationSucceeded || target == OperationFailed
	case OperationCancelling:
		return target == OperationCancelled || target == OperationFailed
	default:
		return false
	}
}

// Validate rejects values outside the operation state contract.
func (value OperationState) Validate() error {
	return validateEnum("operationState", string(value), value.Valid())
}

// OperationStepState is the lifecycle state of one structured operation step.
type OperationStepState string

const (
	OperationStepPending   OperationStepState = "pending"
	OperationStepRunning   OperationStepState = "running"
	OperationStepSucceeded OperationStepState = "succeeded"
	OperationStepFailed    OperationStepState = "failed"
	OperationStepSkipped   OperationStepState = "skipped"
	OperationStepCancelled OperationStepState = "cancelled"
)

// Valid reports whether the operation step state is defined.
func (value OperationStepState) Valid() bool {
	switch value {
	case OperationStepPending, OperationStepRunning, OperationStepSucceeded,
		OperationStepFailed, OperationStepSkipped, OperationStepCancelled:
		return true
	default:
		return false
	}
}

// Validate rejects values outside the operation step state contract.
func (value OperationStepState) Validate() error {
	return validateEnum("operationStepState", string(value), value.Valid())
}

// CanTransitionTo reports whether a structured step may enter target.
func (value OperationStepState) CanTransitionTo(target OperationStepState) bool {
	switch value {
	case OperationStepPending:
		return target == OperationStepRunning || target == OperationStepSkipped || target == OperationStepCancelled
	case OperationStepRunning:
		return target == OperationStepSucceeded || target == OperationStepFailed || target == OperationStepCancelled
	case OperationStepFailed:
		return target == OperationStepRunning
	default:
		return false
	}
}

// ServiceState is the observed lifecycle state of a service instance.
type ServiceState string

const (
	ServiceStopped           ServiceState = "stopped"
	ServiceWaitingDependency ServiceState = "waiting_dependency"
	ServiceStarting          ServiceState = "starting"
	ServiceWaitingReady      ServiceState = "waiting_ready"
	ServiceReady             ServiceState = "ready"
	ServiceDegraded          ServiceState = "degraded"
	ServiceCompleted         ServiceState = "completed"
	ServiceStopping          ServiceState = "stopping"
	ServiceFailed            ServiceState = "failed"
	ServiceUnknown           ServiceState = "unknown"
)

// Valid reports whether the service state is defined.
func (value ServiceState) Valid() bool {
	switch value {
	case ServiceStopped, ServiceWaitingDependency, ServiceStarting, ServiceWaitingReady,
		ServiceReady, ServiceDegraded, ServiceCompleted, ServiceStopping, ServiceFailed, ServiceUnknown:
		return true
	default:
		return false
	}
}

// Validate rejects values outside the service state contract.
func (value ServiceState) Validate() error {
	return validateEnum("serviceState", string(value), value.Valid())
}

// CanTransitionTo reports whether a persisted service instance may enter target.
func (value ServiceState) CanTransitionTo(target ServiceState) bool {
	switch value {
	case ServiceStopped:
		return target == ServiceStarting
	case ServiceWaitingDependency:
		return target == ServiceStarting || target == ServiceStopped || target == ServiceFailed
	case ServiceStarting:
		return target == ServiceWaitingReady || target == ServiceCompleted || target == ServiceStopping || target == ServiceFailed || target == ServiceUnknown
	case ServiceWaitingReady:
		return target == ServiceReady || target == ServiceCompleted || target == ServiceStopping || target == ServiceFailed || target == ServiceUnknown
	case ServiceReady:
		return target == ServiceDegraded || target == ServiceStopping || target == ServiceFailed || target == ServiceUnknown
	case ServiceDegraded:
		return target == ServiceReady || target == ServiceStopping || target == ServiceFailed || target == ServiceUnknown
	case ServiceCompleted:
		return target == ServiceStopped
	case ServiceStopping:
		return target == ServiceStopped || target == ServiceFailed || target == ServiceUnknown
	case ServiceFailed, ServiceUnknown:
		return target == ServiceStopping || target == ServiceStopped
	default:
		return false
	}
}

// Satisfies reports whether this state releases a dependency with the given condition.
func (value ServiceState) Satisfies(condition DependencyCondition) bool {
	switch condition {
	case DependencyReady:
		return value == ServiceReady
	case DependencyCompleted:
		return value == ServiceCompleted
	default:
		return false
	}
}

// SystemState is the server-computed aggregate state exposed to clients.
type SystemState string

const (
	SystemStopping SystemState = "stopping"
	SystemFailed   SystemState = "failed"
	SystemStarting SystemState = "starting"
	SystemDegraded SystemState = "degraded"
	SystemRunning  SystemState = "running"
	SystemStopped  SystemState = "stopped"
)

// Valid reports whether the aggregate system state is defined.
func (value SystemState) Valid() bool {
	switch value {
	case SystemStopping, SystemFailed, SystemStarting, SystemDegraded, SystemRunning, SystemStopped:
		return true
	default:
		return false
	}
}

// Validate rejects values outside the system state contract.
func (value SystemState) Validate() error {
	return validateEnum("systemState", string(value), value.Valid())
}

// CanTransitionTo reports whether an aggregate runtime state may enter target.
func (value SystemState) CanTransitionTo(target SystemState) bool {
	switch value {
	case SystemStarting:
		return target == SystemRunning || target == SystemDegraded || target == SystemFailed || target == SystemStopping
	case SystemRunning:
		return target == SystemDegraded || target == SystemFailed || target == SystemStopping
	case SystemDegraded:
		return target == SystemRunning || target == SystemFailed || target == SystemStopping
	case SystemFailed:
		return target == SystemStopping
	case SystemStopping:
		return target == SystemStopped || target == SystemFailed
	default:
		return false
	}
}

// DependencyCondition controls when an upstream service releases a dependent service.
type DependencyCondition string

const (
	DependencyReady     DependencyCondition = "ready"
	DependencyCompleted DependencyCondition = "completed"
)

// Valid reports whether the dependency condition is defined.
func (value DependencyCondition) Valid() bool {
	return value == DependencyReady || value == DependencyCompleted
}

// Validate rejects values outside the dependency condition contract.
func (value DependencyCondition) Validate() error {
	return validateEnum("dependencyCondition", string(value), value.Valid())
}

// DriverKind identifies the runtime driver selected by a service definition.
type DriverKind string

const (
	DriverProcess DriverKind = "process"
	DriverCompose DriverKind = "compose"
)

// Valid reports whether the driver kind is defined.
func (value DriverKind) Valid() bool { return value == DriverProcess || value == DriverCompose }

// Validate rejects values outside the driver kind contract.
func (value DriverKind) Validate() error {
	return validateEnum("driverKind", string(value), value.Valid())
}

// ProcessMode identifies the lifecycle semantics of a process service.
type ProcessMode string

const (
	ProcessDaemon  ProcessMode = "daemon"
	ProcessOneshot ProcessMode = "oneshot"
)

// Valid reports whether the process mode is defined.
func (value ProcessMode) Valid() bool { return value == ProcessDaemon || value == ProcessOneshot }

// Validate rejects values outside the process mode contract.
func (value ProcessMode) Validate() error {
	return validateEnum("processMode", string(value), value.Valid())
}

func validateEnum(field, value string, valid bool) error {
	if !valid {
		return newInvalidValue(field, value, ErrInvalidEnumValue)
	}
	return nil
}
