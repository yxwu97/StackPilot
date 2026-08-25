<script setup lang="ts">
import { Delete, Edit, Link } from '@element-plus/icons-vue'
import { ElAlert, ElButton, ElDescriptions, ElDescriptionsItem, ElDialog, ElDrawer, ElEmpty, ElInput, ElInputNumber, ElSkeleton, ElTabPane, ElTable, ElTableColumn, ElTabs, ElTag, ElTooltip } from 'element-plus'
import { computed, ref, watch } from 'vue'
import type { Workspace } from '../../api/types'
import { useWorkspaceManagementStore } from '../../stores/workspace-management'

const open = defineModel<boolean>({ required: true })
const props = defineProps<{ workspaceId: string | null }>()
const emit = defineEmits<{ requestRemove: [workspace: Workspace] }>()
const management = useWorkspaceManagementStore()
const title = computed(() => management.detail?.workspace.systemName ?? '工作区详情')
const editOpen = ref(false)
const relinkOpen = ref(false)
const relinkPath = ref('')
const editName = ref('')
const editDescription = ref('')
const serviceNames = ref<Record<string, string>>({})
const portValues = ref<Record<string, number>>({})
const canEdit = computed(() => management.detail?.runtime.state === 'stopped' && management.detail.runtime.activeOperationId === null)
const editCandidate = computed(() => management.editDraft?.draft.candidates[0] ?? null)

watch([open, () => props.workspaceId], ([visible, id]) => {
	if (visible && id !== null) void management.load(id)
	if (!visible) management.clear()
}, { immediate: true })

function shortDigest(value: string | undefined): string { return value ? `${value.slice(0, 12)}…` : '--' }
function sourceLabel(value: string): string { return { 'existing-manifest': '已有清单', 'bat-import': 'BAT 导入', 'structured-edit': '结构化编辑', 'relinked-manifest': '重新关联清单' }[value] ?? value }

function openEdit(): void {
	const detail = management.detail
	if (detail === null) return
	management.clearEdit()
	editName.value = detail.workspace.systemName
	editDescription.value = detail.manifest.description ?? ''
	serviceNames.value = Object.fromEntries(detail.services.map((item) => [item.id, item.displayName]))
	portValues.value = Object.fromEntries(detail.ports.filter((item) => item.preferred !== undefined).map((item) => [item.name, item.preferred as number]))
	editOpen.value = true
}

async function previewEdit(): Promise<void> {
	await management.prepareEdit({ systemName: editName.value, description: editDescription.value, serviceDisplayNames: serviceNames.value, portPreferred: portValues.value })
}

async function applyEdit(): Promise<void> { if (await management.applyEdit()) editOpen.value = false }

function openRelink(): void {
	management.clearRelink(); relinkPath.value = ''; relinkOpen.value = true
}

async function previewRelink(): Promise<void> { await management.prepareRelink(relinkPath.value.trim()) }
async function applyRelink(): Promise<void> { if (await management.applyRelink()) relinkOpen.value = false }

function requestRemoval(): void {
	if (management.detail !== null && canEdit.value) emit('requestRemove', management.detail.workspace)
}
</script>

