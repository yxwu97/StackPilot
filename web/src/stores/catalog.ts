import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { APIError, getSystem, listSystems, listWorkspaces, refreshWorkspace, registerWorkspace, unregisterWorkspace } from '../api/client'
import { isAuthenticationFailure } from '../api/auth-lifecycle'
import type { SystemDetail, SystemSummary, Workspace } from '../api/types'

export const useCatalogStore = defineStore('catalog', () => {
  const systems = ref<SystemSummary[]>([])
  const workspaces = ref<Workspace[]>([])
  const selected = ref<SystemDetail | null>(null)
  const loading = ref(false)
  const mutating = ref(false)
  const error = ref<string | null>(null)
  const traceId = ref<string | null>(null)
  const invalidCount = computed(() => workspaces.value.filter((item) => item.manifestStatus === 'invalid').length)

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
    error.value = reason instanceof Error ? reason.message : '请求失败。'
    traceId.value = null
  }

  async function load(clearError = true): Promise<void> {
    loading.value = true
    if (clearError) error.value = null
    try {
      ;[systems.value, workspaces.value] = await Promise.all([listSystems(), listWorkspaces()])
    } catch (reason: unknown) {
      captureError(reason)
    } finally {
      loading.value = false
    }
  }

  async function selectSystem(system: SystemSummary, clearError = true): Promise<void> {
    loading.value = true
    if (clearError) error.value = null
    try {
      selected.value = await getSystem(system.id, system.workspaceId)
    } catch (reason: unknown) {
      captureError(reason)
    } finally {
      loading.value = false
    }
  }

  async function addWorkspace(path: string): Promise<boolean> {
    mutating.value = true
    error.value = null
    try {
      await registerWorkspace(path)
      await load()
      return true
    } catch (reason: unknown) {
      captureError(reason)
      return false
    } finally {
      mutating.value = false
    }
  }

  async function removeWorkspace(id: string): Promise<boolean> {
    mutating.value = true
    error.value = null
    try {
      await unregisterWorkspace(id)
      selected.value = null
      await load()
      return true
    } catch (reason: unknown) {
      captureError(reason)
      return false
    } finally {
      mutating.value = false
    }
  }

  async function refreshManifest(id: string): Promise<boolean> {
    mutating.value = true
    error.value = null
    const selectedSystem = selected.value?.system
    try {
      await refreshWorkspace(id)
      await load()
      if (selectedSystem !== undefined && selectedSystem.workspaceId === id) {
        const current = systems.value.find((system) => system.workspaceId === id)
        if (current !== undefined) await selectSystem(current)
      }
      return true
    } catch (reason: unknown) {
      await load(false)
      const current = systems.value.find((system) => system.workspaceId === id)
      if (selected.value !== null && current !== undefined && selected.value.system.workspaceId === id) {
        selected.value = { ...selected.value, system: current }
      }
      captureError(reason)
      return false
    } finally {
      mutating.value = false
    }
  }

  return {
    systems, workspaces, selected, loading, mutating, error, traceId, invalidCount,
    load, selectSystem, addWorkspace, removeWorkspace, refreshManifest,
  }
})
