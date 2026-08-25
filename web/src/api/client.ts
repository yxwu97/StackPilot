import type { ErrorEnvelope, Incident, IncidentDetail, LogEntry, Operation, OperationRef, SystemDetail, SystemRuntimeStatus, SystemSummary, VersionResponse, Workspace, WorkspaceDetail, WorkspaceImportDraft, WorkspaceImportOperation, WorkspaceProbe } from './types'
import { prepareMutationCSRF, publishAuthenticationInvalidation } from './auth-lifecycle.ts'

const apiBase = '/api/v1'

export interface SessionResponse {
  csrf: string
  expiresAt: string
}

interface RequestOptions extends RequestInit {
  skipMutationCoordination?: boolean
}

export class APIError extends Error {
  readonly code: string
  readonly traceId: string

  constructor(
    code: string,
    message: string,
    traceId: string,
  ) {
    super(message)
    this.name = 'APIError'
    this.code = code
    this.traceId = traceId
  }
}

async function requestAt<T>(path: string, init?: RequestOptions): Promise<T> {
  const headers = new Headers(init?.headers)
  if (init?.body !== undefined) {
    headers.set('Content-Type', 'application/json')
  }
  const method = init?.method?.toUpperCase() ?? 'GET'
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method) && init?.skipMutationCoordination !== true) {
    headers.set('X-StackPilot-CSRF', await prepareMutationCSRF())
  }
  const { skipMutationCoordination: _, ...requestInit } = init ?? {}
  const response = await fetch(path, { ...requestInit, credentials: 'same-origin', headers })
  if (!response.ok) {
    const body = (await response.json()) as ErrorEnvelope
    const error = new APIError(body.error.code, body.error.message, body.error.traceId)
    publishAuthenticationInvalidation(error.code)
    throw error
  }
  if (response.status === 204) return undefined as T
  return (await response.json()) as T
}

async function request<T>(path: string, init?: RequestOptions): Promise<T> {
  return requestAt<T>(`${apiBase}${path}`, init)
}

const productVersionPattern = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/

export async function getVersion(): Promise<VersionResponse> {
  const value: unknown = await requestAt<unknown>('/version')
  if (!isVersionResponse(value)) throw new Error('控制面返回了无效的系统版本信息。')
  return value
}

function isVersionResponse(value: unknown): value is VersionResponse {
  if (typeof value !== 'object' || value === null) return false
  const candidate = value as Record<string, unknown>
  return typeof candidate.version === 'string'
    && productVersionPattern.test(candidate.version)
    && typeof candidate.commit === 'string'
    && typeof candidate.buildTime === 'string'
    && typeof candidate.apiVersion === 'string'
    && Array.isArray(candidate.capabilities)
    && candidate.capabilities.every((item) => typeof item === 'string')
}

export async function initializeAuthentication(bootstrap: string | null): Promise<SessionResponse> {
  return bootstrap === null
    ? request<SessionResponse>('/auth/session')
    : request<SessionResponse>('/auth/session', {
      method: 'POST', body: JSON.stringify({ bootstrap }), skipMutationCoordination: true,
    })
}

export async function refreshAuthentication(signal?: AbortSignal): Promise<SessionResponse> {
  return request<SessionResponse>('/auth/session', { signal })
}

export async function revokeAuthentication(): Promise<void> {
  await request<void>('/auth/session', { method: 'DELETE', headers: { 'Content-Type': 'application/json' } })
}

export function consumeBootstrapFragment(): string | null {
	const fragment = new URLSearchParams(window.location.hash.startsWith('#') ? window.location.hash.slice(1) : '')
	const bootstrap = fragment.get('bootstrap')
	if (bootstrap !== null) {
		fragment.delete('bootstrap')
		const suffix = fragment.toString() === '' ? '' : `#${fragment.toString()}`
		window.history.replaceState(null, '', `${window.location.pathname}${window.location.search}${suffix}`)
	}
	return bootstrap
}

export async function listWorkspaces(): Promise<Workspace[]> {
  return (await request<{ items: Workspace[] }>('/workspaces')).items
}

export async function registerWorkspace(path: string): Promise<Workspace> {
  return request<Workspace>('/workspaces', { method: 'POST', body: JSON.stringify({ path }) })
}

export async function probeWorkspace(path: string): Promise<WorkspaceProbe> {
	return request<WorkspaceProbe>('/workspaces/probe', { method: 'POST', body: JSON.stringify({ path }) })
}

export async function analyzeWorkspace(path: string, script: string): Promise<WorkspaceImportDraft> {
	return request<WorkspaceImportDraft>('/workspace-imports/analyze', { method: 'POST', body: JSON.stringify({ path, script }) })
}

export async function applyWorkspaceDraft(draftId: string, candidateId: string): Promise<OperationRef> {
	return request<OperationRef>(`/workspace-imports/drafts/${encodeURIComponent(draftId)}/apply`, {
		method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ candidateId }),
	})
}