<template>
  <ElDrawer v-model="open" :title="title" size="min(900px, 100vw)" class="workspace-detail-drawer">
    <ElSkeleton v-if="management.loading" :rows="8" animated />
    <ElAlert v-else-if="management.error" type="error" :closable="false" show-icon :title="management.error">
      <p v-if="management.traceId" class="trace-id">{{ management.traceId }}</p>
    </ElAlert>
    <ElEmpty v-else-if="management.detail === null" description="工作区不可用" />
    <template v-else>
      <div class="detail-title">
        <div><strong>{{ management.detail.workspace.systemName }}</strong><span>{{ management.detail.workspace.path }}</span></div>
        <div class="detail-actions"><ElTag :type="management.detail.workspace.manifestStatus === 'valid' ? 'success' : 'danger'">{{ management.detail.workspace.manifestStatus === 'valid' ? '清单有效' : '清单无效' }}</ElTag><ElTooltip :content="canEdit ? '重新关联路径' : '系统运行中或存在活动 Operation'"><span><ElButton :icon="Link" circle plain aria-label="重新关联路径" :disabled="!canEdit" @click="openRelink" /></span></ElTooltip><ElTooltip :content="canEdit ? '编辑工作区' : '系统运行中或存在活动 Operation'"><span><ElButton :icon="Edit" circle plain aria-label="编辑工作区" :disabled="!canEdit" @click="openEdit" /></span></ElTooltip><ElTooltip :content="canEdit ? '解除注册' : '系统运行中或存在活动 Operation'"><span><ElButton :icon="Delete" circle plain type="danger" aria-label="解除注册" :disabled="!canEdit" @click="requestRemoval" /></span></ElTooltip></div>
      </div>
      <ElTabs>
        <ElTabPane label="概览">
          <ElDescriptions :column="2" border>
            <ElDescriptionsItem label="工作区 ID"><code>{{ management.detail.workspace.id }}</code></ElDescriptionsItem>
            <ElDescriptionsItem label="System ID"><code>{{ management.detail.workspace.systemId }}</code></ElDescriptionsItem>
            <ElDescriptionsItem label="运行状态">{{ management.detail.runtime.state }}</ElDescriptionsItem>
            <ElDescriptionsItem label="活动 Operation"><code>{{ management.detail.runtime.activeOperationId ?? '--' }}</code></ElDescriptionsItem>
            <ElDescriptionsItem label="来源">{{ sourceLabel(management.detail.source.type) }}</ElDescriptionsItem>
            <ElDescriptionsItem label="入口脚本">{{ management.detail.source.entryScript ?? '--' }}</ElDescriptionsItem>
            <ElDescriptionsItem label="Manifest digest"><code :title="management.detail.manifest.digest">{{ shortDigest(management.detail.manifest.digest) }}</code></ElDescriptionsItem>
            <ElDescriptionsItem label="API version">{{ management.detail.manifest.apiVersion }}</ElDescriptionsItem>
          </ElDescriptions>
        </ElTabPane>
        <ElTabPane label="服务">
          <ElTable :data="management.detail.services" size="small">
            <ElTableColumn prop="displayName" label="服务" min-width="140" />
            <ElTableColumn prop="runner" label="Runner" width="100" />
            <ElTableColumn prop="mode" label="模式" width="90" />
            <ElTableColumn prop="workingDirectory" label="工作目录" min-width="160" />
            <ElTableColumn prop="readiness" label="就绪" width="90" />
            <ElTableColumn label="依赖" min-width="140"><template #default="scope">{{ Object.keys(scope.row.dependsOn).join(', ') || '--' }}</template></ElTableColumn>
          </ElTable>
        </ElTabPane>
        <ElTabPane label="端口">
          <ElTable :data="management.detail.ports" size="small">
            <ElTableColumn prop="name" label="名称" />
            <ElTableColumn prop="preferred" label="首选" />
            <ElTableColumn prop="fallbackRange" label="备用范围" />
            <ElTableColumn prop="conflictPolicy" label="冲突策略" />
            <ElTableColumn prop="exposure" label="监听范围" />
          </ElTable>
        </ElTabPane>
        <ElTabPane label="清单">
          <pre class="manifest-preview">{{ management.detail.manifest.yaml }}</pre>
        </ElTabPane>
      </ElTabs>
    </template>
  </ElDrawer>

  <ElDialog v-model="editOpen" title="编辑工作区" width="min(760px, calc(100vw - 32px))" :close-on-click-modal="!management.loading" @closed="management.clearEdit">
    <ElAlert v-if="management.error" type="error" :closable="false" show-icon :title="management.error" />
    <template v-if="management.editDraft === null">
      <div class="edit-grid">
        <label><span>系统名称</span><ElInput v-model="editName" maxlength="128" /></label>
        <label><span>描述</span><ElInput v-model="editDescription" type="textarea" maxlength="1024" :rows="3" /></label>
      </div>
      <section class="edit-section">
        <h4>服务名称</h4>
        <label v-for="service in management.detail?.services ?? []" :key="service.id" class="edit-row"><code>{{ service.id }}</code><ElInput v-model="serviceNames[service.id]" maxlength="128" /></label>
      </section>
      <section class="edit-section">
        <h4>首选端口</h4>
        <label v-for="port in management.detail?.ports ?? []" :key="port.name" class="edit-row"><code>{{ port.name }}</code><ElInputNumber v-model="portValues[port.name]" :min="1024" :max="65535" controls-position="right" /></label>
      </section>
    </template>
    <template v-else>
      <div class="edit-summary"><span>{{ editCandidate?.services.length ?? 0 }} 个服务</span><span>{{ editCandidate?.ports.length ?? 0 }} 个端口</span><code>{{ editCandidate?.manifestDigest.slice(0, 12) }}</code></div>
      <pre class="manifest-preview edit-preview">{{ editCandidate?.manifestYaml }}</pre>
      <ElTable v-if="management.editOperation" :data="management.editOperation.steps" size="small" class="edit-operation"><ElTableColumn prop="key" label="步骤" /><ElTableColumn prop="state" label="状态" width="120" /><ElTableColumn prop="errorCode" label="错误" /></ElTable>
    </template>
    <template #footer>
      <ElButton :disabled="management.loading" @click="editOpen = false">取消</ElButton>
      <ElButton v-if="management.editDraft === null" type="primary" :loading="management.loading" :disabled="editName.trim() === ''" @click="previewEdit">预览变更</ElButton>
      <ElButton v-else type="primary" :loading="management.loading" @click="applyEdit">应用</ElButton>
    </template>
  </ElDialog>

  <ElDialog v-model="relinkOpen" title="重新关联工作区路径" width="min(680px, calc(100vw - 32px))" :close-on-click-modal="!management.loading" @closed="management.clearRelink">
    <ElAlert v-if="management.error" type="error" :closable="false" show-icon :title="management.error" />
    <template v-if="management.relinkDraft === null">
      <label class="relink-field"><span>新的工作区根目录</span><ElInput v-model="relinkPath" placeholder="E:\Projects\MovedWorkspace" /></label>
    </template>
    <template v-else>
      <ElDescriptions :column="1" border>
        <ElDescriptionsItem label="新路径">{{ management.relinkDraft.path }}</ElDescriptionsItem>
        <ElDescriptionsItem label="System ID"><code>{{ management.relinkDraft.draft.systemId }}</code></ElDescriptionsItem>
        <ElDescriptionsItem label="Manifest digest"><code>{{ shortDigest(management.relinkDraft.draft.candidates[0]?.manifestDigest) }}</code></ElDescriptionsItem>
      </ElDescriptions>
      <ElTable v-if="management.relinkOperation" :data="management.relinkOperation.steps" size="small" class="edit-operation"><ElTableColumn prop="key" label="步骤" /><ElTableColumn prop="state" label="状态" width="120" /><ElTableColumn prop="errorCode" label="错误" /></ElTable>
    </template>
    <template #footer>
      <ElButton :disabled="management.loading" @click="relinkOpen = false">取消</ElButton>
      <ElButton v-if="management.relinkDraft === null" type="primary" :loading="management.loading" :disabled="relinkPath.trim() === ''" @click="previewRelink">验证路径</ElButton>
      <ElButton v-else type="primary" :loading="management.loading" @click="applyRelink">应用重关联</ElButton>
    </template>
  </ElDialog>
