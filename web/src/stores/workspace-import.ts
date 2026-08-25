import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { APIError, analyzeWorkspace, applyWorkspaceDraft, correctWorkspaceDraft, getWorkspaceImportOperation, probeWorkspace, registerWorkspace } from '../api/client'
import type { WorkspaceImportCandidate, WorkspaceImportDraft, WorkspaceImportOperation, WorkspaceProbe } from '../api/types'

type ImportStage = 'path' | 'script' | 'review' | 'applying' | 'done'

export const useWorkspaceImportStore = defineStore('workspace-import', () => {
	const stage = ref<ImportStage>('path')
	const path = ref('')
	const probe = ref<WorkspaceProbe | null>(null)
	const script = ref('')
	const draft = ref<WorkspaceImportDraft | null>(null)
	const candidateId = ref('')
	const operation = ref<WorkspaceImportOperation | null>(null)
	const correctionName = ref('')
	const correctionDescription = ref('')
	const correctionServiceNames = ref<Record<string, string>>({})
	const correctionPorts = ref<Record<string, number>>({})
	const composeRunning = ref<Record<string, boolean>>({})
	const composeBuild = ref(false)
	const busy = ref(false)
	const error = ref<string | null>(null)
	const traceId = ref<string | null>(null)
	const candidate = computed<WorkspaceImportCandidate | null>(() => draft.value?.draft.candidates.find((item) => item.id === candidateId.value) ?? null)

	function captureError(reason: unknown): void {
		if (reason instanceof APIError) {
			error.value = `${reason.code}: ${reason.message}`
			traceId.value = reason.traceId
		} else {
			error.value = reason instanceof Error ? reason.message : '请求失败。'
			traceId.value = null
		}
	}

	async function inspect(): Promise<'registered' | 'initialize' | null> {
		if (path.value.trim() === '') return null
		busy.value = true
		error.value = null
		try {
			probe.value = await probeWorkspace(path.value.trim())
			if (probe.value.state === 'ready_to_register') {
				await registerWorkspace(path.value.trim())
				stage.value = 'done'
				return 'registered'
			}
			script.value = probe.value.candidates[0]?.path ?? ''
			stage.value = 'script'
			return 'initialize'
		} catch (reason: unknown) {
			captureError(reason)
			return null
		} finally {
			busy.value = false
		}
	}

	async function analyze(): Promise<void> {
		if (script.value === '') return
		busy.value = true
		error.value = null
		try {
			draft.value = await analyzeWorkspace(path.value.trim(), script.value)
			candidateId.value = draft.value.draft.candidates[0]?.id ?? ''
			loadCorrections()
			stage.value = 'review'
		} catch (reason: unknown) {
			captureError(reason)
		} finally {
			busy.value = false
		}
	}

	function loadCorrections(): void {
		correctionName.value = draft.value?.draft.systemName ?? ''
		correctionDescription.value = draft.value?.draft.description ?? ''
		correctionServiceNames.value = Object.fromEntries(candidate.value?.services.map((item) => [item.id, item.displayName]) ?? [])
		correctionPorts.value = Object.fromEntries(candidate.value?.ports.map((item) => [item.name, item.preferred]) ?? [])
		const compose = candidate.value?.services.find((item) => item.driver === 'compose')?.compose
		const composeConfirmed = candidate.value?.applyable === true
		composeRunning.value = Object.fromEntries(Object.entries(compose?.readiness ?? {}).filter(([, value]) => value === 'running').map(([name]) => [name, composeConfirmed]))
		composeBuild.value = compose?.buildPolicy === 'always' && composeConfirmed
	}

	async function correct(): Promise<void> {
		if (draft.value === null || candidate.value === null) return
		busy.value = true; error.value = null
		try {
			draft.value = await correctWorkspaceDraft(draft.value.id, {
				candidateId: candidate.value.id, systemName: correctionName.value, description: correctionDescription.value,
				serviceDisplayNames: correctionServiceNames.value, portPreferred: correctionPorts.value,
				composeRunning: composeRunning.value, composeBuild: composeBuild.value,
			})
			candidateId.value = draft.value.draft.candidates[0]?.id ?? ''
			loadCorrections()
		} catch (reason: unknown) { captureError(reason) }
		finally { busy.value = false }
	}

	async function apply(): Promise<void> {
		if (draft.value === null || candidate.value === null || !candidate.value.applyable) return
		busy.value = true
		error.value = null
		try {
			const reference = await applyWorkspaceDraft(draft.value.id, candidate.value.id)
			stage.value = 'applying'
			operation.value = await waitForImport(reference.operationId)
			if (operation.value.state === 'succeeded') stage.value = 'done'
			else error.value = operation.value.errorCode ?? '工作区导入失败。'
		} catch (reason: unknown) {
			captureError(reason)
		} finally {
			busy.value = false
		}
	}

	async function waitForImport(id: string): Promise<WorkspaceImportOperation> {
		for (;;) {
			const current = await getWorkspaceImportOperation(id)
			operation.value = current
			if (['succeeded', 'failed', 'cancelled'].includes(current.state)) return current
			await new Promise<void>((resolve) => window.setTimeout(resolve, 400))
		}
	}

	function back(): void {
		if (stage.value === 'review') stage.value = 'script'
		else if (stage.value === 'script') stage.value = 'path'
	}

	function reset(): void {
		stage.value = 'path'; path.value = ''; probe.value = null; script.value = ''; draft.value = null
		candidateId.value = ''; operation.value = null; correctionName.value = ''; correctionDescription.value = ''
		correctionServiceNames.value = {}; correctionPorts.value = {}; busy.value = false; error.value = null; traceId.value = null
		composeRunning.value = {}; composeBuild.value = false
	}

	return { stage, path, probe, script, draft, candidateId, candidate, operation, correctionName, correctionDescription,
		correctionServiceNames, correctionPorts, composeRunning, composeBuild, busy, error, traceId, inspect, analyze, loadCorrections, correct, apply, back, reset }
})
