<script setup lang="ts">
import {
  ArrowDownBold,
  ArrowUpBold,
  CircleCloseFilled,
  CopyDocument,
  Delete,
  Download,
  FullScreen,
  ScaleToOriginal,
  Search,
  Tickets,
  VideoPause,
  VideoPlay,
  WarningFilled,
} from '@element-plus/icons-vue'
import {
  ElAlert,
  ElAutoResizer,
  ElButton,
  ElDrawer,
  ElEmpty,
  ElIcon,
  ElInput,
  ElMessage,
  ElTable,
  ElTableColumn,
  ElTableV2,
  ElTag,
  ElTooltip,
  TableV2FixedDir,
} from 'element-plus'
import type { Column, TableV2Instance } from 'element-plus'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { LogEntry, ServiceRuntimeStatus } from '../../api/types'
import { useRuntimeStore } from '../../stores/runtime'
import {
  displayLogLevel,
  errorExcerpt,
  errorSequences,
  filterLogEntries,
  formatLogExport,
  logExportFilename,
  messagesOnly,
} from './log-viewer-model'

interface Props {
  modelValue: boolean
  service: ServiceRuntimeStatus | null
  systemId: string | null
  systemName: string
  instanceId: string | null
  manifestInvalid: boolean
  operationActive: boolean
  restarting: boolean
}

interface RowsRendered {
  rowVisibleStart: number
  rowVisibleEnd: number
}

interface ResizeSize {
  width: number
  height: number
}

const props = defineProps<Props>()
const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  restart: []
  closed: []
}>()
const runtime = useRuntimeStore()
const query = ref('')
const wraps = ref(false)
const fullscreen = ref(false)
const table = ref<TableV2Instance | null>(null)
const tableWidth = ref(0)
const layoutRevision = ref(0)
const firstVisibleSequence = ref<number | null>(null)
const currentErrorSequence = ref<number | null>(null)
const highlightedSequence = ref<number | null>(null)
let highlightTimer: number | null = null

const filteredLogs = computed(() => filterLogEntries(runtime.logs, query.value))
const errors = computed(() => errorSequences(filteredLogs.value))
const currentErrorIndex = computed(() => currentErrorSequence.value === null
  ? -1
  : errors.value.indexOf(currentErrorSequence.value))
const currentErrorPosition = computed(() => currentErrorIndex.value < 0 ? 0 : currentErrorIndex.value + 1)
const previousErrorDisabled = computed(() => currentErrorIndex.value <= 0)
const nextErrorDisabled = computed(() => errors.value.length === 0 || currentErrorIndex.value === errors.value.length - 1)
const currentExcerpt = computed(() => currentErrorSequence.value === null
  ? []
  : errorExcerpt(filteredLogs.value, currentErrorSequence.value))
const emptyDescription = computed(() => {
  if (query.value.trim() !== '') return '没有匹配的日志'
  if (runtime.logViewCleared) return '当前视图已清空，正在等待新日志'
  return '尚无日志'
})
const columns = computed<Column<LogEntry>[]>(() => {
  const fixedWidth = 70 + 108 + 76 + 42
  const available = Math.max(220, tableWidth.value - fixedWidth)
  const messageWidth = wraps.value ? available : Math.max(available, longestLineLength.value * 7.4 + 28)
  return [
    { key: 'sequence', dataKey: 'sequence', title: 'SEQ', width: 70, flexShrink: 0, fixed: TableV2FixedDir.LEFT, class: 'log-meta-cell' },
    { key: 'timestamp', dataKey: 'timestamp', title: '时间', width: 108, flexShrink: 0, fixed: TableV2FixedDir.LEFT, class: 'log-meta-cell' },
    { key: 'level', dataKey: 'level', title: '级别', width: 76, flexShrink: 0, fixed: TableV2FixedDir.LEFT, class: 'log-meta-cell' },
    { key: 'message', dataKey: 'message', title: '正文', width: messageWidth, flexShrink: 0, class: 'log-message-cell' },
    { key: 'actions', dataKey: 'sequence', title: '', width: 42, flexShrink: 0, fixed: TableV2FixedDir.RIGHT, class: 'log-action-cell' },
  ]
})
const longestLineLength = computed(() => filteredLogs.value.reduce((longest, entry) => {
  const entryLongest = entry.message.split('\n').reduce((value, line) => Math.max(value, line.length), 0)
  return Math.max(longest, entryLongest)
}, 0))

