export interface Workspace {
  id: string
  systemId: string
  systemName: string
  path: string
  manifestStatus: 'valid' | 'invalid'
  manifestDigest: string
  lastErrorCode?: string
  serviceCount: number
  createdAt: string
  updatedAt: string
}

export interface WorkspaceProbe {
	state: 'ready_to_register' | 'initialization_required'
	path: string
	candidates: Array<{ path: string; size: number }>
}

export type ImportConfidence = 'confirmed' | 'inferred' | 'unresolved'

export interface ImportFinding {
	code: string
	severity: 'info' | 'warning' | 'blocking'
	message: string
	field?: string
	confidence: ImportConfidence
	evidence: Array<{ path: string; line?: number; field?: string }>
}

export interface WorkspaceImportCandidate {
	id: string
	name: string
	description: string
	applyable: boolean
	requiredCapabilities: string[]
	services: Array<{
		id: string
		displayName: string
		driver: 'process' | 'compose'
		runner: string
		mode: string
		workingDirectory: string
		arguments: string[]
		readinessType: string
		readinessTarget?: string
		confidence: ImportConfidence
		compose?: {
			file: string
			services: string[]
			buildPolicy: 'never' | 'always'
			buildServices: string[]
			readiness: Record<string, 'healthy' | 'running'>
			ports: Record<string, { service: string; target: number }>
		}
	}>
	ports: Array<{ name: string; preferred: number; exposure: string; confidence: ImportConfidence }>
	findings: ImportFinding[]
	manifestYaml: string
	manifestDigest: string
}

export interface WorkspaceImportDraft {
	id: string
	state: 'active' | 'applied' | 'expired'
	path: string
	expiresAt: string
	draft: {
		systemId: string
		systemName: string
		description?: string
		sourceScript: string
		sourceDigest: string
		analyzedAt: string
		candidates: WorkspaceImportCandidate[]
	}
}

export interface WorkspaceImportOperation {
	id: string
	workspaceId?: string
	type: 'workspace-import-apply' | 'workspace-edit-apply' | 'workspace-relink-apply'
	state: OperationState
	errorCode?: string
	createdAt: string
	startedAt?: string
	finishedAt?: string
	durationMs?: number
	steps: Array<{ number: number; key: string; state: string; startedAt?: string; finishedAt?: string; errorCode?: string }>
}

export interface WorkspaceDetail {
	workspace: Workspace
	source: { type: 'existing-manifest' | 'bat-import' | 'structured-edit' | 'relinked-manifest'; entryScript?: string; sourceDigest?: string; analyzedAt?: string }
	manifest: { digest: string; apiVersion: string; description?: string; yaml: string; createdAt: string }
	services: Array<{ id: string; displayName: string; driver: string; mode: string; runner: string; workingDirectory: string; required: boolean; dependsOn: Record<string, string>; readiness: string }>
	ports: Array<{ name: string; protocol: string; preferred?: number; fallbackRange?: string; conflictPolicy: string; exposure: string }>
	runtime: { state: RuntimeState; activeOperationId: string | null }
}

export interface SystemSummary {
  id: string
  name: string
  workspaceId: string
  workspacePath: string
  manifestStatus: 'valid' | 'invalid'
  manifestDigest: string
  state: RuntimeState
  serviceSummary: { ready: number; total: number }
  activeOperationId: string | null
  updatedAt: string
}

export interface ServiceDefinition {
  id: string
  driver: 'process' | 'compose'
  mode: 'daemon' | 'oneshot'
  required: boolean
  definitionDigest: string
}

export interface SystemDetail {
  system: SystemSummary
  manifest: { digest: string; apiVersion: string; createdAt: string }
  services: ServiceDefinition[]
}

export interface ErrorEnvelope {
  error: { code: string; message: string; details: Record<string, unknown>; traceId: string }
}

export interface VersionResponse {
  version: string
  commit: string
  buildTime: string
  apiVersion: string
  capabilities: string[]
}

