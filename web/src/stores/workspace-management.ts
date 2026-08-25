import { defineStore } from 'pinia'
import { ref } from 'vue'
import { APIError, applyWorkspaceDraft, createWorkspaceEditDraft, createWorkspaceRelinkDraft, getWorkspace, getWorkspaceImportOperation } from '../api/client'
import type { WorkspaceDetail, WorkspaceImportDraft, WorkspaceImportOperation } from '../api/types'

export const useWorkspaceManagementStore = defineStore('workspace-management', () => {
	const detail = ref<WorkspaceDetail | null>(null)
	const loading = ref(false)
	const error = ref<string | null>(null)
	const traceId = ref<string | null>(null)
	const editDraft = ref<WorkspaceImportDraft | null>(null)
	const editOperation = ref<WorkspaceImportOperation | null>(null)
	const relinkDraft = ref<WorkspaceImportDraft | null>(null)
	const relinkOperation = ref<WorkspaceImportOperation | null>(null)

	async function load(id: string): Promise<void> {
		loading.value = true; error.value = null
		try { detail.value = await getWorkspace(id) }
		catch (reason: unknown) {
			if (reason instanceof APIError) { error.value = `${reason.code}: ${reason.message}`; traceId.value = reason.traceId }
			else { error.value = reason instanceof Error ? reason.message : '请求失败。'; traceId.value = null }
		} finally { loading.value = false }
	}

	async function prepareRelink(path: string): Promise<void> {
		if (detail.value === null) return
		loading.value = true; error.value = null
		try { relinkDraft.value = await createWorkspaceRelinkDraft(detail.value.workspace.id, path) }
		catch (reason: unknown) { capture(reason) }
		finally { loading.value = false }
	}

	async function applyRelink(): Promise<boolean> {
		if (relinkDraft.value === null || detail.value === null) return false
		const workspaceId = detail.value.workspace.id
		loading.value = true; error.value = null
		try {
			const reference = await applyWorkspaceDraft(relinkDraft.value.id, 'relink')
			for (;;) {
				relinkOperation.value = await getWorkspaceImportOperation(reference.operationId)
				if (['succeeded', 'failed', 'cancelled'].includes(relinkOperation.value.state)) break
				await new Promise<void>((resolve) => window.setTimeout(resolve, 400))
			}
			if (relinkOperation.value.state !== 'succeeded') { error.value = relinkOperation.value.errorCode ?? '工作区重新关联失败。'; return false }
			await load(workspaceId); return true
		} catch (reason: unknown) { capture(reason); return false }
		finally { loading.value = false }
	}

	async function prepareEdit(input: { systemName: string; description: string; serviceDisplayNames: Record<string, string>; portPreferred: Record<string, number> }): Promise<void> {
		if (detail.value === null) return
		loading.value = true; error.value = null
		try { editDraft.value = await createWorkspaceEditDraft(detail.value.workspace.id, input) }
		catch (reason: unknown) { capture(reason) }
		finally { loading.value = false }
	}

	async function applyEdit(): Promise<boolean> {
		if (editDraft.value === null || detail.value === null) return false
		const workspaceId = detail.value.workspace.id
		loading.value = true; error.value = null
		try {
			const reference = await applyWorkspaceDraft(editDraft.value.id, 'edit')
			for (;;) {
				editOperation.value = await getWorkspaceImportOperation(reference.operationId)
				if (['succeeded', 'failed', 'cancelled'].includes(editOperation.value.state)) break
				await new Promise<void>((resolve) => window.setTimeout(resolve, 400))
			}
			if (editOperation.value.state !== 'succeeded') { error.value = editOperation.value.errorCode ?? '工作区编辑失败。'; return false }
			await load(workspaceId)
			return true
		} catch (reason: unknown) { capture(reason); return false }
		finally { loading.value = false }
	}

	function capture(reason: unknown): void {
		if (reason instanceof APIError) { error.value = `${reason.code}: ${reason.message}`; traceId.value = reason.traceId }
		else { error.value = reason instanceof Error ? reason.message : '请求失败。'; traceId.value = null }
	}

	function clearEdit(): void { editDraft.value = null; editOperation.value = null }
	function clearRelink(): void { relinkDraft.value = null; relinkOperation.value = null }

	function clear(): void { detail.value = null; error.value = null; traceId.value = null; clearEdit(); clearRelink() }
	return { detail, loading, error, traceId, editDraft, editOperation, relinkDraft, relinkOperation,
		load, prepareEdit, applyEdit, prepareRelink, applyRelink, clearEdit, clearRelink, clear }
})
