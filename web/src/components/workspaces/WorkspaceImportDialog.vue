<script setup lang="ts">
import { Check, DocumentChecked, FolderOpened, Search } from '@element-plus/icons-vue'
import { ElAlert, ElButton, ElCheckbox, ElDialog, ElEmpty, ElIcon, ElInput, ElInputNumber, ElOption, ElRadioButton, ElRadioGroup, ElSelect, ElStep, ElSteps, ElTable, ElTableColumn, ElTag } from 'element-plus'
import { computed, watch } from 'vue'
import { useWorkspaceImportStore } from '../../stores/workspace-import'

const open = defineModel<boolean>({ required: true })
const emit = defineEmits<{ completed: [] }>()
const imports = useWorkspaceImportStore()
const interactiveFindingCodes = new Set([
	'WORKSPACE_IMPORT_BUILD_UNCONFIRMED',
	'WORKSPACE_IMPORT_READINESS_UNCONFIRMED',
])

const activeStep = computed(() => ({ path: 0, script: 1, review: 2, applying: 3, done: 4 })[imports.stage])
const title = computed(() => imports.stage === 'done' ? '工作区已注册' : '注册工作区')
const composeService = computed(() => imports.candidate?.services.find((item) => item.driver === 'compose')?.compose)
const visibleFindings = computed(() => imports.candidate?.findings.filter((finding) =>
	!composeService.value || !interactiveFindingCodes.has(finding.code),
) ?? [])
const confirmationsComplete = computed(() => {
	if (!composeService.value) return true
	const buildReady = composeService.value.buildPolicy !== 'always' || imports.composeBuild
	const runningReady = Object.entries(composeService.value.readiness).every(([name, value]) => value !== 'running' || imports.composeRunning[name])
	return buildReady && runningReady
})

watch(open, (value) => { if (!value) imports.reset() })

async function inspect(): Promise<void> {
	const result = await imports.inspect()
	if (result === 'registered') emit('completed')
}

async function apply(): Promise<void> {
	await imports.apply()
	if (imports.stage === 'done') emit('completed')
}

function finish(): void { open.value = false }

function confidenceLabel(value: string): string {
	return { confirmed: '已确认', inferred: '待确认', unresolved: '未解析' }[value] ?? value
}
</script>

