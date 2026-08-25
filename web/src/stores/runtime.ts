import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import {
  APIError,
  cancelOperation,
  getOperation,
  getServiceLogs,
  getSystemStatus,
  listOperations,
  restartService,
  restartSystem,
  startSystem,
  stopSystem,
} from '../api/client'
import { ResilientEventStream, StreamHTTPError } from '../api/stream'
import { isAuthenticationFailure } from '../api/auth-lifecycle'
import type { StreamConnectionState } from '../api/stream'
import type { LogEntry, Operation, OperationRef, ServiceRuntimeStatus, SystemRuntimeStatus } from '../api/types'
import {
  clearLogWindow,
  createLogWindowState,
  ingestLogEntries,
  markLogWindowRecovered,
  setLogWindowPaused,
} from '../components/logs/log-window'

const operationPollMs = 250
const operationWaitLimit = 2400
interface LogScope {
  systemId: string
  serviceId: string
  instanceId: string
}

export const useRuntimeStore = defineStore('runtime', () => {
  const status = ref<SystemRuntimeStatus | null>(null)
  const operations = ref<Operation[]>([])
  const selectedOperation = ref<Operation | null>(null)
  const logWindow = ref(createLogWindowState())
  const logs = computed(() => logWindow.value.entries)
  const logPaused = computed(() => logWindow.value.paused)
  const pausedLogCount = computed(() => logWindow.value.pausedEntries.length)
  const lastReceivedSequence = computed(() => logWindow.value.lastReceivedSequence)
  const viewFloorSequence = computed(() => logWindow.value.viewFloorSequence)
  const logViewCleared = computed(() => logWindow.value.viewCleared)
  const logConnectionState = ref<StreamConnectionState>('idle')
  const logConnectionError = ref<string | null>(null)
  const logBufferOverflow = computed(() => logWindow.value.bufferOverflow)
  const loading = ref(false)
  const mutating = ref(false)
  const error = ref<string | null>(null)
  const traceId = ref<string | null>(null)
  const activeOperation = computed(() => operations.value.find((item) => ['queued', 'running', 'cancelling'].includes(item.state)) ?? null)
  let logStream: ResilientEventStream | null = null
  let logScope: LogScope | null = null

  function captureError(reason: unknown): void {
    if (isAuthenticationFailure(reason)) {
      error.value = null
      traceId.value = null
      return
    }
    if (reason instanceof APIError) {
      error.value = `${reason.code}: ${reason.message}`
      traceId.value = reason.traceId
    } else {
      error.value = reason instanceof Error ? reason.message : '请求失败。'
      traceId.value = null
    }
  }

  async function load(systemId: string, workspaceId: string, clearError = true): Promise<void> {
    loading.value = true
    if (clearError) error.value = null
    try {
      ;[status.value, operations.value] = await Promise.all([getSystemStatus(systemId, workspaceId), listOperations(workspaceId)])
    } catch (reason: unknown) {
      captureError(reason)
    } finally {
      loading.value = false
    }
  }

  async function loadAllOperations(clearError = true): Promise<void> {
    loading.value = true
    if (clearError) error.value = null
    try {
      operations.value = await listOperations()
    } catch (reason: unknown) {
      captureError(reason)
    } finally {
      loading.value = false
    }
  }

  async function mutate(systemId: string, workspaceId: string, action: 'start' | 'stop' | 'restart'): Promise<boolean> {
    const submit = action === 'start' ? startSystem : action === 'stop' ? stopSystem : restartSystem
    return runMutation(systemId, workspaceId, () => submit(systemId, workspaceId))
  }

  async function restartOne(systemId: string, workspaceId: string, serviceId: string): Promise<boolean> {
    return runMutation(systemId, workspaceId, () => restartService(systemId, serviceId, workspaceId))
  }

  async function runMutation(systemId: string, workspaceId: string, submit: () => Promise<OperationRef>): Promise<boolean> {
    mutating.value = true
    error.value = null
    try {
      const reference = await submit()
      selectedOperation.value = await waitForOperation(reference.operationId)
      await load(systemId, workspaceId)
      if (selectedOperation.value.state === 'succeeded') return true
      error.value = selectedOperation.value.errorCode === undefined
        ? `操作${selectedOperation.value.state === 'cancelled' ? '已取消' : '失败'}。`
        : `${selectedOperation.value.errorCode}: 操作执行失败。`
      traceId.value = null
      return false
    } catch (reason: unknown) {
      await load(systemId, workspaceId, false)
      captureError(reason)
      return false
    } finally {
      mutating.value = false
    }
  }

  async function waitForOperation(id: string): Promise<Operation> {
    for (let attempt = 0; attempt < operationWaitLimit; attempt += 1) {
      const operation = await getOperation(id)
      selectedOperation.value = operation
      if (['succeeded', 'failed', 'cancelled'].includes(operation.state)) return operation
      await new Promise<void>((resolve) => window.setTimeout(resolve, operationPollMs))
    }
    throw new Error('操作等待超时。')
  }

  async function selectOperation(operation: Operation): Promise<void> {
    try {
      selectedOperation.value = await getOperation(operation.id)
    } catch (reason: unknown) {
      captureError(reason)
    }
  }

  async function cancelSelected(): Promise<void> {
    if (selectedOperation.value === null) return
    try {
      await cancelOperation(selectedOperation.value.id)
      selectedOperation.value = await getOperation(selectedOperation.value.id)
    } catch (reason: unknown) {
      captureError(reason)
    }
  }

  async function loadLogs(systemId: string, service: ServiceRuntimeStatus, instanceId: string): Promise<void> {
    stopLogStream()
    const scope = { systemId, serviceId: service.serviceId, instanceId }
    logScope = scope
    try {
      const initial = await getServiceLogs(systemId, service.serviceId, instanceId)
      if (!sameLogScope(logScope, scope)) return
      logWindow.value = createLogWindowState(initial)
    } catch (reason: unknown) {
      if (!sameLogScope(logScope, scope)) return
      captureError(reason)
      logWindow.value = createLogWindowState()
      logScope = null
      return
    }
    startLogStream()
  }

  function startLogStream(): void {
    if (logScope === null) return
    const afterSequence = logWindow.value.lastReceivedSequence
    logStream = new ResilientEventStream({
      url: () => logStreamURL(logScope),
      cursor: String(afterSequence),
      cursorQueryParameter: 'afterSequence',
      onEvent(event) {
        if (event.type !== 'log.entry') return
        appendLogEntry(parseLogEntry(event.data))
      },
      onState(state, reason) {
        logConnectionState.value = state
        logConnectionError.value = state === 'connected' || state === 'idle' ? null : streamErrorMessage(reason)
      },
      async onCursorExpired() {
        return recoverLogWindow(true)
      },
      async onRetry() {
        return recoverLogWindow(false)
      },
    })
    logStream.start()
  }

  function appendLogEntry(entry: LogEntry | null): void {
    if (entry === null) return
    logWindow.value = ingestLogEntries(logWindow.value, [entry])
  }

  async function recoverLogWindow(replace: boolean): Promise<string | null> {
    const scope = logScope
    if (scope === null) return null
    const latest = await getServiceLogs(scope.systemId, scope.serviceId, scope.instanceId)
    if (!sameLogScope(logScope, scope)) return null
    logWindow.value = ingestLogEntries(logWindow.value, latest, replace ? 'replace' : 'merge')
    return String(logWindow.value.lastReceivedSequence)
  }

  function setLogPaused(value: boolean): void {
    const needsRecovery = logWindow.value.bufferOverflow && !value
    logWindow.value = setLogWindowPaused(logWindow.value, value)
    if (needsRecovery) void restoreOverflowedLogWindow()
  }

  function clearLogView(): void {
    logWindow.value = clearLogWindow(logWindow.value)
  }

  async function restoreOverflowedLogWindow(): Promise<void> {
    try {
      await recoverLogWindow(true)
      logWindow.value = markLogWindowRecovered(logWindow.value)
    } catch (reason: unknown) {
      captureError(reason)
    }
  }

  function stopLogStream(): void {
    logStream?.stop()
    logStream = null
    logScope = null
    logWindow.value = createLogWindowState()
    logConnectionState.value = 'idle'
    logConnectionError.value = null
  }

  function clear(): void {
    status.value = null
    operations.value = []
    selectedOperation.value = null
    stopLogStream()
  }

  return {
    status, operations, selectedOperation, logs, logPaused, pausedLogCount, lastReceivedSequence, viewFloorSequence,
    logViewCleared, logConnectionState, logConnectionError, logBufferOverflow,
    loading, mutating, error, traceId, activeOperation,
    load, loadAllOperations, mutate, restartOne, selectOperation, cancelSelected, loadLogs, setLogPaused, clearLogView,
    stopLogStream, clear,
  }
})

function parseLogEntry(data: string): LogEntry | null {
  try {
    const value: unknown = JSON.parse(data)
    if (typeof value !== 'object' || value === null) return null
    const entry = value as Partial<LogEntry>
    if (typeof entry.sequence !== 'number' || typeof entry.message !== 'string') return null
    return entry as LogEntry
  } catch {
    return null
  }
}

function logStreamURL(scope: LogScope | null): string {
  if (scope === null) return '/api/v1/log-stream'
  const query = new URLSearchParams({ serviceId: scope.serviceId, instanceId: scope.instanceId })
  return `/api/v1/log-stream?${query.toString()}`
}

function streamErrorMessage(reason: Error | null): string | null {
  if (reason === null) return null
  if (isAuthenticationFailure(reason)) return null
  return reason instanceof StreamHTTPError ? `${reason.code}: ${reason.message}` : reason.message
}

function sameLogScope(left: LogScope | null, right: LogScope): boolean {
  return left !== null && left.systemId === right.systemId
    && left.serviceId === right.serviceId && left.instanceId === right.instanceId
}