watch(
  () => [props.modelValue, props.systemId, props.instanceId, props.service?.serviceInstanceId] as const,
  ([open, systemId, instanceId]) => {
    if (!open) {
      runtime.stopLogStream()
      return
    }
    if (systemId === null || instanceId === null || props.service === null) return
    void runtime.loadLogs(systemId, props.service, instanceId)
  },
  { immediate: true },
)
watch(errors, (sequences) => {
  if (currentErrorSequence.value !== null && !sequences.includes(currentErrorSequence.value)) {
    currentErrorSequence.value = null
  }
})
watch(query, () => {
  currentErrorSequence.value = null
  scrollToRow(0, 'start')
})

onMounted(() => window.addEventListener('keydown', handleKeydown, true))
onBeforeUnmount(() => {
  window.removeEventListener('keydown', handleKeydown, true)
  clearHighlightTimer()
  runtime.stopLogStream()
})

function updateOpen(value: boolean): void {
  emit('update:modelValue', value)
}

function handleClosed(): void {
  runtime.stopLogStream()
  query.value = ''
  wraps.value = false
  fullscreen.value = false
  currentErrorSequence.value = null
  highlightedSequence.value = null
  firstVisibleSequence.value = null
  emit('closed')
}

function handleKeydown(event: KeyboardEvent): void {
  if (!props.modelValue || !fullscreen.value || event.key !== 'Escape') return
  event.preventDefault()
  event.stopImmediatePropagation()
  toggleFullscreen()
}

function handleResize(size: ResizeSize): void {
  tableWidth.value = size.width
}

function handleRowsRendered(range: RowsRendered): void {
  firstVisibleSequence.value = filteredLogs.value[range.rowVisibleStart]?.sequence ?? null
}

function rowClass({ rowData }: { rowData: LogEntry }): string {
  const level = displayLogLevel(rowData.level)
  return `log-table-row log-row-${level}${rowData.sequence === highlightedSequence.value ? ' is-located' : ''}`
}

function toggleWrap(): void {
  wraps.value = !wraps.value
  refreshLayoutAtAnchor()
}

function toggleFullscreen(): void {
  fullscreen.value = !fullscreen.value
  refreshLayoutAtAnchor()
}

function refreshLayoutAtAnchor(): void {
  const anchor = currentErrorSequence.value ?? firstVisibleSequence.value
  layoutRevision.value += 1
  void nextTick(() => {
    if (anchor === null) return
    const index = filteredLogs.value.findIndex((entry) => entry.sequence === anchor)
    if (index >= 0) table.value?.scrollToRow(index, 'center')
  })
}

function navigateError(direction: 'previous' | 'next'): void {
  if (errors.value.length === 0) return
  const targetIndex = direction === 'next'
    ? Math.min(errors.value.length - 1, currentErrorIndex.value + 1)
    : Math.max(0, currentErrorIndex.value - 1)
  const sequence = errors.value[targetIndex]
  if (sequence === undefined) return
  currentErrorSequence.value = sequence
  highlightedSequence.value = sequence
  const rowIndex = filteredLogs.value.findIndex((entry) => entry.sequence === sequence)
  scrollToRow(rowIndex, 'center')
  clearHighlightTimer()
  highlightTimer = window.setTimeout(() => {
    highlightedSequence.value = null
    highlightTimer = null
  }, 1600)
}

function scrollToRow(index: number, strategy: 'start' | 'center'): void {
  if (index < 0) return
  void nextTick(() => table.value?.scrollToRow(index, strategy))
}

function clearHighlightTimer(): void {
  if (highlightTimer === null) return
  window.clearTimeout(highlightTimer)
  highlightTimer = null
}