<template>
  <ElDialog v-model="open" :title="title" width="min(900px, calc(100vw - 32px))" :close-on-click-modal="!imports.busy" class="workspace-import-dialog">
    <ElSteps v-if="imports.stage !== 'done'" :active="activeStep" finish-status="success" align-center class="import-steps">
      <ElStep title="路径" />
      <ElStep title="启动文件" />
      <ElStep title="确认" />
      <ElStep title="应用" />
    </ElSteps>

    <ElAlert v-if="imports.error" type="error" :closable="false" show-icon class="import-error">
      <template #title>{{ imports.error }}</template>
      <p v-if="imports.traceId" class="trace-id">{{ imports.traceId }}</p>
    </ElAlert>

    <section v-if="imports.stage === 'path'" class="import-pane">
      <label class="field-label" for="import-workspace-path">工作区根目录</label>
      <ElInput id="import-workspace-path" v-model="imports.path" placeholder="E:\Projects\BTC" :disabled="imports.busy" @keyup.enter="inspect">
        <template #prefix><ElIcon><FolderOpened /></ElIcon></template>
      </ElInput>
    </section>

    <section v-else-if="imports.stage === 'script'" class="import-pane">
      <label class="field-label" for="import-script">启动文件</label>
      <ElSelect id="import-script" v-model="imports.script" filterable allow-create default-first-option :disabled="imports.busy" class="full-width">
        <ElOption v-for="item in imports.probe?.candidates ?? []" :key="item.path" :label="item.path" :value="item.path" />
      </ElSelect>
      <ElEmpty v-if="(imports.probe?.candidates.length ?? 0) === 0" description="未发现 BAT 启动文件" :image-size="72" />
    </section>

    <section v-else-if="imports.stage === 'review' && imports.draft" class="import-pane review-pane">
      <div class="draft-heading">
        <div><h3>{{ imports.draft.draft.systemName }}</h3><code>{{ imports.draft.draft.systemId }}</code></div>
        <span>{{ imports.draft.draft.sourceScript }}</span>
      </div>
      <ElRadioGroup v-model="imports.candidateId" class="candidate-switch" @change="imports.loadCorrections">
        <ElRadioButton v-for="item in imports.draft.draft.candidates" :key="item.id" :value="item.id">{{ item.name }}</ElRadioButton>
      </ElRadioGroup>

      <template v-if="imports.candidate">
        <div v-if="composeService" class="review-section compose-confirmations">
          <div class="section-label"><h4>导入前安全确认</h4><span>{{ composeService.services.length }} 个容器服务</span></div>
          <p><code>{{ composeService.file }}</code></p>
          <p v-if="composeService.buildServices.length">构建服务：{{ composeService.buildServices.join('、') }}</p>
          <ElAlert v-if="composeService.buildPolicy === 'always'" type="warning" :closable="false" show-icon
            title="构建可能拉取镜像、访问网络，并在 Docker daemon 中留下镜像与缓存。停止系统不会删除这些资源。" />
          <ElCheckbox v-if="composeService.buildPolicy === 'always'" v-model="imports.composeBuild">
            确认执行已登记工作区内的本地 Dockerfile
          </ElCheckbox>
          <div v-for="(requirement, service) in composeService.readiness" :key="service" class="readiness-confirmation">
            <ElCheckbox v-if="requirement === 'running'" v-model="imports.composeRunning[service]">
              确认 {{ service }} 无 healthcheck，使用 running 就绪语义
            </ElCheckbox>
            <span v-else>{{ service }}：healthy</span>
          </div>
        </div>

        <ElAlert v-for="finding in visibleFindings" :key="`${finding.code}-${finding.evidence[0]?.line ?? 0}`"
          :type="finding.severity === 'blocking' ? 'error' : finding.severity === 'warning' ? 'warning' : 'info'" :closable="false" show-icon>
          <template #title>{{ finding.code }}</template>
          <p>{{ finding.message }}</p>
          <small v-if="finding.evidence.length">{{ finding.evidence[0].path }}<template v-if="finding.evidence[0].line">:{{ finding.evidence[0].line }}</template></small>
        </ElAlert>

        <div class="correction-grid">
          <label><span>系统名称</span><ElInput v-model="imports.correctionName" maxlength="128" /></label>
          <label><span>描述</span><ElInput v-model="imports.correctionDescription" maxlength="1024" /></label>
        </div>

        <div class="review-section">
          <div class="section-label"><h4>服务</h4><span>{{ imports.candidate.services.length }}</span></div>
          <ElTable :data="imports.candidate.services" size="small" class="service-review-table">
            <ElTableColumn label="服务" min-width="180"><template #default="scope"><ElInput v-model="imports.correctionServiceNames[scope.row.id]" maxlength="128" /></template></ElTableColumn>
            <ElTableColumn label="驱动" width="110"><template #default="scope">{{ scope.row.driver === 'compose' ? 'Compose' : scope.row.runner }}</template></ElTableColumn>
            <ElTableColumn label="来源" min-width="160" class-name="import-mobile-hide"><template #default="scope">{{ scope.row.compose?.file ?? scope.row.workingDirectory }}</template></ElTableColumn>
            <ElTableColumn prop="readinessType" label="就绪" width="90" />
            <ElTableColumn label="证据" width="90" class-name="import-mobile-hide"><template #default="scope"><ElTag effect="plain" size="small">{{ confidenceLabel(scope.row.confidence) }}</ElTag></template></ElTableColumn>
          </ElTable>
        </div>

        <div class="review-section">
          <div class="section-label"><h4>端口</h4><span>{{ imports.candidate.ports.length }}</span></div>
          <ElTable :data="imports.candidate.ports" size="small" class="port-review-table">
            <ElTableColumn prop="name" label="名称" width="100" />
            <ElTableColumn label="首选端口" min-width="160"><template #default="scope"><ElInputNumber v-model="imports.correctionPorts[scope.row.name]" :min="1024" :max="65535" controls-position="right" /></template></ElTableColumn>
            <ElTableColumn prop="exposure" label="监听范围" class-name="import-mobile-hide" />
            <ElTableColumn label="证据" class-name="import-mobile-hide"><template #default="scope"><ElTag effect="plain" size="small">{{ confidenceLabel(scope.row.confidence) }}</ElTag></template></ElTableColumn>
          </ElTable>
        </div>

        <div class="review-section yaml-section">
          <div class="section-label"><h4>清单预览</h4><code>{{ imports.candidate.manifestDigest.slice(0, 12) }}</code></div>
          <pre>{{ imports.candidate.manifestYaml }}</pre>
        </div>
      </template>
    </section>

    <section v-else-if="imports.stage === 'applying'" class="import-pane operation-pane">
      <ElIcon class="operation-icon"><DocumentChecked /></ElIcon>
      <ElTable :data="imports.operation?.steps ?? []" size="small">
        <ElTableColumn prop="key" label="步骤" min-width="220" />
        <ElTableColumn prop="state" label="状态" width="120" />
        <ElTableColumn prop="errorCode" label="错误" min-width="180" />
      </ElTable>
    </section>

    <section v-else-if="imports.stage === 'done'" class="import-pane done-pane">
      <ElIcon><Check /></ElIcon>
      <h3>注册完成</h3>
    </section>

    <template #footer>
      <ElButton v-if="imports.stage === 'path'" :disabled="imports.busy" @click="open = false">取消</ElButton>
      <ElButton v-else-if="!['applying', 'done'].includes(imports.stage)" :disabled="imports.busy" @click="imports.back">上一步</ElButton>
      <ElButton v-if="imports.stage === 'path'" type="primary" :icon="Search" :loading="imports.busy" :disabled="imports.path.trim() === ''" @click="inspect">探测</ElButton>
      <ElButton v-else-if="imports.stage === 'script'" type="primary" :loading="imports.busy" :disabled="imports.script === ''" @click="imports.analyze">分析</ElButton>
      <ElButton v-if="imports.stage === 'review'" :type="imports.candidate?.applyable === true ? 'default' : 'primary'" :loading="imports.busy" :disabled="imports.correctionName.trim() === '' || !confirmationsComplete" @click="imports.correct">确认并更新预览</ElButton>
      <ElButton v-if="imports.stage === 'review'" type="primary" :loading="imports.busy" :disabled="imports.candidate?.applyable !== true" @click="apply">生成并注册</ElButton>
      <ElButton v-else-if="imports.stage === 'done'" type="primary" @click="finish">完成</ElButton>
    </template>
  </ElDialog>
