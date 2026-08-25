import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { analyzeIncident, APIError, getIncident, getOperation, listIncidents } from '../api/client'
import { isAuthenticationFailure } from '../api/auth-lifecycle'
import type { Incident, IncidentDetail } from '../api/types'

export const useIncidentStore = defineStore('incidents', () => {
  const items = ref<Incident[]>([])
  const selected = ref<IncidentDetail | null>(null)
  const loading = ref(false)
  const analyzing = ref(false)
  const error = ref<string | null>(null)
  const traceId = ref<string | null>(null)
  const openCount = computed(() => items.value.filter((item) => item.state === 'open').length)

  function captureError(reason: unknown): void {
    if (isAuthenticationFailure(reason)) {
      error.value = null
      traceId.value = null
      return
    }
    if (reason instanceof APIError) {
      error.value = `${reason.code}: ${reason.message}`
      traceId.value = reason.traceId
      return
    }
    error.value = reason instanceof Error ? reason.message : '事故数据加载失败。'
    traceId.value = null
  }

  async function load(workspaceIds: string[], clearError = true): Promise<void> {
    loading.value = true
    if (clearError) error.value = null
    try {
      const pages = await Promise.all(workspaceIds.map(listIncidents))
      items.value = pages.flat().sort((left, right) => right.lastSeenAt.localeCompare(left.lastSeenAt))
    } catch (reason: unknown) {
      captureError(reason)
    } finally {
      loading.value = false
    }
  }

  async function select(item: Incident): Promise<void> {
    loading.value = true
    error.value = null
    try {
      selected.value = await getIncident(item.id)
    } catch (reason: unknown) {
      captureError(reason)
    } finally {
      loading.value = false
    }
  }

  async function analyzeSelected(): Promise<void> {
    if (selected.value === null || analyzing.value) return
    analyzing.value = true
    error.value = null
    try {
      const reference = await analyzeIncident(selected.value.incident.id)
      await waitForAnalysis(reference.operationId)
      selected.value = await getIncident(selected.value.incident.id)
    } catch (reason: unknown) {
      captureError(reason)
    } finally {
      analyzing.value = false
    }
  }

  async function waitForAnalysis(operationId: string): Promise<void> {
    for (let attempt = 0; attempt < 120; attempt += 1) {
      const operation = await getOperation(operationId)
      if (operation.state === 'succeeded') return
      if (['failed', 'cancelled'].includes(operation.state)) throw new Error(operation.errorCode ?? '事故复查失败。')
      await new Promise((resolve) => window.setTimeout(resolve, 250))
    }
    throw new Error('事故复查等待超时。')
  }

  return { items, selected, loading, analyzing, error, traceId, openCount, load, select, analyzeSelected }
})
