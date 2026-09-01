<script setup lang="ts">
import { ArrowLeft, Box, Delete, Document, FolderAdd, Monitor, Refresh, RefreshRight, Search, Setting, SwitchButton, TopRight, VideoPlay, Warning } from '@element-plus/icons-vue'
import { ElAlert, ElButton, ElDialog, ElDrawer, ElEmpty, ElIcon, ElInput, ElSkeleton, ElTabPane, ElTable, ElTableColumn, ElTabs, ElTag, ElTimeline, ElTimelineItem, ElTooltip } from 'element-plus'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { APIError, getSystemStatus, getVersion } from './api/client'
import type { Incident, Operation, ServiceRuntimeStatus, SystemSummary, Workspace } from './api/types'
import ServiceLogViewer from './components/logs/ServiceLogViewer.vue'
import ObservabilityChangePanel from './components/observability/ObservabilityChangePanel.vue'
import WorkspaceImportDialog from './components/workspaces/WorkspaceImportDialog.vue'
import WorkspaceDetailDrawer from './components/workspaces/WorkspaceDetailDrawer.vue'
import { useCatalogStore } from './stores/catalog'
import { useEventStore } from './stores/events'
import { useIncidentStore } from './stores/incidents'
import { useRuntimeStore } from './stores/runtime'
import { useSessionStore } from './stores/session'
import { useWorkspaceImportStore } from './stores/workspace-import'

type ViewName = 'systems' | 'workspaces' | 'operations' | 'incidents' | 'detail'

const catalog = useCatalogStore()
const domainEvents = useEventStore()
const runtime = useRuntimeStore()
const incidents = useIncidentStore()
const session = useSessionStore()
const workspaceImport = useWorkspaceImportStore()
const authenticationTitle = computed(() => {
  if (session.state === 'expired') return '浏览器会话已失效'
  if (session.state === 'bootstrap-invalid') return '启动链接已失效'
  return '无法连接本地控制面'
})
const controlPlaneAddress = window.location.host
const systemVersion = ref<string | null>(null)
const systemCapabilities = ref<string[]>([])
const versionLoaded = ref(false)
const systemVersionText = computed(() => systemVersion.value === null ? '--' : `v${systemVersion.value}`)
const systemVersionTitle = computed(() => versionLoaded.value && systemVersion.value === null
  ? '系统版本暂不可用'
  : `系统版本 ${systemVersionText.value}`)
const view = ref<ViewName>('systems')
const query = ref('')
const addDialogOpen = ref(false)
const removeDialogOpen = ref(false)
const workspaceDetailOpen = ref(false)
const workspaceDetailID = ref<string | null>(null)
const pendingRemoval = ref<Workspace | null>(null)
const pendingRemovalBlockReason = computed(() => pendingRemoval.value === null ? null : workspaceRemovalBlockReason(pendingRemoval.value))
const detailTab = ref('overview')
const serviceDrawerOpen = ref(false)
const operationDrawerOpen = ref(false)
const incidentDrawerOpen = ref(false)
const selectedService = ref<ServiceRuntimeStatus | null>(null)
const overviewMutation = ref<{ systemId: string; action: 'start' | 'stop' | 'restart' } | null>(null)
const openingWebSystemId = ref<string | null>(null)
const overviewActionError = ref<{ message: string; traceId: string | null } | null>(null)
let serviceDrawerTrigger: HTMLElement | null = null
const currentManifestInvalid = computed(() => catalog.selected?.system.manifestStatus === 'invalid')
const staleRuntimeSnapshot = computed(() => {
  const currentDigest = catalog.selected?.manifest.digest
  const runtimeDigest = runtime.status?.manifestDigest
  return runtime.status?.state !== 'stopped' && currentDigest !== undefined && runtimeDigest !== undefined && currentDigest !== runtimeDigest
})
const webEntry = computed(() => {
  const port = runtime.status?.ports.find((item) => item.logicalName === 'web')?.port
  return port === undefined ? null : `http://127.0.0.1:${port}`
})
const readyServiceCount = computed(() => runtime.status?.services.filter(
  (item) => item.state === 'ready' || item.state === 'completed',
).length ?? 0)
const incidentResults = computed(() => {
  const analyses = incidents.selected?.analyses ?? []
  return analyses.length === 0 ? [] : analyses[analyses.length - 1].result.results
})
const viewTitle = computed(() => {
  if (view.value === 'workspaces') return '工作区管理'
  if (view.value === 'operations') return '操作中心'
  if (view.value === 'incidents') return '事故中心'
  if (view.value === 'detail') return catalog.selected?.system.name ?? '系统详情'
  return '系统总览'
})

const filteredSystems = computed(() => {
  const value = query.value.trim().toLocaleLowerCase()
  if (value === '') return catalog.systems
  return catalog.systems.filter((system) =>
    [system.name, system.id, system.workspacePath].some((field) => field.toLocaleLowerCase().includes(value)),
  )
})

onMounted(() => {
  void loadSystemVersion()
  void authenticateAndLoad()
})
onBeforeUnmount(() => {
  domainEvents.stop()
  runtime.stopLogStream()
  session.dispose()
})
watch(() => session.state, (value) => {
  if (value === 'ready' || value === 'loading') return
  domainEvents.stop()
  runtime.stopLogStream()
})

async function loadSystemVersion(): Promise<void> {
  try {
    const version = await getVersion()
    systemVersion.value = version.version
    systemCapabilities.value = version.capabilities
  } catch {
    systemVersion.value = null
    systemCapabilities.value = []
  } finally {
    versionLoaded.value = true
  }
}

