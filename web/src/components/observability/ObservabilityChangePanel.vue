<script setup lang="ts">
import { DataAnalysis, DocumentChecked, Refresh, VideoPlay, Warning } from '@element-plus/icons-vue'
import { ElAlert, ElButton, ElDialog, ElEmpty, ElIcon, ElRadioButton, ElRadioGroup, ElTable, ElTableColumn, ElTag, ElTooltip } from 'element-plus'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import type { ServiceDefinition, SystemRuntimeStatus } from '../../api/types'
import { formatBytes, latestAvailable, riskTag, sparklinePoints } from '../../observability/model'
import { useObservabilityChangeStore } from '../../stores/observability-change'

const props = defineProps<{
  workspaceId: string
  services: ServiceDefinition[]
  status: SystemRuntimeStatus | null
  activeOperation: boolean
  capabilities: string[]
  active: boolean
}>()
const emit = defineEmits<{ refresh: [] }>()
const store = useObservabilityChangeStore()
const restartDialogOpen = ref(false)

const metricRows = computed(() => props.services.map((service) => {
  const series = store.metrics?.series.find((candidate) => candidate.serviceId === service.id)
  const latest = latestAvailable(series?.points ?? [])
  return {
    serviceId: service.id,
    source: series?.source ?? null,
    points: series?.points ?? [],
    latest,
    cpuPoints: sparklinePoints(series?.points ?? [], 'cpuPercent'),
    memoryPoints: sparklinePoints(series?.points ?? [], 'memoryBytes'),
  }
}))

const totalCPU = computed(() => metricRows.value.reduce((sum, row) => sum + (row.latest?.cpuPercent ?? 0), 0))
const totalMemory = computed(() => metricRows.value.reduce((sum, row) => sum + (row.latest?.memoryBytes ?? 0), 0))
const availableSeries = computed(() => metricRows.value.filter((row) => row.latest !== null).length)
const coverageRows = computed(() => (props.status?.services ?? []).map((service) => ({
  serviceId: service.serviceId,
  required: props.services.find((candidate) => candidate.id === service.serviceId)?.required ?? false,
  state: service.state,
  coverage: props.status?.healthCoverage?.find((item) => item.serviceInstanceId === service.serviceInstanceId),
})))
const requiredCoverageReady = computed(() => props.services.filter((item) => item.required).every((definition) => {
  const row = coverageRows.value.find((candidate) => candidate.serviceId === definition.id)
  return row?.coverage?.satisfiesVerification === true
}))
const verifiedRestartBlockReason = computed(() => {
  if (!store.verifiedRestartEnabled) return '当前版本尚未发布验证式重启 capability'
  if (props.activeOperation || store.restarting) return '系统存在活动 Operation'
  if (props.status === null || !['running', 'degraded'].includes(props.status.state)) return '系统运行后才能执行验证式重启'
  if (store.plan === null) return '请先生成变更计划'
  if (store.plan.state !== 'ready' || store.plan.blockedCount > 0) return '变更计划包含阻断项'
  if (!requiredCoverageReady.value) return '必需服务的健康覆盖不完整'
  return null
})

watch(
  () => [props.workspaceId, props.services.map((item) => item.id).join(','), props.capabilities.join(','), props.active] as const,
  ([workspaceId, , , active]) => {
    store.configure(workspaceId, props.services.map((item) => item.id), props.capabilities)
    store.stopMetricPolling()
    if (!active) return
    void store.load()
    store.startMetricPolling()
  },
  { immediate: true },
)
watch(() => store.metricHours, () => {
  if (props.active) void store.loadMetrics()
})
onBeforeUnmount(() => store.stopMetricPolling())

async function generatePlan(): Promise<void> {
  await store.generatePlan()
  emit('refresh')
}

async function confirmVerifiedRestart(): Promise<void> {
  restartDialogOpen.value = false
  await store.verifiedRestart()
  emit('refresh')
}

function shortDigest(value: string): string {
  return `${value.slice(0, 10)}...${value.slice(-6)}`
}

function formatTime(value: string | undefined): string {
  if (value === undefined) return '--'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  }).format(new Date(value))
}

function coverageLabel(value: string | undefined): string {
  const labels: Record<string, string> = {
    business: '业务级', container: '容器级', 'process-only': '仅进程', unavailable: '不可用',
  }
  return value === undefined ? '无数据' : labels[value] ?? value
}

function changeLabel(value: string): string {
  return ({ added: '新增', removed: '移除', changed: '变更' } as Record<string, string>)[value] ?? value
}
</script>