export type RuntimeState = 'stopping' | 'failed' | 'starting' | 'degraded' | 'running' | 'stopped'
export type ServiceState = 'stopped' | 'waiting_dependency' | 'starting' | 'waiting_ready' | 'ready' | 'degraded' | 'completed' | 'stopping' | 'failed' | 'unknown'
export type OperationState = 'queued' | 'running' | 'cancelling' | 'succeeded' | 'failed' | 'cancelled'

export interface PortRuntimeStatus {
  logicalName: string
  port: number
  source: 'request' | 'workspace' | 'sticky' | 'preferred' | 'fallback'
  replaced: boolean
  conflictPort?: number
}

export interface ServiceRuntimeStatus {
  serviceId: string
  serviceInstanceId: string
  driver: 'process' | 'compose'
  mode: 'daemon' | 'oneshot'
  state: ServiceState
  stateVersion: number
  pid?: number
  processStartedAt?: string
  commandDigest?: string
  exitCode?: number
  dependsOn: string[]
  containers: ComposeContainerStatus[]
}

export interface ComposeContainerStatus {
  service: string
  state: string
  health: string
  exitCode: number
}

export interface SystemRuntimeStatus {
  systemId: string
  workspaceId: string
  state: RuntimeState
  instanceId?: string
  manifestDigest?: string
  resolvedSpecDigest?: string
  startedAt?: string
  stoppedAt?: string
  services: ServiceRuntimeStatus[]
  ports: PortRuntimeStatus[]
}

export interface OperationStep {
  number: number
  key: string
  state: 'pending' | 'running' | 'succeeded' | 'failed' | 'skipped' | 'cancelled'
  attempt: number
  startedAt?: string
  finishedAt?: string
  durationMs?: number
  errorCode?: string
  detailRef?: string
}

export interface Operation {
  id: string
  workspaceId: string
  systemId: string
  type: 'start' | 'stop' | 'restart' | 'service-restart' | 'port-plan' | 'refresh' | 'analyze'
  state: OperationState
  cancellable: boolean
  cancelRequestedAt?: string
  errorCode?: string
  createdAt: string
  startedAt?: string
  finishedAt?: string
  durationMs?: number
  steps: OperationStep[]
}

export interface OperationRef {
  operationId: string
  state: OperationState
}

export interface LogEntry {
  timestamp: string
  systemId: string
  instanceId: string
  serviceId: string
  stream: 'stdout' | 'stderr'
  level: string
  message: string
  operationId?: string
  sequence: number
  truncated: boolean
}

export interface IncidentEvidence {
  type: 'event' | 'health' | 'log'
  eventId?: number
  healthResultId?: number
  serviceInstanceId?: string
  logSequence?: number
}

export interface IncidentLogLine {
  serviceInstanceId: string
  sequence: number
  timestamp: string
  stream: 'stdout' | 'stderr'
  message: string
  repeatCount?: number
}

export interface IncidentContext {
  schemaVersion: string
  workspaceId: string
  systemInstanceId?: string
  serviceInstanceId?: string
  serviceId?: string
  kind: string
  triggerCode: string
  windowStart: string
  windowEnd: string
  dependencies: Record<string, ServiceState>
  ports: Record<string, number>
  evidence: IncidentEvidence[]
  logs: IncidentLogLine[]
}

export interface Incident {
  id: string
  workspaceId: string
  systemInstanceId?: string
  serviceInstanceId?: string
  serviceId?: string
  kind: string
  severity: 'info' | 'warning' | 'critical'
  state: 'open' | 'resolved'
  occurrenceCount: number
  context: IncidentContext
  firstSeenAt: string
  lastSeenAt: string
}

export interface IncidentSuggestion {
  action: string
  description: string
  automatic: false
}

export interface IncidentRuleResult {
  ruleId: string
  title: string
  cause: string
  confidence: number
  evidence: IncidentEvidence[]
  suggestions: IncidentSuggestion[]
}

export interface IncidentAnalysis {
  id: number
  engine: string
  schemaVersion: string
  result: { results: IncidentRuleResult[] }
  createdAt: string
}

export interface IncidentDetail {
  incident: Incident
  analyses: IncidentAnalysis[]
}
