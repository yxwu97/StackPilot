package api

import (
	"encoding/json"
	"net/http"
)

// ErrorCode is a stable machine-readable API error identifier.
type ErrorCode string

const (
	ErrorRequestValidationFailed       ErrorCode = "REQUEST_VALIDATION_FAILED"
	ErrorResourceNotFound              ErrorCode = "RESOURCE_NOT_FOUND"
	ErrorMethodNotAllowed              ErrorCode = "METHOD_NOT_ALLOWED"
	ErrorFeatureNotEnabled             ErrorCode = "FEATURE_NOT_ENABLED"
	ErrorEventCursorExpired            ErrorCode = "EVENT_CURSOR_EXPIRED"
	ErrorHealthNotReady                ErrorCode = "HEALTH_NOT_READY"
	ErrorHealthReadinessTimeout        ErrorCode = "HEALTH_READINESS_TIMEOUT"
	ErrorProcessExited                 ErrorCode = "PROCESS_EXITED"
	ErrorProcessIdentityMismatch       ErrorCode = "PROCESS_IDENTITY_MISMATCH"
	ErrorControlPlaneRestarted         ErrorCode = "CONTROL_PLANE_RESTARTED"
	ErrorComposeConfigInvalid          ErrorCode = "COMPOSE_CONFIG_INVALID"
	ErrorComposeBuildConfigInvalid     ErrorCode = "COMPOSE_BUILD_CONFIG_INVALID"
	ErrorComposeBuildFailed            ErrorCode = "COMPOSE_BUILD_FAILED"
	ErrorComposeBuildTimeout           ErrorCode = "COMPOSE_BUILD_TIMEOUT"
	ErrorComposeDiscoveryFailed        ErrorCode = "COMPOSE_DISCOVERY_FAILED"
	ErrorComposeInspectFailed          ErrorCode = "COMPOSE_INSPECT_FAILED"
	ErrorComposeLogFollowFailed        ErrorCode = "COMPOSE_LOG_FOLLOW_FAILED"
	ErrorComposeLifecycleInvalid       ErrorCode = "COMPOSE_LIFECYCLE_INVALID"
	ErrorComposeLifecycleTimeout       ErrorCode = "COMPOSE_LIFECYCLE_TIMEOUT"
	ErrorComposeNotFound               ErrorCode = "COMPOSE_NOT_FOUND"
	ErrorComposeOverrideConflict       ErrorCode = "COMPOSE_OVERRIDE_CONFLICT"
	ErrorComposeOverrideInvalid        ErrorCode = "COMPOSE_OVERRIDE_INVALID"
	ErrorComposePreflightTimeout       ErrorCode = "COMPOSE_PREFLIGHT_TIMEOUT"
	ErrorComposeProjectMismatch        ErrorCode = "COMPOSE_PROJECT_IDENTITY_MISMATCH"
	ErrorComposeProjectNotFound        ErrorCode = "COMPOSE_PROJECT_NOT_FOUND"
	ErrorComposeStartFailed            ErrorCode = "COMPOSE_START_FAILED"
	ErrorComposeStopFailed             ErrorCode = "COMPOSE_STOP_FAILED"
	ErrorComposeVersionUnsupported     ErrorCode = "COMPOSE_VERSION_UNSUPPORTED"
	ErrorDockerDaemonUnavailable       ErrorCode = "DOCKER_DAEMON_UNAVAILABLE"
	ErrorDockerNotFound                ErrorCode = "DOCKER_NOT_FOUND"
	ErrorDockerVersionUnsupported      ErrorCode = "DOCKER_VERSION_UNSUPPORTED"
	ErrorContainerUnhealthy            ErrorCode = "CONTAINER_UNHEALTHY"
	ErrorSupervisorExited              ErrorCode = "SUPERVISOR_EXITED"
	ErrorSupervisorUnavailable         ErrorCode = "SUPERVISOR_UNAVAILABLE"
	ErrorProcessStartFailed            ErrorCode = "PROCESS_START_FAILED"
	ErrorProcessStopFailed             ErrorCode = "PROCESS_STOP_FAILED"
	ErrorPortConflict                  ErrorCode = "PORT_CONFLICT"
	ErrorTCPRefused                    ErrorCode = "TCP_REFUSED"
	ErrorTCPTimeout                    ErrorCode = "TCP_TIMEOUT"
	ErrorHTTPStatusMismatch            ErrorCode = "HTTP_STATUS_MISMATCH"
	ErrorHTTPBodyMismatch              ErrorCode = "HTTP_BODY_MISMATCH"
	ErrorHTTPTimeout                   ErrorCode = "HTTP_TIMEOUT"
	ErrorLogCursorExpired              ErrorCode = "LOG_CURSOR_EXPIRED"
	ErrorManifestFileTooLarge          ErrorCode = "MANIFEST_FILE_TOO_LARGE"
	ErrorManifestNotRegularFile        ErrorCode = "MANIFEST_NOT_REGULAR_FILE"
	ErrorManifestYAMLInvalid           ErrorCode = "MANIFEST_YAML_INVALID"
	ErrorManifestDuplicateKey          ErrorCode = "MANIFEST_DUPLICATE_KEY"
	ErrorManifestUnknownField          ErrorCode = "MANIFEST_UNKNOWN_FIELD"
	ErrorManifestMultipleDocs          ErrorCode = "MANIFEST_MULTIPLE_DOCUMENTS"
	ErrorManifestSchemaInvalid         ErrorCode = "MANIFEST_SCHEMA_INVALID"
	ErrorManifestSemanticInvalid       ErrorCode = "MANIFEST_SEMANTIC_INVALID"
	ErrorManifestPathOutside           ErrorCode = "MANIFEST_PATH_OUTSIDE_WORKSPACE"
	ErrorManifestCycleDetected         ErrorCode = "MANIFEST_CYCLE_DETECTED"
	ErrorManifestReferenceAbsent       ErrorCode = "MANIFEST_REFERENCE_NOT_FOUND"
	ErrorManifestTemplateInvalid       ErrorCode = "MANIFEST_TEMPLATE_INVALID"
	ErrorManifestDurationInvalid       ErrorCode = "MANIFEST_DURATION_INVALID"
	ErrorManifestPortRange             ErrorCode = "MANIFEST_PORT_RANGE_INVALID"
	ErrorManifestHealthUnsafe          ErrorCode = "MANIFEST_HEALTH_TARGET_UNSAFE"
	ErrorManifestChanged               ErrorCode = "MANIFEST_CHANGED"
	ErrorWorkspaceAlreadyExists        ErrorCode = "WORKSPACE_ALREADY_REGISTERED"
	ErrorWorkspaceNotFound             ErrorCode = "WORKSPACE_NOT_FOUND"
	ErrorWorkspaceManifestAbsent       ErrorCode = "WORKSPACE_MANIFEST_UNAVAILABLE"
	ErrorWorkspaceSystemChanged        ErrorCode = "WORKSPACE_SYSTEM_ID_CHANGED"
	ErrorWorkspaceIDRequired           ErrorCode = "WORKSPACE_ID_REQUIRED"
	ErrorWorkspacePathInvalid          ErrorCode = "WORKSPACE_PATH_INVALID"
	ErrorWorkspaceScriptNotFound       ErrorCode = "WORKSPACE_SCRIPT_NOT_FOUND"
	ErrorWorkspaceScriptOutside        ErrorCode = "WORKSPACE_SCRIPT_OUTSIDE"
	ErrorWorkspaceScriptType           ErrorCode = "WORKSPACE_SCRIPT_TYPE_UNSUPPORTED"
	ErrorWorkspaceScriptEncoding       ErrorCode = "WORKSPACE_SCRIPT_ENCODING_UNSUPPORTED"
	ErrorWorkspaceScriptTooLarge       ErrorCode = "WORKSPACE_SCRIPT_TOO_LARGE"
	ErrorWorkspaceScriptSyntax         ErrorCode = "WORKSPACE_SCRIPT_SYNTAX_UNSUPPORTED"
	ErrorWorkspaceScriptDangerous      ErrorCode = "WORKSPACE_SCRIPT_DANGEROUS"
	ErrorWorkspaceScriptCycle          ErrorCode = "WORKSPACE_SCRIPT_REFERENCE_CYCLE"
	ErrorWorkspaceImportIncomplete     ErrorCode = "WORKSPACE_IMPORT_INCOMPLETE"
	ErrorWorkspaceImportPort           ErrorCode = "WORKSPACE_IMPORT_PORT_UNCONFIRMED"
	ErrorWorkspaceImportDependency     ErrorCode = "WORKSPACE_IMPORT_DEPENDENCY_UNCONFIRMED"
	ErrorWorkspaceImportSourceChanged  ErrorCode = "WORKSPACE_IMPORT_SOURCE_CHANGED"
	ErrorWorkspaceManifestConflict     ErrorCode = "WORKSPACE_MANIFEST_CONFLICT"
	ErrorWorkspaceManifestWrite        ErrorCode = "WORKSPACE_MANIFEST_WRITE_FAILED"
	ErrorWorkspaceDraftExpired         ErrorCode = "WORKSPACE_IMPORT_DRAFT_EXPIRED"
	ErrorWorkspaceEditRuntimeActive    ErrorCode = "WORKSPACE_EDIT_RUNTIME_ACTIVE"
	ErrorWorkspaceUnregisterActive     ErrorCode = "WORKSPACE_UNREGISTER_RUNTIME_ACTIVE"
	ErrorWorkspaceRelinkSystemMismatch ErrorCode = "WORKSPACE_RELINK_SYSTEM_MISMATCH"
	ErrorOperationAlreadyActive        ErrorCode = "OPERATION_ALREADY_ACTIVE"
	ErrorIdempotencyKeyReused          ErrorCode = "IDEMPOTENCY_KEY_REUSED"
	ErrorOperationNotFound             ErrorCode = "OPERATION_NOT_FOUND"
	ErrorOperationNotCancellable       ErrorCode = "OPERATION_NOT_CANCELLABLE"
	ErrorOperationInvalidState         ErrorCode = "OPERATION_INVALID_STATE"
	ErrorRunnerNotFound                ErrorCode = "RUNNER_NOT_FOUND"
	ErrorRunnerVersionFailed           ErrorCode = "RUNNER_VERSION_CHECK_FAILED"
	ErrorRunnerVersionTimeout          ErrorCode = "RUNNER_VERSION_CHECK_TIMEOUT"
	ErrorRunnerPathUnsafe              ErrorCode = "RUNNER_PATH_UNSAFE"
	ErrorPlatformNotSupported          ErrorCode = "PLATFORM_NOT_SUPPORTED"
	ErrorAuthenticationRequired        ErrorCode = "AUTH_TOKEN_INVALID"
	ErrorBootstrapInvalid              ErrorCode = "AUTH_BOOTSTRAP_INVALID"
	ErrorSessionInvalid                ErrorCode = "AUTH_SESSION_INVALID"
	ErrorAuthCapacity                  ErrorCode = "AUTH_CAPACITY_REACHED"
	ErrorBrowserRequestRejected        ErrorCode = "AUTH_BROWSER_REQUEST_REJECTED"
	ErrorSecretNotFound                ErrorCode = "SECRET_NOT_FOUND"
	ErrorSecretInvalid                 ErrorCode = "SECRET_INVALID"
	ErrorSecretVersionConflict         ErrorCode = "SECRET_VERSION_CONFLICT"
	ErrorInternal                      ErrorCode = "INTERNAL_ERROR"
)