</template>

<style scoped>
:global(.workspace-import-dialog) { display: flex; flex-direction: column; max-height: calc(100dvh - 32px); margin: 16px auto; overflow: hidden; }
:global(.workspace-import-dialog .el-dialog__body) { flex: 1 1 auto; min-height: 0; overflow-y: auto; }
.import-steps { margin: 4px 0 24px; }
.import-error { margin-bottom: 16px; }
.trace-id { margin: 4px 0 0; font-family: ui-monospace, monospace; font-size: 12px; }
.import-pane { min-height: 132px; }
.full-width { width: 100%; }
.draft-heading, .section-label { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.draft-heading h3, .section-label h4, .done-pane h3 { margin: 0; }
.draft-heading span { max-width: 50%; overflow-wrap: anywhere; color: var(--el-text-color-secondary); }
.candidate-switch { margin: 18px 0; display: flex; flex-wrap: wrap; }
.review-pane :deep(.el-alert) { margin: 10px 0; }
.review-pane :deep(.el-alert__content) { min-width: 0; }
.review-pane :deep(.el-alert__title) { overflow-wrap: anywhere; }
.review-pane :deep(.el-alert__content p) { margin: 4px 0; }
.correction-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-top: 16px; }
.correction-grid label { display: grid; gap: 6px; }
.review-section { margin-top: 22px; }
.compose-confirmations { display: grid; gap: 8px; margin-top: 0; padding-bottom: 14px; border-bottom: 1px solid var(--el-border-color); }
.compose-confirmations p { margin: 0; color: var(--el-text-color-secondary); }
.compose-confirmations :deep(.el-checkbox) { height: auto; align-items: flex-start; white-space: normal; }
.compose-confirmations :deep(.el-checkbox__input) { margin-top: 3px; }
.compose-confirmations :deep(.el-checkbox__label) { min-width: 0; line-height: 1.5; overflow-wrap: anywhere; white-space: normal; }
.readiness-confirmation { min-height: 28px; display: flex; align-items: center; }
.port-review-table :deep(.el-input-number) { width: 100%; min-width: 0; }
.section-label { margin-bottom: 8px; }
.yaml-section pre { max-height: 280px; margin: 0; padding: 14px; overflow: auto; border: 1px solid var(--el-border-color); background: var(--el-fill-color-light); font: 12px/1.55 ui-monospace, monospace; white-space: pre; }
.operation-pane { display: grid; gap: 18px; }
.operation-icon { font-size: 32px; color: var(--el-color-primary); }
.done-pane { display: grid; justify-items: center; align-content: center; gap: 12px; min-height: 180px; }
.done-pane .el-icon { font-size: 48px; color: var(--el-color-success); }
@media (max-width: 640px) {
  :global(.workspace-import-dialog) { overflow-x: hidden; }
  :global(.workspace-import-dialog .el-dialog__body), .review-pane, .import-steps { min-width: 0; overflow-x: hidden; }
  :global(.workspace-import-dialog .el-dialog__footer) { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }
  :global(.workspace-import-dialog .el-dialog__footer .el-button + .el-button) { margin-left: 0; }
  .draft-heading { align-items: flex-start; flex-direction: column; }
  .draft-heading span { max-width: 100%; }
  .compose-confirmations .section-label { align-items: flex-start; flex-direction: column; gap: 4px; }
  .candidate-switch { display: grid; }
  .correction-grid { grid-template-columns: 1fr; }
  :deep(.service-review-table th:nth-child(3)), :deep(.service-review-table td:nth-child(3)),
  :deep(.service-review-table th:nth-child(5)), :deep(.service-review-table td:nth-child(5)),
  :deep(.port-review-table th:nth-child(3)), :deep(.port-review-table td:nth-child(3)),
  :deep(.port-review-table th:nth-child(4)), :deep(.port-review-table td:nth-child(4)) { display: none; }
}
</style>
