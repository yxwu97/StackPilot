# Error Code Registry

The OpenAPI extension `x-stackpilot-error-codes` in `api/openapi.yaml` is the machine-readable source of truth. The implementation and this human-readable table must remain aligned with it.

| Code | HTTP | Retryable | Allowed details | Safe message |
| --- | ---: | --- | --- | --- |
| `REQUEST_VALIDATION_FAILED` | 400 | No | None | The request is invalid. |
| `EVENT_CURSOR_EXPIRED` | 409 | No | None | The event cursor is older than retained history. |
| `LOG_CURSOR_EXPIRED` | 409 | No | None | The log cursor is older than retained history. |
| `AUTH_TOKEN_INVALID` | 401 | No | None | The local authentication token is invalid. |
| `AUTH_BOOTSTRAP_INVALID` | 401 | No | None | The browser bootstrap code is invalid or expired. |
| `AUTH_SESSION_INVALID` | 401 | No | None | The browser session is invalid or expired. |
| `AUTH_CAPACITY_REACHED` | 503 | Yes | None | The local authentication session capacity is exhausted. |
| `AUTH_BROWSER_REQUEST_REJECTED` | 403 | No | None | The browser request failed the local origin or CSRF check. |
| `SECRET_INVALID` | 400 | No | None | The Secret request is invalid. |
| `SECRET_NOT_FOUND` | 404 | No | None | The requested Secret was not found. |
| `SECRET_VERSION_CONFLICT` | 409 | Yes | None | The Secret metadata version conflicts with protected storage. |
| `RESOURCE_NOT_FOUND` | 404 | No | None | The requested resource was not found. |
| `METHOD_NOT_ALLOWED` | 405 | No | None | The request method is not allowed for this resource. |
| `FEATURE_NOT_ENABLED` | 422 | No | `feature` | The requested feature is not enabled. |
| `METRIC_QUERY_INVALID` | 400 | No | None | The metric query exceeds the supported time, service, or point limits. |
| `METRIC_SOURCE_UNAVAILABLE` | 503 | Yes | None | The trusted runtime metric source is temporarily unavailable. |
| `METRIC_SOURCE_UNSUPPORTED` | 422 | No | None | The managed runtime does not support trusted resource metrics. |
| `REVISION_NOT_FOUND` | 404 | No | None | The requested system revision was not found. |
| `REVISION_SOURCE_UNAVAILABLE` | 422 | No | None | The system revision cannot be collected from the trusted sources. |
| `REVISION_SOURCE_UNSAFE` | 422 | No | None | A revision source is outside the trusted workspace boundary. |
| `REVISION_SOURCE_TOO_LARGE` | 413 | No | None | The revision source exceeds the bounded collection limits. |
| `REVISION_GIT_PROBE_FAILED` | 503 | Yes | None | The bounded read-only Git probe failed. |
| `CHANGE_PLAN_NOT_FOUND` | 404 | No | None | The requested change plan was not found. |
| `CHANGE_PLAN_STALE` | 409 | No | None | The workspace changed after the change plan was created. |
| `CHANGE_PLAN_BLOCKED` | 409 | No | None | The change plan contains blocking findings. |
| `CHANGE_PLAN_INVALID_STATE` | 409 | No | None | The change plan is not in a state that permits this action. |
| `VERIFICATION_HEALTH_INCOMPLETE` | 422 | No | None | The required services do not have sufficient health coverage for verified restart. |
| `VERIFICATION_UNAVAILABLE` | 422 | No | None | Verified restart is unavailable for the current system facts. |
| `VERIFICATION_FAILED` | 409 | No | None | The restarted system did not satisfy the stability contract. |
| `COMPOSE_CONFIG_INVALID` | 422 | No | None | The Docker Compose configuration is invalid. |
| `COMPOSE_BUILD_CONFIG_INVALID` | 422 | No | None | The Docker Compose build configuration is invalid. |
| `COMPOSE_BUILD_FAILED` | 422 | Yes | None | The Docker Compose image build failed. |
| `COMPOSE_BUILD_TIMEOUT` | 504 | Yes | None | The Docker Compose image build timed out. |
| `COMPOSE_DISCOVERY_FAILED` | 503 | Yes | None | The Docker Compose project could not be discovered. |
| `COMPOSE_INSPECT_FAILED` | 503 | Yes | None | The Docker Compose project could not be inspected. |
| `COMPOSE_LOG_FOLLOW_FAILED` | 503 | Yes | None | Docker Compose logs could not be followed. |
| `COMPOSE_LIFECYCLE_INVALID` | 422 | No | None | The Docker Compose lifecycle request is invalid. |
| `COMPOSE_LIFECYCLE_TIMEOUT` | 504 | Yes | None | The Docker Compose lifecycle command timed out. |
| `COMPOSE_NOT_FOUND` | 422 | No | None | Docker Compose v2 is not available. |
| `COMPOSE_OVERRIDE_CONFLICT` | 409 | No | None | The Docker Compose runtime override conflicts with existing operation output. |
| `COMPOSE_OVERRIDE_INVALID` | 422 | No | None | The Docker Compose runtime override is invalid. |
| `COMPOSE_PREFLIGHT_TIMEOUT` | 504 | Yes | None | Docker Compose preflight timed out. |
| `COMPOSE_PROJECT_IDENTITY_MISMATCH` | 409 | No | None | The Docker Compose project identity could not be verified. |
| `COMPOSE_PROJECT_NOT_FOUND` | 409 | Yes | None | The Docker Compose project was not found. |
| `COMPOSE_START_FAILED` | 422 | Yes | None | The Docker Compose project could not be started. |
| `COMPOSE_STOP_FAILED` | 409 | Yes | None | The Docker Compose project could not be stopped. |
| `CONTAINER_UNHEALTHY` | 503 | Yes | None | One or more managed Compose containers are unhealthy. |
| `COMPOSE_VERSION_UNSUPPORTED` | 422 | No | None | The Docker Compose version is unsupported. |
| `DOCKER_DAEMON_UNAVAILABLE` | 503 | Yes | None | The Docker daemon is unavailable. |
| `DOCKER_NOT_FOUND` | 422 | No | None | The Docker CLI is not available. |
| `DOCKER_VERSION_UNSUPPORTED` | 422 | No | None | The Docker version is unsupported. |
| `HEALTH_NOT_READY` | 503 | Yes | None | The control plane is not ready. |
| `HEALTH_READINESS_TIMEOUT` | 504 | Yes | None | The service did not become ready before the deadline. |
| `PROCESS_EXITED` | 409 | Yes | None | The managed process exited before becoming ready. |
| `PROCESS_IDENTITY_MISMATCH` | 409 | No | None | The managed process identity could not be verified. |
| `CONTROL_PLANE_RESTARTED` | 409 | Yes | None | The operation was interrupted by a control-plane restart. |
| `SUPERVISOR_EXITED` | 409 | Yes | None | The managed Supervisor exited. |
| `SUPERVISOR_UNAVAILABLE` | 503 | Yes | None | The managed Supervisor is temporarily unavailable. |
| `PROCESS_START_FAILED` | 422 | Yes | None | The managed process could not be started. |
| `PROCESS_STOP_FAILED` | 409 | Yes | None | The managed process could not be stopped. |
| `PORT_CONFLICT` | 409 | Yes | `logicalPort`, `port` | A selected service port is already in use. |
| `TCP_REFUSED` | 503 | Yes | None | The readiness TCP connection was refused. |
| `TCP_TIMEOUT` | 504 | Yes | None | The readiness TCP connection timed out. |
| `HTTP_STATUS_MISMATCH` | 503 | Yes | None | The readiness HTTP status did not match. |
| `HTTP_BODY_MISMATCH` | 503 | Yes | None | The readiness HTTP response did not match. |
| `HTTP_TIMEOUT` | 504 | Yes | None | The readiness HTTP request timed out. |
| `MANIFEST_FILE_TOO_LARGE` | 400 | No | `maxBytes` | The system manifest exceeds the maximum size. |
| `MANIFEST_NOT_REGULAR_FILE` | 400 | No | None | The system manifest is not a regular file. |
| `MANIFEST_YAML_INVALID` | 400 | No | `location` | The system manifest contains invalid YAML. |
| `MANIFEST_DUPLICATE_KEY` | 400 | No | `location`, `field` | The system manifest contains a duplicate key. |
| `MANIFEST_UNKNOWN_FIELD` | 400 | No | `location`, `field` | The system manifest contains an unknown field. |
| `MANIFEST_MULTIPLE_DOCUMENTS` | 400 | No | None | The system manifest must contain exactly one YAML document. |
| `MANIFEST_SCHEMA_INVALID` | 400 | No | `location` | The system manifest does not match the v1alpha1 schema. |
| `MANIFEST_SEMANTIC_INVALID` | 422 | No | `location`, `field` | The system manifest contains inconsistent values. |
| `MANIFEST_PATH_OUTSIDE_WORKSPACE` | 422 | No | `location` | A manifest path resolves outside the workspace. |
| `MANIFEST_CYCLE_DETECTED` | 422 | No | `location`, `field` | The system manifest contains a service dependency cycle. |
| `MANIFEST_REFERENCE_NOT_FOUND` | 422 | No | `location`, `field` | The system manifest references an undeclared item. |
| `MANIFEST_TEMPLATE_INVALID` | 422 | No | `location`, `field` | The system manifest contains an invalid template. |
| `MANIFEST_DURATION_INVALID` | 422 | No | `location`, `field` | The system manifest contains an invalid duration. |
| `MANIFEST_PORT_RANGE_INVALID` | 422 | No | `location`, `field` | The system manifest contains an invalid port or port range. |
| `MANIFEST_HEALTH_TARGET_UNSAFE` | 422 | No | `location`, `field` | The system manifest contains an unsafe health-check target. |
| `MANIFEST_CHANGED` | 409 | No | None | The running instance uses a different manifest; restart the system instead. |
| `WORKSPACE_ALREADY_REGISTERED` | 409 | No | None | The workspace is already registered. |
| `WORKSPACE_NOT_FOUND` | 404 | No | None | The workspace was not found. |
| `WORKSPACE_MANIFEST_UNAVAILABLE` | 422 | No | None | The fixed workspace manifest is unavailable. |
| `WORKSPACE_SYSTEM_ID_CHANGED` | 409 | No | None | A workspace refresh cannot change its system ID. |
| `WORKSPACE_ID_REQUIRED` | 409 | No | None | A workspace ID is required because this system has multiple registrations. |
| `WORKSPACE_PATH_INVALID` | 422 | No | None | The workspace path must be an existing readable directory. |
| `WORKSPACE_SCRIPT_NOT_FOUND` | 404 | No | None | The selected workspace startup script was not found. |
| `WORKSPACE_SCRIPT_OUTSIDE` | 422 | No | None | The selected startup script resolves outside the workspace. |
| `WORKSPACE_SCRIPT_TYPE_UNSUPPORTED` | 422 | No | None | The selected startup script type is not supported. |
| `WORKSPACE_SCRIPT_ENCODING_UNSUPPORTED` | 422 | No | None | The startup script encoding is not supported. |
| `WORKSPACE_SCRIPT_TOO_LARGE` | 413 | No | None | The startup script exceeds the analysis size limit. |
| `WORKSPACE_SCRIPT_SYNTAX_UNSUPPORTED` | 422 | No | None | The startup script contains unsupported syntax. |
| `WORKSPACE_SCRIPT_DANGEROUS` | 422 | No | None | The startup script contains dangerous syntax and cannot be imported. |
| `WORKSPACE_SCRIPT_REFERENCE_CYCLE` | 422 | No | None | The startup script reference graph contains a cycle. |
| `WORKSPACE_IMPORT_INCOMPLETE` | 422 | No | None | The workspace import draft has unresolved blocking findings. |
| `WORKSPACE_IMPORT_PORT_UNCONFIRMED` | 422 | No | None | A service port must be confirmed before import. |
| `WORKSPACE_IMPORT_DEPENDENCY_UNCONFIRMED` | 422 | No | None | A service dependency must be confirmed before import. |
| `WORKSPACE_IMPORT_SOURCE_CHANGED` | 409 | No | None | The workspace source changed after analysis. |
| `WORKSPACE_IMPORT_DRAFT_EXPIRED` | 410 | No | None | The workspace import draft expired and must be analyzed again. |
| `WORKSPACE_MANIFEST_CONFLICT` | 409 | No | None | The workspace manifest changed or appeared after analysis. |
| `WORKSPACE_MANIFEST_WRITE_FAILED` | 500 | No | None | The validated workspace manifest could not be published. |
| `WORKSPACE_EDIT_RUNTIME_ACTIVE` | 409 | No | None | The workspace must be stopped with no active Operation before applying edits. |
| `WORKSPACE_UNREGISTER_RUNTIME_ACTIVE` | 409 | No | None | The workspace must be stopped with no active Operation before it can be unregistered. |
| `WORKSPACE_RELINK_SYSTEM_MISMATCH` | 409 | No | None | The relink target must contain the same system identity. |
| `OPERATION_ALREADY_ACTIVE` | 409 | Yes | None | The workspace already has an active operation. |
| `IDEMPOTENCY_KEY_REUSED` | 409 | No | None | The idempotency key was already used for a different request. |
| `OPERATION_NOT_FOUND` | 404 | No | None | The operation was not found. |
| `OPERATION_NOT_CANCELLABLE` | 409 | No | None | The operation cannot be cancelled. |
| `OPERATION_INVALID_STATE` | 409 | No | None | The operation is not in a state that permits this action. |
| `RUNNER_NOT_FOUND` | 422 | No | None | The required runner executable was not found. |
| `RUNNER_VERSION_CHECK_FAILED` | 422 | No | None | The runner version check failed. |
| `RUNNER_VERSION_CHECK_TIMEOUT` | 504 | Yes | None | The runner version check timed out. |
| `RUNNER_PATH_UNSAFE` | 422 | No | None | The runner executable is outside the trusted path boundary. |
| `PLATFORM_NOT_SUPPORTED` | 422 | No | None | The requested operation is not supported on this platform. |
| `INTERNAL_ERROR` | 500 | No | None | An internal error occurred. |

`details` must contain only the listed fields. Unknown internal errors use `INTERNAL_ERROR`; their response contains only the safe message, an empty details object, and the request `traceId`.

## Non-HTTP Operation and Incident Codes

These codes are persisted in Operation or Incident records and are not synchronous HTTP error envelopes, so they are intentionally outside `x-stackpilot-error-codes`.

| Code | Scope | Retryable | Meaning |
| --- | --- | --- | --- |
| `INCIDENT_ANALYSIS_FAILED` | `analyze` Operation | Yes, through a new idempotent analysis request | The bounded read-only evidence refresh or deterministic rule analysis failed. |
| `RESTART_LIMIT_REACHED` | Incident trigger | No automatic retry | The configured automatic restart attempt limit was reached; the service remains failed until an explicit action. |