async function authenticateAndLoad(): Promise<void> {
  domainEvents.stop()
  try {
    await session.initialize()
    await catalog.load()
	openWorkspaceImportHandoff()
    if (!session.ready) return
    domainEvents.start(refreshSnapshots)
  } catch {
    // The session store owns the durable recovery state and user-facing message.
  }
}

function openWorkspaceImportHandoff(): void {
	const fragment = new URLSearchParams(window.location.hash.startsWith('#') ? window.location.hash.slice(1) : '')
	const path = fragment.get('workspace-import')
	if (path === null) return
	workspaceImport.path = path
	addDialogOpen.value = true
	window.history.replaceState(null, '', `${window.location.pathname}${window.location.search}`)
}

async function refreshSnapshots(): Promise<void> {
  const selected = catalog.selected?.system
  await catalog.load(false)
  if (view.value === 'operations') {
    await runtime.loadAllOperations(false)
    return
  }
  if (view.value === 'incidents') {
    await incidents.load(catalog.workspaces.map((item) => item.id), false)
    return
  }
  if (selected === undefined || view.value !== 'detail') return
  const current = catalog.systems.find((system) => system.workspaceId === selected.workspaceId)
  if (current === undefined) {
    runtime.clear()
    view.value = 'systems'
    return
  }
  await Promise.all([catalog.selectSystem(current, false), runtime.load(current.id, current.workspaceId, false)])
}

async function openSystem(system: SystemSummary): Promise<void> {
  await Promise.all([catalog.selectSystem(system), runtime.load(system.id, system.workspaceId)])
  if (catalog.selected !== null) view.value = 'detail'
}

async function openSystems(): Promise<void> {
  view.value = 'systems'
  await catalog.load()
}

async function openOperations(): Promise<void> {
  view.value = 'operations'
  await runtime.loadAllOperations()
}

async function openIncidents(): Promise<void> {
  view.value = 'incidents'
  await incidents.load(catalog.workspaces.map((item) => item.id))
}

async function openIncident(item: Incident): Promise<void> {
  await incidents.select(item)
  if (incidents.selected !== null) incidentDrawerOpen.value = true
}

async function runSystemAction(action: 'start' | 'stop' | 'restart'): Promise<void> {
  const system = catalog.selected?.system
  if (system === undefined) return
  await runtime.mutate(system.id, system.workspaceId, action)
  await catalog.load()
}

async function runOverviewSystemAction(value: unknown, action: 'start' | 'stop' | 'restart'): Promise<void> {
  const system = resolveSystemSummary(value)
  if (system === null) return
  if (systemActionBlockReason(system, action) !== null) return
  overviewActionError.value = null
  overviewMutation.value = { systemId: system.id, action }
  try {
    await runtime.mutate(system.id, system.workspaceId, action)
    await catalog.load()
  } finally {
    overviewMutation.value = null
  }
}

async function openSystemWeb(value: unknown): Promise<void> {
  const system = resolveSystemSummary(value)
  if (system === null) return
  if (webActionBlockReason(system) !== null) return
  overviewActionError.value = null
  const target = window.open('about:blank', '_blank')
  if (target === null) {
    overviewActionError.value = { message: '浏览器阻止了新窗口，请允许本站打开新窗口后重试。', traceId: null }
    return
  }
  target.opener = null
  openingWebSystemId.value = system.id
  try {
    const status = await getSystemStatus(system.id, system.workspaceId)
    const port = status.ports.find((item) => item.logicalName === 'web')?.port
    if (port === undefined) throw new Error('当前运行实例没有可用的 Web 端口。')
    target.location.replace(`http://127.0.0.1:${port}`)
  } catch (reason: unknown) {
    target.close()
    overviewActionError.value = reason instanceof APIError
      ? { message: `${reason.code}: ${reason.message}`, traceId: reason.traceId }
      : { message: reason instanceof Error ? reason.message : '打开 Web 失败。', traceId: null }
  } finally {
    openingWebSystemId.value = null
  }
}

function systemActionBlockReason(value: unknown, action: 'start' | 'stop' | 'restart'): string | null {
  const system = resolveSystemSummary(value)
  if (system === null) return '系统状态不可用，请重新加载'
  if (runtime.mutating) return '另一项系统操作正在执行'
  if (system.activeOperationId !== null) return '系统存在活动 Operation'
  if (action !== 'stop' && system.manifestStatus === 'invalid') return '系统清单无效'
  if (action === 'start' && system.state !== 'stopped') return '仅已停止的系统可以启动'
  if (action !== 'start' && system.state === 'stopped') return `已停止的系统不能${action === 'stop' ? '停止' : '重启'}`
  return null
}

function webActionBlockReason(value: unknown): string | null {
  const system = resolveSystemSummary(value)
  if (system === null) return '系统状态不可用，请重新加载'
  if (openingWebSystemId.value !== null) return '正在解析 Web 地址'
  if (system.activeOperationId !== null) return '系统存在活动 Operation'
  if (!['running', 'degraded'].includes(system.state)) return '系统运行后才能打开 Web'
  return null
}

function overviewActionLoading(value: unknown, action: 'start' | 'stop' | 'restart'): boolean {
  const system = resolveSystemSummary(value)
  if (system === null) return false
  return overviewMutation.value?.systemId === system.id && overviewMutation.value.action === action
}

function resolveSystemSummary(value: unknown): SystemSummary | null {
  if (typeof value !== 'object' || value === null || !('id' in value) || !('workspaceId' in value)) return null
  if (typeof value.id !== 'string' || typeof value.workspaceId !== 'string') return null
  return catalog.systems.find((item) => item.id === value.id && item.workspaceId === value.workspaceId) ?? null
}