type errorSpec struct {
	HTTPStatus     int
	Message        string
	Retryable      bool
	AllowedDetails []string
}

var errorRegistry = map[ErrorCode]errorSpec{
	ErrorRequestValidationFailed: {
		HTTPStatus: http.StatusBadRequest,
		Message:    "The request is invalid.",
	},
	ErrorResourceNotFound: {
		HTTPStatus: http.StatusNotFound,
		Message:    "The requested resource was not found.",
	},
	ErrorMethodNotAllowed: {
		HTTPStatus: http.StatusMethodNotAllowed,
		Message:    "The request method is not allowed for this resource.",
	},
	ErrorFeatureNotEnabled: {
		HTTPStatus:     http.StatusUnprocessableEntity,
		Message:        "The requested feature is not enabled.",
		AllowedDetails: []string{"feature"},
	},
	ErrorEventCursorExpired: {
		HTTPStatus: http.StatusConflict,
		Message:    "The event cursor is older than retained history.",
	},
	ErrorHealthNotReady: {
		HTTPStatus: http.StatusServiceUnavailable,
		Message:    "The control plane is not ready.",
		Retryable:  true,
	},
	ErrorHealthReadinessTimeout: {
		HTTPStatus: http.StatusGatewayTimeout,
		Message:    "The service did not become ready before the deadline.",
		Retryable:  true,
	},
	ErrorProcessExited: {
		HTTPStatus: http.StatusConflict,
		Message:    "The managed process exited before becoming ready.",
		Retryable:  true,
	},
	ErrorProcessIdentityMismatch: {
		HTTPStatus: http.StatusConflict,
		Message:    "The managed process identity could not be verified.",
	},
	ErrorControlPlaneRestarted: {
		HTTPStatus: http.StatusConflict,
		Message:    "The operation was interrupted by a control-plane restart.",
		Retryable:  true,
	},
	ErrorComposeConfigInvalid: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The Docker Compose configuration is invalid.",
	},
	ErrorComposeBuildConfigInvalid: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The Docker Compose build configuration is invalid.",
	},
	ErrorComposeBuildFailed: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The Docker Compose image build failed.",
		Retryable:  true,
	},
	ErrorComposeBuildTimeout: {
		HTTPStatus: http.StatusGatewayTimeout,
		Message:    "The Docker Compose image build timed out.",
		Retryable:  true,
	},
	ErrorComposeDiscoveryFailed: {
		HTTPStatus: http.StatusServiceUnavailable,
		Message:    "The Docker Compose project could not be discovered.",
		Retryable:  true,
	},
	ErrorComposeInspectFailed: {
		HTTPStatus: http.StatusServiceUnavailable,
		Message:    "The Docker Compose project could not be inspected.",
		Retryable:  true,
	},
	ErrorComposeLogFollowFailed: {
		HTTPStatus: http.StatusServiceUnavailable,
		Message:    "Docker Compose logs could not be followed.",
		Retryable:  true,
	},
	ErrorComposeLifecycleInvalid: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The Docker Compose lifecycle request is invalid.",
	},
	ErrorComposeLifecycleTimeout: {
		HTTPStatus: http.StatusGatewayTimeout,
		Message:    "The Docker Compose lifecycle command timed out.",
		Retryable:  true,
	},
	ErrorComposeNotFound: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "Docker Compose v2 is not available.",
	},
	ErrorComposeOverrideConflict: {
		HTTPStatus: http.StatusConflict,
		Message:    "The Docker Compose runtime override conflicts with existing operation output.",
	},
	ErrorComposeOverrideInvalid: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The Docker Compose runtime override is invalid.",
	},
	ErrorComposePreflightTimeout: {
		HTTPStatus: http.StatusGatewayTimeout,
		Message:    "Docker Compose preflight timed out.",
		Retryable:  true,
	},
	ErrorComposeProjectMismatch: {
		HTTPStatus: http.StatusConflict,
		Message:    "The Docker Compose project identity could not be verified.",
	},
	ErrorComposeProjectNotFound: {
		HTTPStatus: http.StatusConflict,
		Message:    "The Docker Compose project was not found.",
		Retryable:  true,
	},
	ErrorComposeStartFailed: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The Docker Compose project could not be started.",
		Retryable:  true,
	},
	ErrorComposeStopFailed: {
		HTTPStatus: http.StatusConflict,
		Message:    "The Docker Compose project could not be stopped.",
		Retryable:  true,
	},
	ErrorComposeVersionUnsupported: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The Docker Compose version is unsupported.",
	},
	ErrorDockerDaemonUnavailable: {
		HTTPStatus: http.StatusServiceUnavailable,
		Message:    "The Docker daemon is unavailable.",
		Retryable:  true,
	},
	ErrorDockerNotFound: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The Docker CLI is not available.",
	},
	ErrorDockerVersionUnsupported: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The Docker version is unsupported.",
	},
	ErrorContainerUnhealthy: {
		HTTPStatus: http.StatusServiceUnavailable,
		Message:    "One or more managed Compose containers are unhealthy.",
		Retryable:  true,
	},
	ErrorSupervisorExited: {
		HTTPStatus: http.StatusConflict,
		Message:    "The managed Supervisor exited.",
		Retryable:  true,
	},
	ErrorSupervisorUnavailable: {
		HTTPStatus: http.StatusServiceUnavailable,
		Message:    "The managed Supervisor is temporarily unavailable.",
		Retryable:  true,
	},
	ErrorProcessStartFailed: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The managed process could not be started.",
		Retryable:  true,
	},
	ErrorProcessStopFailed: {
		HTTPStatus: http.StatusConflict,
		Message:    "The managed process could not be stopped.",
		Retryable:  true,
	},
	ErrorPortConflict: {
		HTTPStatus:     http.StatusConflict,
		Message:        "A selected service port is already in use.",
		Retryable:      true,
		AllowedDetails: []string{"logicalPort", "port"},
	},
	ErrorTCPRefused: {
		HTTPStatus: http.StatusServiceUnavailable,
		Message:    "The readiness TCP connection was refused.",
		Retryable:  true,
	},
	ErrorTCPTimeout: {
		HTTPStatus: http.StatusGatewayTimeout,
		Message:    "The readiness TCP connection timed out.",
		Retryable:  true,
	},
	ErrorHTTPStatusMismatch: {
		HTTPStatus: http.StatusServiceUnavailable,
		Message:    "The readiness HTTP status did not match.",
		Retryable:  true,
	},
	ErrorHTTPBodyMismatch: {
		HTTPStatus: http.StatusServiceUnavailable,
		Message:    "The readiness HTTP response did not match.",
		Retryable:  true,
	},
	ErrorHTTPTimeout: {
		HTTPStatus: http.StatusGatewayTimeout,
		Message:    "The readiness HTTP request timed out.",
		Retryable:  true,
	},
	ErrorLogCursorExpired: {
		HTTPStatus: http.StatusConflict,
		Message:    "The log cursor is older than retained history.",
	},
	ErrorManifestFileTooLarge: {
		HTTPStatus:     http.StatusBadRequest,
		Message:        "The system manifest exceeds the maximum size.",
		AllowedDetails: []string{"maxBytes"},
	},
	ErrorManifestNotRegularFile: {
		HTTPStatus: http.StatusBadRequest,
		Message:    "The system manifest is not a regular file.",
	},
	ErrorManifestYAMLInvalid: {
		HTTPStatus:     http.StatusBadRequest,
		Message:        "The system manifest contains invalid YAML.",
		AllowedDetails: []string{"location"},
	},
	ErrorManifestDuplicateKey: {
		HTTPStatus:     http.StatusBadRequest,
		Message:        "The system manifest contains a duplicate key.",
		AllowedDetails: []string{"location", "field"},
	},
	ErrorManifestUnknownField: {
		HTTPStatus:     http.StatusBadRequest,
		Message:        "The system manifest contains an unknown field.",
		AllowedDetails: []string{"location", "field"},
	},
	ErrorManifestMultipleDocs: {
		HTTPStatus: http.StatusBadRequest,
		Message:    "The system manifest must contain exactly one YAML document.",
	},
	ErrorManifestSchemaInvalid: {
		HTTPStatus:     http.StatusBadRequest,
		Message:        "The system manifest does not match the v1alpha1 schema.",
		AllowedDetails: []string{"location"},
	},
	ErrorManifestSemanticInvalid: {
		HTTPStatus:     http.StatusUnprocessableEntity,
		Message:        "The system manifest contains inconsistent values.",
		AllowedDetails: []string{"location", "field"},
	},
	ErrorManifestPathOutside: {
		HTTPStatus:     http.StatusUnprocessableEntity,
		Message:        "A manifest path resolves outside the workspace.",
		AllowedDetails: []string{"location"},
	},
	ErrorManifestCycleDetected: {
		HTTPStatus:     http.StatusUnprocessableEntity,
		Message:        "The system manifest contains a service dependency cycle.",
		AllowedDetails: []string{"location", "field"},
	},
	ErrorManifestReferenceAbsent: {
		HTTPStatus:     http.StatusUnprocessableEntity,
		Message:        "The system manifest references an undeclared item.",
		AllowedDetails: []string{"location", "field"},
	},
	ErrorManifestTemplateInvalid: {
		HTTPStatus:     http.StatusUnprocessableEntity,
		Message:        "The system manifest contains an invalid template.",
		AllowedDetails: []string{"location", "field"},
	},
	ErrorManifestDurationInvalid: {
		HTTPStatus:     http.StatusUnprocessableEntity,
		Message:        "The system manifest contains an invalid duration.",
		AllowedDetails: []string{"location", "field"},
	},
	ErrorManifestPortRange: {
		HTTPStatus:     http.StatusUnprocessableEntity,
		Message:        "The system manifest contains an invalid port or port range.",
		AllowedDetails: []string{"location", "field"},
	},
	ErrorManifestHealthUnsafe: {
		HTTPStatus:     http.StatusUnprocessableEntity,
		Message:        "The system manifest contains an unsafe health-check target.",
		AllowedDetails: []string{"location", "field"},
	},
	ErrorManifestChanged: {
		HTTPStatus: http.StatusConflict,
		Message:    "The running instance uses a different manifest; restart the system instead.",
	},
	ErrorWorkspaceAlreadyExists: {
		HTTPStatus: http.StatusConflict,
		Message:    "The workspace is already registered.",
	},
	ErrorWorkspaceNotFound: {
		HTTPStatus: http.StatusNotFound,
		Message:    "The workspace was not found.",
	},
	ErrorWorkspaceManifestAbsent: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The fixed workspace manifest is unavailable.",
	},
	ErrorWorkspaceSystemChanged: {
		HTTPStatus: http.StatusConflict,
		Message:    "A workspace refresh cannot change its system ID.",
	},
	ErrorWorkspaceIDRequired: {
		HTTPStatus: http.StatusConflict,
		Message:    "A workspace ID is required because this system has multiple registrations.",
	},
	ErrorWorkspacePathInvalid: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The workspace path must be an existing readable directory.",
	},
	ErrorWorkspaceScriptNotFound:       {HTTPStatus: http.StatusNotFound, Message: "The selected workspace startup script was not found."},
	ErrorWorkspaceScriptOutside:        {HTTPStatus: http.StatusUnprocessableEntity, Message: "The selected startup script resolves outside the workspace."},
	ErrorWorkspaceScriptType:           {HTTPStatus: http.StatusUnprocessableEntity, Message: "The selected startup script type is not supported."},
	ErrorWorkspaceScriptEncoding:       {HTTPStatus: http.StatusUnprocessableEntity, Message: "The startup script encoding is not supported."},
	ErrorWorkspaceScriptTooLarge:       {HTTPStatus: http.StatusRequestEntityTooLarge, Message: "The startup script exceeds the analysis size limit."},
	ErrorWorkspaceScriptSyntax:         {HTTPStatus: http.StatusUnprocessableEntity, Message: "The startup script contains unsupported syntax."},
	ErrorWorkspaceScriptDangerous:      {HTTPStatus: http.StatusUnprocessableEntity, Message: "The startup script contains dangerous syntax and cannot be imported."},
	ErrorWorkspaceScriptCycle:          {HTTPStatus: http.StatusUnprocessableEntity, Message: "The startup script reference graph contains a cycle."},
	ErrorWorkspaceImportIncomplete:     {HTTPStatus: http.StatusUnprocessableEntity, Message: "The workspace import draft has unresolved blocking findings."},
	ErrorWorkspaceImportPort:           {HTTPStatus: http.StatusUnprocessableEntity, Message: "A service port must be confirmed before import."},
	ErrorWorkspaceImportDependency:     {HTTPStatus: http.StatusUnprocessableEntity, Message: "A service dependency must be confirmed before import."},
	ErrorWorkspaceImportSourceChanged:  {HTTPStatus: http.StatusConflict, Message: "The workspace source changed after analysis."},
	ErrorWorkspaceManifestConflict:     {HTTPStatus: http.StatusConflict, Message: "The workspace manifest changed or appeared after analysis."},
	ErrorWorkspaceManifestWrite:        {HTTPStatus: http.StatusInternalServerError, Message: "The validated workspace manifest could not be published."},
	ErrorWorkspaceDraftExpired:         {HTTPStatus: http.StatusGone, Message: "The workspace import draft expired and must be analyzed again."},
	ErrorWorkspaceEditRuntimeActive:    {HTTPStatus: http.StatusConflict, Message: "The workspace must be stopped with no active Operation before applying edits."},
	ErrorWorkspaceUnregisterActive:     {HTTPStatus: http.StatusConflict, Message: "The workspace must be stopped with no active Operation before it can be unregistered."},
	ErrorWorkspaceRelinkSystemMismatch: {HTTPStatus: http.StatusConflict, Message: "The relink target must contain the same system identity."},
	ErrorOperationAlreadyActive: {
		HTTPStatus: http.StatusConflict,
		Message:    "The workspace already has an active operation.",
		Retryable:  true,
	},
	ErrorIdempotencyKeyReused: {
		HTTPStatus: http.StatusConflict,
		Message:    "The idempotency key was already used for a different request.",
	},
	ErrorOperationNotFound: {
		HTTPStatus: http.StatusNotFound,
		Message:    "The operation was not found.",
	},
	ErrorOperationNotCancellable: {
		HTTPStatus: http.StatusConflict,
		Message:    "The operation cannot be cancelled.",
	},
	ErrorOperationInvalidState: {
		HTTPStatus: http.StatusConflict,
		Message:    "The operation is not in a state that permits this action.",
	},
	ErrorRunnerNotFound: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The required runner executable was not found.",
	},
	ErrorRunnerVersionFailed: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The runner version check failed.",
	},
	ErrorRunnerVersionTimeout: {
		HTTPStatus: http.StatusGatewayTimeout,
		Message:    "The runner version check timed out.",
		Retryable:  true,
	},
	ErrorRunnerPathUnsafe: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The runner executable is outside the trusted path boundary.",
	},
	ErrorPlatformNotSupported: {
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The requested operation is not supported on this platform.",
	},
	ErrorAuthenticationRequired: {
		HTTPStatus: http.StatusUnauthorized,
		Message:    "The local authentication token is invalid.",
	},
	ErrorBootstrapInvalid: {
		HTTPStatus: http.StatusUnauthorized,
		Message:    "The browser bootstrap code is invalid or expired.",
	},
	ErrorSessionInvalid: {
		HTTPStatus: http.StatusUnauthorized,
		Message:    "The browser session is invalid or expired.",
	},
	ErrorAuthCapacity: {
		HTTPStatus: http.StatusServiceUnavailable,
		Message:    "The local authentication session capacity is exhausted.",
		Retryable:  true,
	},
	ErrorBrowserRequestRejected: {
		HTTPStatus: http.StatusForbidden,
		Message:    "The browser request failed the local origin or CSRF check.",
	},
	ErrorSecretNotFound: {
		HTTPStatus: http.StatusNotFound,
		Message:    "The requested Secret was not found.",
	},
	ErrorSecretInvalid: {
		HTTPStatus: http.StatusBadRequest,
		Message:    "The Secret request is invalid.",
	},
	ErrorSecretVersionConflict: {
		HTTPStatus: http.StatusConflict,
		Message:    "The Secret metadata version conflicts with protected storage.",
		Retryable:  true,
	},
	ErrorInternal: {
		HTTPStatus: http.StatusInternalServerError,
		Message:    "An internal error occurred.",
	},
}

type errorEnvelope struct {
	Error errorDTO `json:"error"`
}

type errorDTO struct {
	Code        ErrorCode      `json:"code"`
	Message     string         `json:"message"`
	Details     map[string]any `json:"details"`
	OperationID *string        `json:"operationId,omitempty"`
	TraceID     string         `json:"traceId"`
}

func writeRegisteredError(response http.ResponseWriter, request *http.Request, code ErrorCode) {
	writeRegisteredErrorDetails(response, request, code, nil)
}

func writeRegisteredErrorDetails(response http.ResponseWriter, request *http.Request, code ErrorCode, details map[string]any) {
	spec, ok := errorRegistry[code]
	if !ok {
		code = ErrorInternal
		spec = errorRegistry[code]
	}
	writeJSON(response, spec.HTTPStatus, errorEnvelope{Error: errorDTO{
		Code:    code,
		Message: spec.Message,
		Details: allowedErrorDetails(spec, details),
		TraceID: traceIDFromContext(request.Context()),
	}})
}

func allowedErrorDetails(spec errorSpec, details map[string]any) map[string]any {
	result := make(map[string]any)
	for _, key := range spec.AllowedDetails {
		if value, ok := details[key]; ok {
			result[key] = value
		}
	}
	return result
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