async function copyMessage(entry: LogEntry): Promise<void> {
  await copyText(entry.message, '正文已复制')
}

async function copyCurrentError(): Promise<void> {
  if (currentExcerpt.value.length === 0) return
  await copyText(messagesOnly(currentExcerpt.value), '错误片段已复制')
}

async function copyText(content: string, successMessage: string): Promise<void> {
  try {
    if (navigator.clipboard === undefined) throw new Error('clipboard unavailable')
    await navigator.clipboard.writeText(content)
    ElMessage.success(successMessage)
  } catch {
    ElMessage.error('浏览器未允许复制，请使用正文原生选择复制')
  }
}

function exportCurrentLogs(): void {
  if (filteredLogs.value.length === 0) return
  const filename = logExportFilename(props.systemName, props.service?.serviceId ?? '', props.instanceId ?? '', new Date())
  downloadText(formatLogExport(filteredLogs.value), filename)
}

function exportCurrentError(): void {
  if (currentExcerpt.value.length === 0) return
  const filename = logExportFilename(
    props.systemName,
    props.service?.serviceId ?? '',
    props.instanceId ?? '',
    new Date(),
    '-error',
  )
  downloadText(messagesOnly(currentExcerpt.value), filename)
}

function downloadText(content: string, filename: string): void {
  let url: string | null = null
  try {
    url = URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' }))
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = filename
    document.body.append(anchor)
    anchor.click()
    anchor.remove()
  } catch {
    ElMessage.error('导出日志失败')
  } finally {
    if (url !== null) {
      const objectURL = url
      window.requestAnimationFrame(() => URL.revokeObjectURL(objectURL))
    }
  }
}

function formatLogTime(timestamp: string): string {
  return timestamp.slice(11, 23)
}

function levelIsWarning(level: string): boolean {
  return displayLogLevel(level) === 'warn'
}

function levelIsError(level: string): boolean {
  const displayed = displayLogLevel(level)
  return displayed === 'error' || displayed === 'fatal'
}

function streamStateLabel(state: string): string {
  if (state === 'connected') return '实时'
  if (state === 'connecting') return '连接中'
  if (state === 'retrying') return '重连中'
  if (state === 'error') return '已中断'
  return '未连接'
}

function stateLabel(state: string): string {
  const labels: Record<string, string> = {
    stopped: '已停止', starting: '启动中', ready: '已就绪', degraded: '异常', stopping: '停止中',
    failed: '失败', completed: '已完成', running: '运行中', created: '已创建', exited: '已退出',
  }
  return labels[state] ?? state
}

function runtimeTag(state: string): 'success' | 'warning' | 'danger' | 'info' {
  if (['ready', 'running', 'completed'].includes(state)) return 'success'
  if (['failed', 'degraded', 'exited'].includes(state)) return 'danger'
  if (['starting', 'stopping'].includes(state)) return 'warning'
  return 'info'
}

function shortDigest(value: string): string {
  return value.length <= 14 ? value : `${value.slice(0, 10)}...`
}

function formatTime(value: string): string {
  return new Date(value).toLocaleString()
}
</script>