export async function correctWorkspaceDraft(draftId: string, input: {
	candidateId: string
	systemName: string
	description: string
	serviceDisplayNames: Record<string, string>
	portPreferred: Record<string, number>
	composeRunning: Record<string, boolean>
	composeBuild: boolean
}): Promise<WorkspaceImportDraft> {
	return request<WorkspaceImportDraft>(`/workspace-imports/drafts/${encodeURIComponent(draftId)}/corrections`, {
		method: 'POST', body: JSON.stringify(input),
	})
}

export async function getWorkspaceImportOperation(id: string): Promise<WorkspaceImportOperation> {
	return request<WorkspaceImportOperation>(`/workspace-imports/operations/${encodeURIComponent(id)}`)
}

export async function unregisterWorkspace(id: string): Promise<void> {
  await request<void>(`/workspaces/${encodeURIComponent(id)}`, {
    method: 'DELETE', headers: { 'Content-Type': 'application/json' },
  })
}

export async function getWorkspace(id: string): Promise<WorkspaceDetail> {
	return request<WorkspaceDetail>(`/workspaces/${encodeURIComponent(id)}`)
}

export async function createWorkspaceEditDraft(id: string, input: {
	systemName: string
	description: string
	serviceDisplayNames: Record<string, string>
	portPreferred: Record<string, number>
}): Promise<WorkspaceImportDraft> {
	return request<WorkspaceImportDraft>(`/workspaces/${encodeURIComponent(id)}/edit-drafts`, { method: 'POST', body: JSON.stringify(input) })
}

export async function createWorkspaceRelinkDraft(id: string, path: string): Promise<WorkspaceImportDraft> {
	return request<WorkspaceImportDraft>(`/workspaces/${encodeURIComponent(id)}/relink-drafts`, {
		method: 'POST', body: JSON.stringify({ path }),
	})
}

export async function refreshWorkspace(id: string): Promise<Workspace> {
  return request<Workspace>(`/workspaces/${encodeURIComponent(id)}/refresh`, { method: 'POST', body: '{}' })
}

export async function listSystems(): Promise<SystemSummary[]> {
  return (await request<{ items: SystemSummary[] }>('/systems')).items
}

export async function getSystem(id: string, workspaceId: string): Promise<SystemDetail> {
  const query = new URLSearchParams({ workspaceId })
  return request<SystemDetail>(`/systems/${encodeURIComponent(id)}?${query.toString()}`)
}

export async function getSystemStatus(id: string, workspaceId: string): Promise<SystemRuntimeStatus> {
  const query = new URLSearchParams({ workspaceId })
  return request<SystemRuntimeStatus>(`/systems/${encodeURIComponent(id)}/status?${query.toString()}`)
}

export async function listOperations(workspaceId?: string): Promise<Operation[]> {
  const query = workspaceId === undefined ? '' : `?${new URLSearchParams({ workspaceId }).toString()}`
  return (await request<{ items: Operation[] }>(`/operations${query}`)).items
}

export async function getOperation(id: string): Promise<Operation> {
  return request<Operation>(`/operations/${encodeURIComponent(id)}`)
}

export async function cancelOperation(id: string): Promise<OperationRef> {
  return request<OperationRef>(`/operations/${encodeURIComponent(id)}/cancel`, { method: 'POST', body: '{}' })
}

export async function startSystem(systemId: string, workspaceId: string): Promise<OperationRef> {
  return mutateSystem(systemId, 'start', { workspaceId })
}

export async function stopSystem(systemId: string, workspaceId: string): Promise<OperationRef> {
  return mutateSystem(systemId, 'stop', { workspaceId })
}

export async function restartSystem(systemId: string, workspaceId: string): Promise<OperationRef> {
  return mutateSystem(systemId, 'restart', { workspaceId })
}

export async function restartService(systemId: string, serviceId: string, workspaceId: string): Promise<OperationRef> {
  return request<OperationRef>(`/services/${encodeURIComponent(systemId)}/${encodeURIComponent(serviceId)}/restart`, {
    method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ workspaceId }),
  })
}

async function mutateSystem(systemId: string, action: 'start' | 'stop' | 'restart', body: object): Promise<OperationRef> {
  return request<OperationRef>(`/systems/${encodeURIComponent(systemId)}/${action}`, {
    method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify(body),
  })
}

export async function getServiceLogs(systemId: string, serviceId: string, instanceId: string): Promise<LogEntry[]> {
  const query = new URLSearchParams({ instanceId, limit: '500' })
  const page = await request<{ items: LogEntry[]; nextCursor: number | null }>(
    `/services/${encodeURIComponent(systemId)}/${encodeURIComponent(serviceId)}/logs?${query.toString()}`,
  )
  return page.items
}

export async function listIncidents(workspaceId: string): Promise<Incident[]> {
  const query = new URLSearchParams({ workspaceId, limit: '200' })
  return (await request<{ items: Incident[] }>(`/incidents?${query.toString()}`)).items
}

export async function getIncident(id: string): Promise<IncidentDetail> {
  return request<IncidentDetail>(`/incidents/${encodeURIComponent(id)}`)
}

export async function analyzeIncident(id: string): Promise<OperationRef> {
  return request<OperationRef>(`/incidents/${encodeURIComponent(id)}/analyze`, {
    method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: '{}',
  })
}