async function refreshManifest(workspaceId: string): Promise<void> {
  if (!(await catalog.refreshManifest(workspaceId))) return
  const selected = catalog.selected?.system
  if (selected !== undefined && selected.workspaceId === workspaceId) {
    await runtime.load(selected.id, selected.workspaceId, false)
  }
}

function openService(service: ServiceRuntimeStatus, event: Event): void {
  serviceDrawerTrigger = event.currentTarget instanceof HTMLElement ? event.currentTarget : null
  selectedService.value = service
  serviceDrawerOpen.value = true
}

async function restartSelectedService(): Promise<void> {
  const system = catalog.selected?.system
  if (system === undefined || selectedService.value === null) return
  await runtime.restartOne(system.id, system.workspaceId, selectedService.value.serviceId)
  await catalog.load()
  selectedService.value = runtime.status?.services.find((item) => item.serviceId === selectedService.value?.serviceId) ?? null
}

async function reloadCurrentView(): Promise<void> {
  if (view.value === 'operations') {
    await runtime.loadAllOperations()
    return
  }
  if (view.value === 'incidents') {
    await Promise.all([catalog.load(), incidents.load(catalog.workspaces.map((item) => item.id))])
    return
  }
  const system = catalog.selected?.system
  if (view.value === 'detail' && system !== undefined) {
    await Promise.all([catalog.load(), catalog.selectSystem(system), runtime.load(system.id, system.workspaceId)])
    return
  }
  await catalog.load()
}

function closeServiceDrawer(): void {
  selectedService.value = null
  const trigger = serviceDrawerTrigger
  serviceDrawerTrigger = null
  void nextTick(() => trigger?.focus())
}

async function openOperation(operation: Operation): Promise<void> {
  await runtime.selectOperation(operation)
  operationDrawerOpen.value = true
}

async function workspaceImportCompleted(): Promise<void> { await catalog.load() }

function openWorkspaceDetail(item: unknown): void {
  if (typeof item !== 'object' || item === null || !('id' in item) || typeof item.id !== 'string') return
  workspaceDetailID.value = item.id
  workspaceDetailOpen.value = true
}

function confirmRemoval(item: Workspace | undefined): void {
  if (item === undefined || workspaceRemovalBlockReason(item) !== null) return
  pendingRemoval.value = item
  removeDialogOpen.value = true
}

async function removeWorkspace(): Promise<void> {
  if (pendingRemoval.value !== null && (await catalog.removeWorkspace(pendingRemoval.value.id))) {
    if (workspaceDetailID.value === pendingRemoval.value.id) workspaceDetailOpen.value = false
    pendingRemoval.value = null
    removeDialogOpen.value = false
  }
}

function workspaceRemovalBlockReason(item: Workspace | undefined): string | null {
  if (item === undefined) return '工作区状态不可用，请先重新加载'
  const system = catalog.systems.find((candidate) => candidate.workspaceId === item.id)
  if (system === undefined) return '运行状态不可用，请先重新加载'
  if (system.activeOperationId !== null) return '存在活动 Operation，暂时不能解除注册'
  if (system.state !== 'stopped') return '请先停止系统，再解除注册'
  return null
}

function shortDigest(value: string): string {
  return `${value.slice(0, 10)}...${value.slice(-6)}`
}

function formatTime(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(new Date(value))
}

function runtimeTag(state: string): 'success' | 'warning' | 'danger' | 'info' | 'primary' {
  if (state === 'running' || state === 'ready' || state === 'succeeded') return 'success'
  if (state === 'failed') return 'danger'
  if (state === 'starting' || state === 'stopping' || state === 'running' || state === 'cancelling') return 'warning'
  return 'info'
}

function stateLabel(state: string): string {
  const labels: Record<string, string> = {
    stopped: '已停止', running: '运行中', starting: '启动中', stopping: '停止中', degraded: '已降级', failed: '失败',
    ready: '就绪', waiting_dependency: '等待依赖', waiting_ready: '等待就绪', unknown: '未知', queued: '排队中',
    cancelling: '取消中', succeeded: '成功', cancelled: '已取消', pending: '待执行', skipped: '已跳过', completed: '已完成',
  }
  return labels[state] ?? state
}

function completedSteps(steps: Operation['steps']): number {
  return steps.filter((step) => step.state === 'succeeded').length
}

function severityTag(value: string): 'danger' | 'warning' | 'info' {
  if (value === 'critical') return 'danger'
  if (value === 'warning') return 'warning'
  return 'info'
}
</script>