<template>
  <ElDrawer
    class="service-log-drawer"
    :model-value="modelValue"
    :size="fullscreen ? '100%' : 'min(760px, 100vw)'"
    :close-on-press-escape="!fullscreen"
    @update:model-value="updateOpen"
    @closed="handleClosed"
  >
    <template #header>
      <div class="log-drawer-header">
        <strong>{{ service?.serviceId ?? '服务' }}</strong>
        <ElTooltip :content="fullscreen ? '退出全屏' : '全屏'" placement="bottom">
          <ElButton
            :icon="fullscreen ? ScaleToOriginal : FullScreen"
            circle
            :aria-label="fullscreen ? '退出全屏日志' : '全屏日志'"
            @click="toggleFullscreen"
          />
        </ElTooltip>
      </div>
    </template>

    <template v-if="service !== null">
      <div class="service-log-content" :class="{ 'is-fullscreen': fullscreen }">
        <div class="service-log-summary">
          <div class="drawer-actions">
            <ElTag :type="runtimeTag(service.state)">{{ stateLabel(service.state) }}</ElTag>
            <span class="mono">PID {{ service.pid ?? '--' }}</span>
            <ElButton
              :loading="restarting"
              :disabled="manifestInvalid || operationActive"
              @click="emit('restart')"
            >重启服务</ElButton>
          </div>
          <dl class="service-facts">
            <div><dt>实例</dt><dd class="mono">{{ service.serviceInstanceId }}</dd></div>
            <div><dt>驱动</dt><dd>{{ service.driver }} / {{ service.mode }}</dd></div>
            <div><dt>依赖</dt><dd>{{ service.dependsOn.join(', ') || '无' }}</dd></div>
            <div><dt>启动时间</dt><dd>{{ service.processStartedAt ? formatTime(service.processStartedAt) : '--' }}</dd></div>
            <div><dt>命令摘要</dt><dd class="mono">{{ service.commandDigest ? shortDigest(service.commandDigest) : '--' }}</dd></div>
          </dl>
          <ElTable v-if="service.containers.length" :data="service.containers" class="data-table container-status-table">
            <ElTableColumn prop="service" label="容器服务" min-width="180" />
            <ElTableColumn label="状态" width="120"><template #default="scope"><ElTag :type="runtimeTag(scope.row.state)" effect="plain">{{ stateLabel(scope.row.state) }}</ElTag></template></ElTableColumn>
            <ElTableColumn label="健康" width="120"><template #default="scope"><ElTag :type="scope.row.health === 'healthy' ? 'success' : 'warning'" effect="plain">{{ scope.row.health || '--' }}</ElTag></template></ElTableColumn>
            <ElTableColumn prop="exitCode" label="退出码" width="90" />
          </ElTable>
        </div>

        <div class="log-controls">
          <div class="log-toolbar-primary">
            <ElInput v-model="query" :prefix-icon="Search" clearable placeholder="筛选当前窗口" aria-label="筛选当前日志窗口" />
            <ElTag
              v-if="runtime.logConnectionState !== 'idle'"
              :type="runtime.logConnectionState === 'connected' ? 'success' : runtime.logConnectionState === 'error' ? 'danger' : 'warning'"
              effect="plain"
            >{{ streamStateLabel(runtime.logConnectionState) }}</ElTag>
            <ElTag v-if="runtime.logPaused" type="warning" effect="dark">已暂停 · {{ runtime.pausedLogCount }}</ElTag>
            <ElTooltip :content="runtime.logPaused ? '继续接收' : '暂停显示'" placement="bottom">
              <ElButton
                :icon="runtime.logPaused ? VideoPlay : VideoPause"
                circle
                :aria-label="runtime.logPaused ? '继续显示日志' : '暂停显示日志'"
                @click="runtime.setLogPaused(!runtime.logPaused)"
              />
            </ElTooltip>
          </div>
          <div class="log-toolbar-secondary">
            <ElTooltip :content="wraps ? '关闭自动换行' : '开启自动换行'" placement="bottom">
              <ElButton :icon="Tickets" circle :type="wraps ? 'primary' : ''" :aria-label="wraps ? '关闭日志自动换行' : '开启日志自动换行'" @click="toggleWrap" />
            </ElTooltip>
            <ElTooltip content="上一个错误" placement="bottom"><ElButton :icon="ArrowUpBold" circle aria-label="上一个错误" :disabled="previousErrorDisabled" @click="navigateError('previous')" /></ElTooltip>
            <span class="error-position" aria-live="polite">{{ currentErrorPosition }}/{{ errors.length }}</span>
            <ElTooltip content="下一个错误" placement="bottom"><ElButton :icon="ArrowDownBold" circle aria-label="下一个错误" :disabled="nextErrorDisabled" @click="navigateError('next')" /></ElTooltip>
            <ElTooltip content="复制当前错误片段" placement="bottom"><ElButton :icon="CopyDocument" circle aria-label="复制当前错误片段" :disabled="currentExcerpt.length === 0" @click="copyCurrentError" /></ElTooltip>
            <ElTooltip content="导出当前错误片段" placement="bottom"><ElButton :icon="Download" circle aria-label="导出当前错误片段" :disabled="currentExcerpt.length === 0" @click="exportCurrentError" /></ElTooltip>
            <ElTooltip content="清空当前视图" placement="bottom"><ElButton :icon="Delete" circle aria-label="清空当前日志视图" :disabled="runtime.logs.length === 0 && runtime.pausedLogCount === 0" @click="runtime.clearLogView" /></ElTooltip>
            <ElTooltip :content="filteredLogs.length === 0 ? '当前范围为空' : '导出当前日志'" placement="bottom"><ElButton :icon="Download" circle aria-label="导出当前日志" :disabled="filteredLogs.length === 0" @click="exportCurrentLogs" /></ElTooltip>
          </div>
          <span v-if="errors.length === 0" class="error-empty-status">当前窗口无错误日志</span>
        </div>

        <ElAlert v-if="runtime.logConnectionError !== null" class="log-connection-alert" :title="runtime.logConnectionError" :type="runtime.logConnectionState === 'error' ? 'error' : 'warning'" :closable="false" show-icon />
        <ElAlert v-if="runtime.logBufferOverflow" class="log-connection-alert" title="暂停缓存已满，继续后将重新加载最近日志" type="warning" :closable="false" show-icon />

        <div class="log-table-frame" :class="{ 'is-wrapped': wraps }" role="log" aria-live="off">
          <ElAutoResizer @resize="handleResize">
            <template #default="{ height, width }">
              <ElTableV2
                :key="layoutRevision"
                ref="table"
                :columns="columns"
                :data="filteredLogs"
                :width="width"
                :height="height"
                :estimated-row-height="28"
                :row-height="28"
                row-key="sequence"
                :cache="8"
                :row-class="rowClass"
                scrollbar-always-on
                @rows-rendered="handleRowsRendered"
              >
                <template #cell="{ column, rowData }">
                  <span v-if="column.key === 'sequence'" class="log-meta">{{ rowData.sequence }}</span>
                  <time v-else-if="column.key === 'timestamp'" class="log-meta">{{ formatLogTime(rowData.timestamp) }}</time>
                  <span v-else-if="column.key === 'level'" class="log-level" :class="`level-${displayLogLevel(rowData.level)}`">
                    <ElIcon v-if="levelIsWarning(rowData.level)"><WarningFilled /></ElIcon>
                    <ElIcon v-else-if="levelIsError(rowData.level)"><CircleCloseFilled /></ElIcon>
                    {{ displayLogLevel(rowData.level) }}
                  </span>
                  <code v-else-if="column.key === 'message'" class="log-message">{{ rowData.message }}</code>
                  <ElTooltip v-else content="复制正文" placement="left">
                    <ElButton :icon="CopyDocument" link aria-label="复制本行正文" @click="copyMessage(rowData)" />
                  </ElTooltip>
                </template>
                <template #empty><ElEmpty :description="emptyDescription" /></template>
              </ElTableV2>
            </template>
          </ElAutoResizer>
        </div>
      </div>
    </template>
  </ElDrawer>