</template>

<style scoped>
.trace-id { font: 12px ui-monospace, monospace; }
.detail-title { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; margin-bottom: 18px; }
.detail-title div { display: grid; gap: 4px; min-width: 0; }
.detail-title strong { font-size: 18px; }
.detail-title span { overflow-wrap: anywhere; color: var(--el-text-color-secondary); }
.detail-actions { display: flex !important; grid-auto-flow: column; align-items: center; gap: 8px !important; }
.manifest-preview { max-height: calc(100vh - 210px); margin: 0; padding: 14px; overflow: auto; border: 1px solid var(--el-border-color); background: var(--el-fill-color-light); font: 12px/1.55 ui-monospace, monospace; white-space: pre; }
.edit-grid { display: grid; gap: 14px; }
.edit-grid label { display: grid; gap: 6px; }
.edit-section { margin-top: 20px; }
.edit-section h4 { margin: 0 0 10px; }
.edit-row { display: grid; grid-template-columns: minmax(120px, 1fr) minmax(180px, 2fr); align-items: center; gap: 12px; margin: 8px 0; }
.edit-summary { display: flex; flex-wrap: wrap; gap: 16px; margin-bottom: 12px; }
.edit-preview { max-height: 340px; }
.edit-operation { margin-top: 14px; }
.relink-field { display: grid; gap: 6px; }
@media (max-width: 640px) { .detail-title { flex-direction: column; } }
</style>