<template>
  <main v-if="!session.ready" class="authentication-state" aria-live="polite">
    <ElSkeleton v-if="session.state === 'loading' || session.state === 'idle'" :rows="3" animated />
    <ElAlert v-else :title="authenticationTitle" :description="session.error" type="error" :closable="false" show-icon />
    <ElButton v-if="session.state === 'unreachable'" :icon="Refresh" @click="authenticateAndLoad">重新检测</ElButton>
    <p class="authentication-version" :title="systemVersionTitle">系统版本 <code>{{ systemVersionText }}</code></p>
  </main>
  <div v-else class="app-shell">
    <aside class="sidebar">
      <div class="brand">
        <span class="brand-mark" aria-hidden="true"><i></i><i></i><i></i></span>
        <div><strong>StackPilot</strong><small>Local control plane</small></div>
      </div>
      <nav aria-label="主导航">
        <button class="nav-item" :class="{ active: view === 'systems' || view === 'detail' }" type="button" @click="openSystems">
          <ElIcon><Monitor /></ElIcon><span>系统总览</span>
        </button>
        <button class="nav-item" :class="{ active: view === 'workspaces' }" type="button" @click="view = 'workspaces'">
          <ElIcon><Setting /></ElIcon><span>工作区</span><span v-if="catalog.invalidCount > 0" class="nav-count">{{ catalog.invalidCount }}</span>
        </button>
        <button class="nav-item" :class="{ active: view === 'operations' }" type="button" @click="openOperations">
          <ElIcon><Document /></ElIcon><span>操作中心</span><span v-if="runtime.activeOperation !== null" class="nav-count">1</span>
        </button>
        <button class="nav-item" :class="{ active: view === 'incidents' }" type="button" @click="openIncidents">
          <ElIcon><Warning /></ElIcon><span>事故中心</span><span v-if="incidents.openCount > 0" class="nav-count">{{ incidents.openCount }}</span>
        </button>
      </nav>
      <div class="sidebar-footer">
        <div class="sidebar-status"><span class="status-dot" :class="{ interrupted: domainEvents.connectionState !== 'connected' }"></span><div><strong>控制面在线</strong><small>{{ controlPlaneAddress }}</small></div></div>
        <div class="sidebar-version" :title="systemVersionTitle" aria-live="polite"><span>系统版本</span><code>{{ systemVersionText }}</code></div>
      </div>
    </aside>

    <div class="main-shell">
      <header class="topbar">
        <div><strong>{{ viewTitle }}</strong><span>{{ catalog.systems.length }} 个已注册系统</span></div>
        <ElTooltip content="重新加载" placement="bottom"><ElButton :icon="Refresh" circle :loading="catalog.loading || runtime.loading" aria-label="重新加载" @click="reloadCurrentView" /></ElTooltip>
      </header>

      <main class="content">
        <ElAlert v-if="catalog.error !== null" class="persistent-error" :title="catalog.error" type="error" :closable="false" show-icon>
          <template v-if="catalog.traceId !== null" #default><span class="mono">{{ catalog.traceId }}</span></template>
        </ElAlert>
        <ElAlert v-if="runtime.error !== null" class="persistent-error" :title="runtime.error" type="error" :closable="false" show-icon>
          <template v-if="runtime.traceId !== null" #default><span class="mono">{{ runtime.traceId }}</span></template>
        </ElAlert>
        <ElAlert v-if="incidents.error !== null" class="persistent-error" :title="incidents.error" type="error" :closable="false" show-icon>
          <template v-if="incidents.traceId !== null" #default><span class="mono">{{ incidents.traceId }}</span></template>
        </ElAlert>
        <ElAlert
          v-if="domainEvents.connectionState === 'error'"
          class="persistent-error"
          title="实时状态连接已停止"
          :description="domainEvents.connectionError ?? '请重新建立本地会话。'"
          type="error"
          :closable="false"
          show-icon
        />
        <ElAlert
          v-else-if="domainEvents.connectionState === 'reconnecting'"
          class="persistent-error"
          title="实时状态暂时中断，正在恢复"
          :description="domainEvents.connectionError ?? undefined"
          type="warning"
          :closable="false"
          show-icon
        />

        <template v-if="view === 'systems'">
          <section class="page-header"><div><h1>系统总览</h1><p>已注册的本地系统定义与清单状态</p></div><ElButton type="primary" :icon="FolderAdd" @click="addDialogOpen = true">注册工作区</ElButton></section>
          <ElAlert v-if="overviewActionError !== null" class="persistent-error" :title="overviewActionError.message" type="error" :closable="false" show-icon>
            <template v-if="overviewActionError.traceId !== null" #default><span class="mono">{{ overviewActionError.traceId }}</span></template>
          </ElAlert>
          <section class="metrics-band" aria-label="系统统计">
            <div><span>系统</span><strong>{{ catalog.systems.length }}</strong></div>
            <div><span>服务定义</span><strong>{{ catalog.systems.reduce((sum, item) => sum + item.serviceSummary.total, 0) }}</strong></div>
            <div><span>有效清单</span><strong>{{ catalog.systems.length - catalog.invalidCount }}</strong></div>
            <div><span>无效清单</span><strong :class="{ danger: catalog.invalidCount > 0 }">{{ catalog.invalidCount }}</strong></div>
          </section>
          <section class="surface">
            <div class="surface-toolbar"><ElInput v-model="query" :prefix-icon="Search" clearable placeholder="搜索系统或工作区" aria-label="搜索系统或工作区" /><span>{{ filteredSystems.length }} 个结果</span></div>
            <div v-if="catalog.loading && catalog.systems.length === 0" class="loading-list"><ElSkeleton animated :rows="5" /></div>
            <ElEmpty v-else-if="filteredSystems.length === 0" description="没有已注册的系统"><ElButton type="primary" :icon="FolderAdd" @click="addDialogOpen = true">注册工作区</ElButton></ElEmpty>
            <ElTable v-else :data="filteredSystems" class="data-table" @row-click="openSystem">
              <ElTableColumn label="系统" min-width="220"><template #default="scope"><div class="identity-cell"><span class="system-glyph">{{ scope.row.name.slice(0, 2).toUpperCase() }}</span><div><strong>{{ scope.row.name }}</strong><small>{{ scope.row.id }}</small></div></div></template></ElTableColumn>
              <ElTableColumn label="清单" width="120"><template #default="scope"><ElTag :type="scope.row.manifestStatus === 'valid' ? 'success' : 'danger'" effect="light">{{ scope.row.manifestStatus === 'valid' ? '有效' : '无效' }}</ElTag></template></ElTableColumn>
              <ElTableColumn label="状态" width="120"><template #default="scope"><ElTag :type="runtimeTag(scope.row.state)" effect="plain">{{ stateLabel(scope.row.state) }}</ElTag></template></ElTableColumn>
              <ElTableColumn label="服务" width="100"><template #default="scope">{{ scope.row.serviceSummary.ready }} / {{ scope.row.serviceSummary.total }}</template></ElTableColumn>
              <ElTableColumn label="工作区" min-width="280"><template #default="scope"><span class="path-text" :title="scope.row.workspacePath">{{ scope.row.workspacePath }}</span></template></ElTableColumn>
              <ElTableColumn label="更新时间" width="170"><template #default="scope">{{ formatTime(scope.row.updatedAt) }}</template></ElTableColumn>
              <ElTableColumn label="操作" width="160" align="right" fixed="right">
                <template #default="scope">
                  <div class="table-actions">
                    <ElTooltip :content="systemActionBlockReason(scope.row, 'start') ?? '启动'" placement="top"><span><ElButton :icon="VideoPlay" circle size="small" type="primary" plain :loading="overviewActionLoading(scope.row, 'start')" :disabled="systemActionBlockReason(scope.row, 'start') !== null" :aria-label="`启动 ${scope.row.name}`" @click.stop="runOverviewSystemAction(scope.row, 'start')" /></span></ElTooltip>
                    <ElTooltip :content="systemActionBlockReason(scope.row, 'stop') ?? '停止'" placement="top"><span><ElButton :icon="SwitchButton" circle size="small" plain :loading="overviewActionLoading(scope.row, 'stop')" :disabled="systemActionBlockReason(scope.row, 'stop') !== null" :aria-label="`停止 ${scope.row.name}`" @click.stop="runOverviewSystemAction(scope.row, 'stop')" /></span></ElTooltip>
                    <ElTooltip :content="systemActionBlockReason(scope.row, 'restart') ?? '重启'" placement="top"><span><ElButton :icon="RefreshRight" circle size="small" plain :loading="overviewActionLoading(scope.row, 'restart')" :disabled="systemActionBlockReason(scope.row, 'restart') !== null" :aria-label="`重启 ${scope.row.name}`" @click.stop="runOverviewSystemAction(scope.row, 'restart')" /></span></ElTooltip>
                    <ElTooltip :content="webActionBlockReason(scope.row) ?? '打开 Web'" placement="top"><span><ElButton :icon="TopRight" circle size="small" plain :loading="openingWebSystemId === scope.row.id" :disabled="webActionBlockReason(scope.row) !== null" :aria-label="`打开 ${scope.row.name} Web`" @click.stop="openSystemWeb(scope.row)" /></span></ElTooltip>
                  </div>
                </template>
              </ElTableColumn>
            </ElTable>
          </section>
        </template>

        <template v-else-if="view === 'detail' && catalog.selected !== null">
          <button class="back-button" type="button" @click="openSystems"><ElIcon><ArrowLeft /></ElIcon>系统总览</button>
          <ElAlert
            v-if="currentManifestInvalid"
            class="persistent-error"
            title="当前清单无效"
            description="启动和重启已禁用；正在运行的实例仍可安全停止。"
            type="error"
            :closable="false"
            show-icon
          />
          <ElAlert
            v-else-if="staleRuntimeSnapshot"
            class="persistent-error"
            title="运行实例使用旧清单快照"
            description="当前实例继续使用启动时的定义；重启后才会应用最新清单。"
            type="warning"
            :closable="false"
            show-icon
          />
          <section class="page-header detail-heading">
            <div class="title-line"><span class="system-glyph large">{{ catalog.selected.system.name.slice(0, 2).toUpperCase() }}</span><div><h1>{{ catalog.selected.system.name }}</h1><p>{{ catalog.selected.system.workspacePath }}</p></div></div>
            <div class="header-actions">
              <ElTag :type="runtimeTag(runtime.status?.state ?? 'stopped')" size="large">{{ stateLabel(runtime.status?.state ?? 'stopped') }}</ElTag>
              <ElButton v-if="webEntry !== null" tag="a" :href="webEntry" target="_blank" rel="noopener" :icon="TopRight">打开 Web</ElButton>
              <ElButton :icon="Refresh" :loading="catalog.mutating" @click="refreshManifest(catalog.selected.system.workspaceId)">刷新清单</ElButton>
              <ElTooltip content="启动" placement="bottom"><ElButton :icon="VideoPlay" circle type="primary" :loading="runtime.mutating" :disabled="currentManifestInvalid || runtime.status?.state !== 'stopped' || runtime.activeOperation !== null" aria-label="启动系统" @click="runSystemAction('start')" /></ElTooltip>
              <ElTooltip content="停止" placement="bottom"><ElButton :icon="SwitchButton" circle :loading="runtime.mutating" :disabled="runtime.status?.state === 'stopped' || runtime.activeOperation !== null" aria-label="停止系统" @click="runSystemAction('stop')" /></ElTooltip>
              <ElTooltip content="重启" placement="bottom"><ElButton :icon="RefreshRight" circle :loading="runtime.mutating" :disabled="currentManifestInvalid || runtime.status?.state === 'stopped' || runtime.activeOperation !== null" aria-label="重启系统" @click="runSystemAction('restart')" /></ElTooltip>
            </div>
          </section>
          <dl class="definition-strip">
            <div><dt>System ID</dt><dd class="mono">{{ catalog.selected.system.id }}</dd></div>
            <div><dt>运行实例</dt><dd class="mono">{{ runtime.status?.instanceId ?? '--' }}</dd></div>
            <div><dt>Manifest digest</dt><dd class="mono" :title="runtime.status?.manifestDigest ?? catalog.selected.manifest.digest">{{ shortDigest(runtime.status?.manifestDigest ?? catalog.selected.manifest.digest) }}</dd></div>
            <div><dt>Resolved spec</dt><dd class="mono">{{ runtime.status?.resolvedSpecDigest ? shortDigest(runtime.status.resolvedSpecDigest) : '--' }}</dd></div>
          </dl>
          <ElTabs v-model="detailTab" class="detail-tabs">
            <ElTabPane label="概览" name="overview">
              <section class="metrics-band runtime-metrics" aria-label="运行统计">
                <div><span>服务就绪</span><strong>{{ readyServiceCount }} / {{ catalog.selected.services.length }}</strong></div>
                <div><span>端口</span><strong>{{ runtime.status?.ports.length ?? 0 }}</strong></div>
                <div><span>活动操作</span><strong>{{ runtime.activeOperation === null ? 0 : 1 }}</strong></div>
                <div><span>清单</span><strong class="compact-value">{{ catalog.selected.system.manifestStatus === 'valid' ? '有效' : '无效' }}</strong></div>
              </section>
              <section v-if="runtime.activeOperation !== null" class="surface operation-progress">
                <div class="section-heading"><div><h2>当前操作</h2><p class="mono">{{ runtime.activeOperation.id }}</p></div><ElTag :type="runtimeTag(runtime.activeOperation.state)">{{ stateLabel(runtime.activeOperation.state) }}</ElTag></div>
                <div class="progress-steps"><span v-for="step in runtime.activeOperation.steps" :key="step.number" :class="`step-${step.state}`">{{ step.key }}</span></div>
              </section>
            </ElTabPane>
            <ElTabPane label="服务与端口" name="services">
              <section class="surface">
                <div class="section-heading"><div><h2>服务运行状态</h2><p>依赖、进程身份与实际端口</p></div><span>{{ runtime.status?.services.length ?? 0 }} 个服务</span></div>
                <div v-if="runtime.status?.services.length" class="service-grid">
                  <button v-for="service in runtime.status.services" :key="service.serviceInstanceId" class="service-row service-button" type="button" @click="openService(service, $event)">
                    <span class="service-icon"><ElIcon><Box /></ElIcon></span><div class="service-main"><strong>{{ service.serviceId }}</strong><small>{{ service.driver }} · {{ service.mode }} · {{ service.dependsOn.length ? `依赖 ${service.dependsOn.join(', ')}` : '根服务' }}</small></div>
                    <ElTag :type="runtimeTag(service.state)" effect="plain">{{ stateLabel(service.state) }}</ElTag>
                    <div class="digest-cell"><span>PID</span><code>{{ service.pid ?? '--' }}</code></div>
                  </button>
                </div>
                <ElEmpty v-else description="系统尚未运行" />
              </section>
              <section class="surface secondary-surface">
                <div class="section-heading"><div><h2>端口计划</h2><p>本次运行的实际监听端口</p></div><span>{{ runtime.status?.ports.length ?? 0 }} 个端口</span></div>
                <ElTable :data="runtime.status?.ports ?? []" class="data-table">
                  <ElTableColumn prop="logicalName" label="逻辑端口" min-width="150" />
                  <ElTableColumn label="地址" min-width="180"><template #default="scope"><code>127.0.0.1:{{ scope.row.port }}</code></template></ElTableColumn>
                  <ElTableColumn label="来源" width="130"><template #default="scope"><ElTag :type="scope.row.replaced ? 'warning' : 'info'" effect="plain">{{ scope.row.source }}</ElTag></template></ElTableColumn>
                  <ElTableColumn label="替换" min-width="140"><template #default="scope">{{ scope.row.replaced ? `${scope.row.conflictPort} → ${scope.row.port}` : '未替换' }}</template></ElTableColumn>
                </ElTable>
              </section>
            </ElTabPane>
            <ElTabPane label="监测 / 变更" name="observability">
              <ObservabilityChangePanel
                :workspace-id="catalog.selected.system.workspaceId"
                :services="catalog.selected.services"
                :status="runtime.status"
                :active-operation="runtime.activeOperation !== null"
                :capabilities="systemCapabilities"
                :active="detailTab === 'observability'"
                @refresh="refreshSnapshots"
              />
            </ElTabPane>
            <ElTabPane label="操作" name="operations">
              <section class="surface">
                <ElEmpty v-if="runtime.operations.length === 0" description="没有操作记录" />
                <ElTable v-else :data="runtime.operations" class="data-table" @row-click="openOperation">
                  <ElTableColumn label="操作" min-width="250"><template #default="scope"><strong>{{ scope.row.type }}</strong><small class="table-sub mono">{{ scope.row.id }}</small></template></ElTableColumn>
                  <ElTableColumn label="状态" width="120"><template #default="scope"><ElTag :type="runtimeTag(scope.row.state)">{{ stateLabel(scope.row.state) }}</ElTag></template></ElTableColumn>
                  <ElTableColumn label="创建时间" width="180"><template #default="scope">{{ formatTime(scope.row.createdAt) }}</template></ElTableColumn>
                </ElTable>
              </section>
            </ElTabPane>
          </ElTabs>
        </template>

        <template v-else-if="view === 'operations'">
          <section class="page-header"><div><h1>操作中心</h1><p>持久化操作与步骤状态</p></div><ElButton :icon="Refresh" :loading="runtime.loading" @click="runtime.loadAllOperations()">刷新</ElButton></section>
          <section class="surface">
            <ElEmpty v-if="!runtime.loading && runtime.operations.length === 0" description="没有操作记录" />
            <ElTable v-else :data="runtime.operations" class="data-table" @row-click="openOperation">
              <ElTableColumn label="操作" min-width="250"><template #default="scope"><strong>{{ scope.row.type }}</strong><small class="table-sub mono">{{ scope.row.id }}</small></template></ElTableColumn>
              <ElTableColumn prop="systemId" label="系统" width="150" />
              <ElTableColumn label="状态" width="120"><template #default="scope"><ElTag :type="runtimeTag(scope.row.state)">{{ stateLabel(scope.row.state) }}</ElTag></template></ElTableColumn>
              <ElTableColumn label="步骤" width="110"><template #default="scope">{{ completedSteps(scope.row.steps) }} / {{ scope.row.steps.length }}</template></ElTableColumn>
              <ElTableColumn label="创建时间" width="180"><template #default="scope">{{ formatTime(scope.row.createdAt) }}</template></ElTableColumn>
            </ElTable>
          </section>
        </template>

        <template v-else-if="view === 'incidents'">
          <section class="page-header"><div><h1>事故中心</h1><p>运行故障、证据与确定性诊断</p></div></section>
          <section class="metrics-band" aria-label="事故统计">
            <div><span>全部</span><strong>{{ incidents.items.length }}</strong></div>
            <div><span>处理中</span><strong :class="{ danger: incidents.openCount > 0 }">{{ incidents.openCount }}</strong></div>
            <div><span>严重</span><strong>{{ incidents.items.filter((item) => item.severity === 'critical').length }}</strong></div>
            <div><span>已归并</span><strong>{{ incidents.items.reduce((sum, item) => sum + Math.max(0, item.occurrenceCount - 1), 0) }}</strong></div>
          </section>
          <section class="surface">
            <ElEmpty v-if="!incidents.loading && incidents.items.length === 0" description="没有事故记录" />
            <ElTable v-else :data="incidents.items" class="data-table" @row-click="openIncident">
              <ElTableColumn label="事故" min-width="260"><template #default="scope"><strong>{{ scope.row.kind }}</strong><small class="table-sub mono">{{ scope.row.id }}</small></template></ElTableColumn>
              <ElTableColumn label="服务" min-width="150"><template #default="scope"><span>{{ scope.row.serviceId ?? '--' }}</span><small class="table-sub mono">{{ scope.row.context.triggerCode }}</small></template></ElTableColumn>
              <ElTableColumn label="级别" width="110"><template #default="scope"><ElTag :type="severityTag(scope.row.severity)" effect="plain">{{ scope.row.severity }}</ElTag></template></ElTableColumn>
              <ElTableColumn label="状态" width="110"><template #default="scope"><ElTag :type="scope.row.state === 'open' ? 'warning' : 'success'" effect="plain">{{ scope.row.state === 'open' ? '处理中' : '已解决' }}</ElTag></template></ElTableColumn>
              <ElTableColumn prop="occurrenceCount" label="次数" width="90" />
              <ElTableColumn label="最近发生" width="180"><template #default="scope">{{ formatTime(scope.row.lastSeenAt) }}</template></ElTableColumn>
            </ElTable>
          </section>
        </template>

        <template v-else>
          <section class="page-header"><div><h1>工作区</h1><p>注册路径与最后一次清单校验结果</p></div><ElButton type="primary" :icon="FolderAdd" @click="addDialogOpen = true">注册工作区</ElButton></section>
          <section class="surface">
            <ElEmpty v-if="!catalog.loading && catalog.workspaces.length === 0" description="没有已注册的工作区" />
            <ElTable v-else :data="catalog.workspaces" class="data-table workspace-table" @row-click="openWorkspaceDetail">
              <ElTableColumn label="工作区" min-width="310"><template #default="scope"><div class="workspace-cell"><strong>{{ scope.row.systemName }}</strong><span class="path-text" :title="scope.row.path">{{ scope.row.path }}</span><small class="mono">{{ scope.row.id }}</small></div></template></ElTableColumn>
              <ElTableColumn label="清单状态" width="140"><template #default="scope"><ElTag :type="scope.row.manifestStatus === 'valid' ? 'success' : 'danger'">{{ scope.row.manifestStatus === 'valid' ? '有效' : '无效' }}</ElTag><small v-if="scope.row.lastErrorCode" class="error-code">{{ scope.row.lastErrorCode }}</small></template></ElTableColumn>
              <ElTableColumn label="Digest" min-width="190"><template #default="scope"><code :title="scope.row.manifestDigest">{{ shortDigest(scope.row.manifestDigest) }}</code></template></ElTableColumn>
              <ElTableColumn prop="serviceCount" label="服务" width="90" />
              <ElTableColumn label="更新时间" width="170"><template #default="scope">{{ formatTime(scope.row.updatedAt) }}</template></ElTableColumn>
              <ElTableColumn label="操作" width="168" align="right"><template #default="scope"><div class="table-actions"><ElTooltip content="查看详情" placement="left"><ElButton :icon="Document" circle plain aria-label="查看详情" @click.stop="openWorkspaceDetail(scope.row)" /></ElTooltip><ElTooltip content="刷新清单" placement="left"><ElButton :icon="Refresh" circle plain aria-label="刷新清单" :loading="catalog.mutating" @click.stop="refreshManifest(scope.row.id)" /></ElTooltip><ElTooltip :content="workspaceRemovalBlockReason(catalog.workspaces[scope.$index]) ?? '解除注册'" placement="left"><span><ElButton :icon="Delete" circle plain type="danger" aria-label="解除注册" :disabled="workspaceRemovalBlockReason(catalog.workspaces[scope.$index]) !== null" @click.stop="confirmRemoval(catalog.workspaces[scope.$index])" /></span></ElTooltip></div></template></ElTableColumn>
            </ElTable>
          </section>
        </template>
      </main>
    </div>

    <ServiceLogViewer
      v-model="serviceDrawerOpen"
      :service="selectedService"
      :system-id="runtime.status?.systemId ?? null"
      :system-name="catalog.selected?.system.name ?? ''"
      :instance-id="runtime.status?.instanceId ?? null"
      :manifest-invalid="currentManifestInvalid"
      :operation-active="runtime.activeOperation !== null"
      :restarting="runtime.mutating"
      @restart="restartSelectedService"
      @closed="closeServiceDrawer"
    />

    <ElDrawer v-model="operationDrawerOpen" size="min(620px, 100vw)" title="操作详情">
      <template v-if="runtime.selectedOperation !== null">
        <div class="drawer-actions"><ElTag :type="runtimeTag(runtime.selectedOperation.state)">{{ stateLabel(runtime.selectedOperation.state) }}</ElTag><code>{{ runtime.selectedOperation.id }}</code><ElButton v-if="runtime.selectedOperation.cancellable && ['queued', 'running'].includes(runtime.selectedOperation.state)" type="danger" plain @click="runtime.cancelSelected">取消</ElButton></div>
        <ElAlert v-if="runtime.selectedOperation.errorCode" :title="runtime.selectedOperation.errorCode" type="error" :closable="false" show-icon />
        <ElTimeline class="operation-timeline">
          <ElTimelineItem v-for="step in runtime.selectedOperation.steps" :key="step.number" :type="runtimeTag(step.state)" :timestamp="step.durationMs !== undefined ? `${step.durationMs} ms` : ''">
            <strong>{{ step.key }}</strong><span>{{ stateLabel(step.state) }}</span><code v-if="step.errorCode">{{ step.errorCode }}</code>
          </ElTimelineItem>
        </ElTimeline>
      </template>
    </ElDrawer>

    <ElDrawer v-model="incidentDrawerOpen" size="min(760px, 100vw)" title="事故详情">
      <template v-if="incidents.selected !== null">
        <div class="drawer-actions">
          <ElTag :type="severityTag(incidents.selected.incident.severity)">{{ incidents.selected.incident.severity }}</ElTag>
          <ElTag :type="incidents.selected.incident.state === 'open' ? 'warning' : 'success'" effect="plain">{{ incidents.selected.incident.state === 'open' ? '处理中' : '已解决' }}</ElTag>
          <code>{{ incidents.selected.incident.id }}</code>
          <ElButton :icon="RefreshRight" :loading="incidents.analyzing" @click="incidents.analyzeSelected">健康复查</ElButton>
        </div>
        <dl class="service-facts">
          <div><dt>服务</dt><dd>{{ incidents.selected.incident.serviceId ?? '--' }}</dd></div>
          <div><dt>触发码</dt><dd class="mono">{{ incidents.selected.incident.context.triggerCode }}</dd></div>
          <div><dt>发生次数</dt><dd>{{ incidents.selected.incident.occurrenceCount }}</dd></div>
          <div><dt>最近发生</dt><dd>{{ formatTime(incidents.selected.incident.lastSeenAt) }}</dd></div>
        </dl>
        <section class="incident-section">
          <div class="section-heading"><h2>诊断</h2><span>{{ incidentResults.length }} 条</span></div>
          <ElEmpty v-if="incidentResults.length === 0" description="没有匹配的规则" />
          <article v-for="result in incidentResults" :key="result.ruleId" class="diagnosis-row">
            <div><strong>{{ result.title }}</strong><ElTag type="info" effect="plain">{{ result.confidence }}%</ElTag></div>
            <p>{{ result.cause }}</p>
            <ul><li v-for="suggestion in result.suggestions" :key="suggestion.action">{{ suggestion.description }}</li></ul>
          </article>
        </section>
        <section class="incident-section">
          <div class="section-heading"><h2>证据</h2><span>{{ incidents.selected.incident.context.evidence.length }} 项</span></div>
          <ElTable :data="incidents.selected.incident.context.evidence" class="data-table">
            <ElTableColumn prop="type" label="类型" width="100" />
            <ElTableColumn label="引用"><template #default="scope"><code>{{ scope.row.eventId ?? scope.row.healthResultId ?? scope.row.logSequence ?? '--' }}</code></template></ElTableColumn>
            <ElTableColumn prop="serviceInstanceId" label="服务实例" min-width="260" />
          </ElTable>
        </section>
      </template>
    </ElDrawer>

    <WorkspaceImportDialog v-model="addDialogOpen" @completed="workspaceImportCompleted" />
    <WorkspaceDetailDrawer v-model="workspaceDetailOpen" :workspace-id="workspaceDetailID" @request-remove="confirmRemoval" />
    <ElDialog v-model="removeDialogOpen" title="解除工作区注册" width="min(480px, calc(100vw - 32px))" :close-on-click-modal="!catalog.mutating">
      <p class="dialog-copy">将从 StackPilot 中解除 <strong>{{ pendingRemoval?.systemName }}</strong> 的注册。工作区文件不会被删除。</p>
      <ElAlert v-if="pendingRemovalBlockReason !== null" type="warning" :closable="false" show-icon :title="pendingRemovalBlockReason" />
      <template #footer><ElButton :disabled="catalog.mutating" @click="removeDialogOpen = false">取消</ElButton><ElButton type="danger" :loading="catalog.mutating" :disabled="pendingRemovalBlockReason !== null" @click="removeWorkspace">解除注册</ElButton></template>
    </ElDialog>
  </div>
</template>