</template>

<style scoped>
.log-drawer-header { display: flex; min-width: 0; flex: 1; align-items: center; justify-content: space-between; gap: 12px; padding-right: 8px; }
.log-drawer-header strong { overflow: hidden; font-size: 16px; text-overflow: ellipsis; white-space: nowrap; }
.service-log-content { display: flex; height: 100%; min-height: 0; flex-direction: column; }
.service-log-summary { flex: 0 0 auto; }
.drawer-actions { display: flex; min-height: 42px; align-items: center; gap: 10px; margin-bottom: 14px; }
.drawer-actions .el-button { margin-left: auto; }
.service-facts { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); margin: 0 0 14px; border: 1px solid #dce2dd; border-radius: 6px; }
.service-facts > div { min-width: 0; padding: 10px 12px; }
.service-facts > div:nth-child(even) { border-left: 1px solid #dce2dd; }
.service-facts > div:nth-child(n + 3) { border-top: 1px solid #dce2dd; }
.service-facts dt { color: #68736d; font-size: 11px; }
.service-facts dd { overflow: hidden; margin: 3px 0 0; text-overflow: ellipsis; white-space: nowrap; }
.container-status-table { margin-bottom: 14px; }
.log-controls { display: flex; flex: 0 0 auto; flex-wrap: wrap; align-items: center; gap: 8px 10px; margin-bottom: 10px; }
.log-toolbar-primary, .log-toolbar-secondary { display: flex; min-width: 0; align-items: center; gap: 8px; }
.log-toolbar-primary { min-width: min(100%, 320px); flex: 1; }
.log-toolbar-primary .el-input { min-width: 150px; flex: 1; }
.log-toolbar-secondary { flex-wrap: wrap; }
.error-position { min-width: 42px; color: #4f5c55; font: 12px/1.2 "Cascadia Mono", Consolas, monospace; text-align: center; }
.error-empty-status { color: #68736d; font-size: 12px; }
.log-connection-alert { flex: 0 0 auto; margin-bottom: 10px; }
.log-table-frame { min-height: 240px; flex: 1 1 320px; overflow: hidden; color: #dce7df; background: #171c19; border: 1px solid #0c100e; border-radius: 6px; font: 12px/1.55 "Cascadia Mono", Consolas, monospace; }
.log-meta { color: #829087; font-variant-numeric: tabular-nums; user-select: none; }
.log-level { display: inline-flex; align-items: center; gap: 4px; font-size: 10px; font-weight: 700; text-transform: uppercase; user-select: none; }
.log-message { display: block; width: 100%; overflow: visible; color: inherit; font: inherit; white-space: pre; user-select: text; }
.is-wrapped .log-message { overflow-wrap: anywhere; white-space: pre-wrap; word-break: break-word; }
.level-trace, .level-debug { color: #93a098; }
.level-info, .level-unknown { color: #dce7df; }
.level-warn { color: #ffd17a; }
.level-error { color: #ff9b95; }
.level-fatal { color: #fff; background: #a9302d; border-radius: 2px; padding: 1px 3px; }
.mono { font-family: "Cascadia Mono", Consolas, monospace; font-variant-numeric: tabular-nums; }

:deep(.el-table-v2) { color: #dce7df; background: #171c19; }
:deep(.el-table-v2__header-row) { color: #9eaaa3; background: #202722; user-select: none; }
:deep(.el-table-v2__header-cell), :deep(.el-table-v2__row-cell) { padding: 4px 7px; border-right: 1px solid rgb(255 255 255 / 4%); }
:deep(.el-table-v2__row) { border-bottom: 1px solid rgb(255 255 255 / 5%); background: #171c19; }
:deep(.el-table-v2__row:hover), :deep(.el-table-v2__row:focus-within) { background: #222a25; }
:deep(.log-row-warn) { box-shadow: inset 3px 0 #d59a2f; }
:deep(.log-row-error), :deep(.log-row-fatal) { box-shadow: inset 3px 0 #d9534f; }
:deep(.log-row-fatal) { background: #241918; }
:deep(.is-located) { outline: 2px solid #8ed1c4; outline-offset: -2px; background: #27352f; }
:deep(.log-meta-cell), :deep(.log-action-cell) { user-select: none; }
:deep(.log-action-cell .el-button) { width: 28px; height: 28px; color: #a9b7ae; opacity: .35; }
:deep(.el-table-v2__row:hover .log-action-cell .el-button), :deep(.log-action-cell .el-button:focus-visible) { opacity: 1; }
:deep(.el-table-v2__empty) { color: #a9b7ae; background: #171c19; }

@media (max-width: 620px) {
  .service-facts { grid-template-columns: 1fr; }
  .service-facts > div + div { border-top: 1px solid #dce2dd; border-left: 0; }
  .log-toolbar-primary { flex-basis: 100%; }
  .log-toolbar-secondary { width: 100%; }
  .log-table-frame { min-height: 220px; }
}
</style>