<template>
  <div class="observability-change">
    <ElAlert
      v-if="store.error !== null"
      class="persistent-error"
      :title="store.errorCode === null ? '加载监测与变更数据失败' : store.errorCode"
      :description="store.traceId === null ? store.error : `${store.error} · traceId ${store.traceId}`"
      type="error"
      :closable="false"
      show-icon
    />

    <section class="surface observability-section">
      <header class="section-heading">
        <div><h2>资源监测</h2><p>可信运行身份的有界采样，不包含命令与环境变量</p></div>
        <div class="monitoring-actions">
          <ElRadioGroup v-model="store.metricHours" size="small" aria-label="指标时间范围">
            <ElRadioButton :value="1">1 小时</ElRadioButton>
            <ElRadioButton :value="24">24 小时</ElRadioButton>
          </ElRadioGroup>
          <ElTooltip content="刷新指标" placement="top">
            <ElButton :icon="Refresh" circle :loading="store.loadingMetrics" :disabled="!store.metricsEnabled" aria-label="刷新指标" @click="store.loadMetrics" />
          </ElTooltip>
        </div>
      </header>
      <ElAlert v-if="!store.metricsEnabled" title="当前控制面未发布资源监测 capability" type="info" :closable="false" show-icon />
      <template v-else>
        <div class="monitoring-summary">
          <div><span>当前 CPU</span><strong>{{ availableSeries === 0 ? '--' : `${totalCPU.toFixed(1)}%` }}</strong></div>
          <div><span>当前内存</span><strong>{{ availableSeries === 0 ? '--' : formatBytes(totalMemory) }}</strong></div>
          <div><span>有数据服务</span><strong>{{ availableSeries }} / {{ metricRows.length }}</strong></div>
          <div><span>采样窗口</span><strong>{{ store.metricHours }}h</strong></div>
        </div>
        <ElEmpty v-if="!store.loadingMetrics && availableSeries === 0" description="当前窗口没有可用指标；unsupported 与 unavailable 不会被填充为 0" />
        <div v-else class="metric-service-grid">
          <article v-for="row in metricRows" :key="row.serviceId" class="metric-service">
            <div class="metric-service-title">
              <div><strong>{{ row.serviceId }}</strong><span>{{ row.source ?? '暂无来源' }}</span></div>
              <ElTag :type="row.latest === null ? 'info' : 'success'" effect="plain">{{ row.latest === null ? '缺失' : '可用' }}</ElTag>
            </div>
            <div class="metric-values">
              <div><span>CPU</span><strong>{{ row.latest?.cpuPercent === undefined ? '--' : `${row.latest.cpuPercent.toFixed(1)}%` }}</strong></div>
              <svg class="metric-sparkline" viewBox="0 0 100 36" role="img" :aria-label="`${row.serviceId} CPU 趋势`"><polyline v-if="row.cpuPoints !== ''" :points="row.cpuPoints" /></svg>
              <div><span>内存</span><strong>{{ formatBytes(row.latest?.memoryBytes) }}</strong></div>
              <svg class="metric-sparkline memory" viewBox="0 0 100 36" role="img" :aria-label="`${row.serviceId} 内存趋势`"><polyline v-if="row.memoryPoints !== ''" :points="row.memoryPoints" /></svg>
            </div>
            <small v-if="row.latest === null && row.points.length > 0" class="metric-reason">{{ row.points[row.points.length - 1]?.reasonCode ?? row.points[row.points.length - 1]?.status }}</small>
          </article>
        </div>
      </template>
    </section>

    <section class="surface observability-section">
      <header class="section-heading"><div><h2>健康覆盖</h2><p>服务端计算的 Verified Restart 前置条件</p></div><ElTag :type="requiredCoverageReady ? 'success' : 'warning'" effect="plain">{{ requiredCoverageReady ? '覆盖完整' : '覆盖不完整' }}</ElTag></header>
      <ElTable :data="coverageRows" class="data-table" empty-text="系统尚未运行">
        <ElTableColumn prop="serviceId" label="服务" min-width="170" />
        <ElTableColumn label="范围" width="100"><template #default="scope">{{ scope.row.required ? '必需' : '可选' }}</template></ElTableColumn>
        <ElTableColumn prop="state" label="运行状态" width="120" />
        <ElTableColumn label="覆盖" width="120"><template #default="scope"><ElTag :type="scope.row.coverage?.satisfiesVerification ? 'success' : 'warning'" effect="plain">{{ coverageLabel(scope.row.coverage?.coverage) }}</ElTag></template></ElTableColumn>
        <ElTableColumn label="Readiness / Liveness" min-width="190"><template #default="scope"><code>{{ scope.row.coverage?.readinessKind ?? '--' }} / {{ scope.row.coverage?.livenessKind ?? '--' }}</code></template></ElTableColumn>
        <ElTableColumn label="最近检查" min-width="170"><template #default="scope">{{ formatTime(scope.row.coverage?.latestCheckedAt) }}</template></ElTableColumn>
      </ElTable>
    </section>

    <section class="surface observability-section">
      <header class="section-heading">
        <div><h2>变更计划</h2><p>运行 revision 与当前工作区 revision 的确定性只读比较</p></div>
        <div class="monitoring-actions">
          <ElButton :icon="DocumentChecked" :loading="store.loadingPlan" :disabled="!store.planningEnabled || activeOperation" @click="generatePlan">生成计划</ElButton>
          <ElTooltip :content="verifiedRestartBlockReason ?? '执行验证式重启'" placement="top">
            <span><ElButton type="primary" :icon="VideoPlay" :disabled="verifiedRestartBlockReason !== null" @click="restartDialogOpen = true">验证式重启</ElButton></span>
          </ElTooltip>
        </div>
      </header>
      <ElAlert v-if="!store.planningEnabled" title="当前控制面未发布变更计划 capability" type="info" :closable="false" show-icon />
      <ElEmpty v-else-if="store.plan === null" description="尚未生成变更计划" />
      <template v-else>
        <div class="plan-summary">
          <div><span>运行 revision</span><strong class="mono" :title="store.plan.fromRevision.digest">{{ shortDigest(store.plan.fromRevision.digest) }}</strong></div>
          <div><span>工作区 revision</span><strong class="mono" :title="store.plan.toRevision.digest">{{ shortDigest(store.plan.toRevision.digest) }}</strong></div>
          <div><span>风险</span><ElTag :type="riskTag(store.plan.risk)">{{ store.plan.risk }}</ElTag></div>
          <div><span>结论</span><ElTag :type="store.plan.state === 'ready' ? 'success' : 'danger'">{{ store.plan.state === 'ready' ? '可执行' : `阻断 ${store.plan.blockedCount}` }}</ElTag></div>
        </div>
        <ElAlert v-if="store.plan.state === 'blocked'" title="服务端规则已阻断验证式重启" description="请处理下列 blocked 项后重新生成计划。" type="error" :closable="false" show-icon />
        <ElTable :data="store.plan.items" class="data-table plan-table" empty-text="两个 revision 没有结构化差异">
          <ElTableColumn prop="kind" label="类型" width="130" />
          <ElTableColumn prop="key" label="范围" min-width="160" />
          <ElTableColumn label="变更" width="90"><template #default="scope">{{ changeLabel(scope.row.change) }}</template></ElTableColumn>
          <ElTableColumn label="风险" width="100"><template #default="scope"><ElTag :type="riskTag(scope.row.risk)" effect="plain">{{ scope.row.risk }}</ElTag></template></ElTableColumn>
          <ElTableColumn prop="summary" label="确定性结论" min-width="320" />
        </ElTable>
      </template>
    </section>

    <section v-if="store.restartOperation !== null" class="surface verification-result">
      <div><ElIcon><DataAnalysis /></ElIcon><div><strong>验证式重启 Operation</strong><code>{{ store.restartOperation.id }}</code></div></div>
      <ElTag :type="store.restartOperation.state === 'succeeded' ? 'success' : 'danger'">{{ store.restartOperation.state }}</ElTag>
    </section>

    <ElDialog v-model="restartDialogOpen" title="确认验证式重启" width="min(560px, calc(100vw - 32px))" :close-on-click-modal="!store.restarting">
      <div class="restart-confirmation">
        <ElAlert title="此操作不会自动恢复旧源码或业务数据" type="warning" :closable="false" show-icon><template #icon><ElIcon><Warning /></ElIcon></template></ElAlert>
        <dl>
          <div><dt>计划</dt><dd class="mono">{{ store.plan?.id }}</dd></div>
          <div><dt>影响</dt><dd>{{ services.length }} 个声明式服务，按逆拓扑停止后重新启动</dd></div>
          <div><dt>验证</dt><dd>readiness 通过后进入 liveness 稳定观察窗口</dd></div>
        </dl>
      </div>
      <template #footer><ElButton :disabled="store.restarting" @click="restartDialogOpen = false">取消</ElButton><ElButton type="primary" :loading="store.restarting" @click="confirmVerifiedRestart">确认重启并验证</ElButton></template>
    </ElDialog>
  </div>
</template>
