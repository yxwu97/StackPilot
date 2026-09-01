import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { APIError, createChangePlan, createVerifiedRestart, getChangePlan, getOperation, getWorkspaceMetrics, listOperations, listRevisions } from '../api/client'
import { isAuthenticationFailure } from '../api/auth-lifecycle'
import type { ChangePlan, MetricSeriesList, Operation, RevisionSummary } from '../api/types'
import { planIDFromOperation } from '../observability/model'

const metricRefreshMilliseconds = 15_000
const operationPollMilliseconds = 500
const operationWaitMilliseconds = 60_000

export const useObservabilityChangeStore = defineStore('observability-change', () => {
  const workspaceId = ref<string | null>(null)
  const serviceIds = ref<string[]>([])
  const capabilities = ref<string[]>([])
  const metricHours = ref<1 | 24>(1)
  const metrics = ref<MetricSeriesList | null>(null)
  const revisions = ref<RevisionSummary[]>([])
  const plan = ref<ChangePlan | null>(null)
  const planOperation = ref<Operation | null>(null)
  const restartOperation = ref<Operation | null>(null)
  const loadingMetrics = ref(false)
  const loadingPlan = ref(false)
  const restarting = ref(false)
  const error = ref<string | null>(null)
  const errorCode = ref<string | null>(null)
  const traceId = ref<string | null>(null)
  let metricTimer: ReturnType<typeof setInterval> | null = null

  const metricsEnabled = computed(() => capabilities.value.includes('phase3.resource-monitoring'))
  const planningEnabled = computed(() => capabilities.value.includes('phase3.change-planning'))
  const verifiedRestartEnabled = computed(() => capabilities.value.includes('phase3.verified-restart'))

  function configure(id: string, services: string[], enabledCapabilities: string[]): void {
    if (workspaceId.value !== id) {
      workspaceId.value = id
      metrics.value = null
      revisions.value = []
      plan.value = null
      planOperation.value = null
      restartOperation.value = null
    }
    serviceIds.value = [...services]
    capabilities.value = [...enabledCapabilities]
  }

  async function load(): Promise<void> {
    clearError()
    await Promise.all([loadMetrics(), loadPlanningFacts()])
  }

  async function loadMetrics(): Promise<void> {
    const id = workspaceId.value
    if (id === null || !metricsEnabled.value || loadingMetrics.value) return
    loadingMetrics.value = true
    const to = new Date()
    const from = new Date(to.getTime() - metricHours.value * 60 * 60 * 1000)
    try {
      const value = await getWorkspaceMetrics(id, from, to, metricHours.value === 1 ? 'detail' : 'hour', serviceIds.value)
      if (workspaceId.value === id) metrics.value = value
    } catch (reason: unknown) {
      captureError(reason)
    } finally {
      loadingMetrics.value = false
    }
  }

  async function loadPlanningFacts(): Promise<void> {
    const id = workspaceId.value
    if (id === null || !planningEnabled.value || loadingPlan.value) return
    loadingPlan.value = true
    try {
      const [revisionItems, operations] = await Promise.all([listRevisions(id), listOperations(id)])
      if (workspaceId.value !== id) return
      revisions.value = revisionItems
      const operation = operations.find((item) => item.type === 'change-plan' && item.state === 'succeeded') ?? null
      planOperation.value = operation
      const planId = operation === null ? null : planIDFromOperation(operation)
      plan.value = planId === null ? null : await getChangePlan(planId)
    } catch (reason: unknown) {
      captureError(reason)
    } finally {
      loadingPlan.value = false
    }
  }

  async function generatePlan(): Promise<void> {
    const id = workspaceId.value
    if (id === null || !planningEnabled.value) return
    loadingPlan.value = true
    clearError()
    try {
      const submitted = await createChangePlan(id)
      const operation = await waitForOperation(submitted.operationId, id)
      planOperation.value = operation
      if (operation.state !== 'succeeded') throw new Error(operation.errorCode ?? 'CHANGE_PLAN_FAILED')
      const planId = planIDFromOperation(operation)
      if (planId === null) throw new Error('CHANGE_PLAN_RESULT_MISSING')
      plan.value = await getChangePlan(planId)
      revisions.value = await listRevisions(id)
    } catch (reason: unknown) {
      captureError(reason)
    } finally {
      loadingPlan.value = false
    }
  }

  async function verifiedRestart(): Promise<void> {
    const id = workspaceId.value
    if (id === null || plan.value === null || !verifiedRestartEnabled.value) return
    restarting.value = true
    clearError()
    try {
      const submitted = await createVerifiedRestart(id, plan.value.id)
      restartOperation.value = await waitForOperation(submitted.operationId, id)
      if (restartOperation.value.state !== 'succeeded') {
        throw new Error(restartOperation.value.errorCode ?? 'VERIFICATION_FAILED')
      }
    } catch (reason: unknown) {
      captureError(reason)
    } finally {
      restarting.value = false
    }
  }

  function startMetricPolling(): void {
    stopMetricPolling()
    if (!metricsEnabled.value) return
    metricTimer = setInterval(() => void loadMetrics(), metricRefreshMilliseconds)
  }

  function stopMetricPolling(): void {
    if (metricTimer !== null) clearInterval(metricTimer)
    metricTimer = null
  }

  function clearError(): void {
    error.value = null
    errorCode.value = null
    traceId.value = null
  }

  function captureError(reason: unknown): void {
    if (isAuthenticationFailure(reason)) return
    if (reason instanceof APIError) {
      errorCode.value = reason.code
      error.value = reason.message
      traceId.value = reason.traceId
      return
    }
    const message = reason instanceof Error ? reason.message : '请求失败。'
    errorCode.value = /^[A-Z][A-Z0-9_]+$/.test(message) ? message : null
    error.value = message
    traceId.value = null
  }

  return {
    metricHours, metrics, revisions, plan, planOperation, restartOperation, loadingMetrics, loadingPlan, restarting,
    error, errorCode, traceId, metricsEnabled, planningEnabled, verifiedRestartEnabled,
    configure, load, loadMetrics, loadPlanningFacts, generatePlan, verifiedRestart, startMetricPolling, stopMetricPolling,
  }
})

async function waitForOperation(operationId: string, expectedWorkspaceId: string): Promise<Operation> {
  const deadline = Date.now() + operationWaitMilliseconds
  while (Date.now() < deadline) {
    const operation = await getOperation(operationId)
    if (operation.workspaceId !== expectedWorkspaceId) throw new Error('OPERATION_SCOPE_MISMATCH')
    if (['succeeded', 'failed', 'cancelled'].includes(operation.state)) return operation
    await new Promise<void>((resolve) => setTimeout(resolve, operationPollMilliseconds))
  }
  throw new Error('OPERATION_WAIT_TIMEOUT')
}
