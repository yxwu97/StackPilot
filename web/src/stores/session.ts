import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import {
  APIError,
  consumeBootstrapFragment,
  initializeAuthentication,
  refreshAuthentication,
  revokeAuthentication,
} from '../api/client.ts'
import type { SessionResponse } from '../api/client.ts'
import {
  AuthenticationUnavailableError,
  configureAuthenticationBridge,
  sessionInvalidCode,
} from '../api/auth-lifecycle.ts'

export type SessionState = 'idle' | 'loading' | 'ready' | 'expired' | 'bootstrap-invalid' | 'unreachable'

const renewalSafetyWindowMs = 5 * 60 * 1000
const retryRequestBudgetMs = 1000
const retryMaximumMs = 15_000
const channelName = 'stackpilot-browser-session'

interface SessionBroadcast {
  type: 'session-renewed' | 'session-reset'
  csrf: string
  expiresAt: string
}

export const useSessionStore = defineStore('session', () => {
  const state = ref<SessionState>('idle')
  const error = ref('')
  const csrf = ref('')
  const expiresAt = ref(0)
  const ready = computed(() => state.value === 'ready')
  let refreshPromise: Promise<void> | null = null
  let initializePromise: Promise<void> | null = null
  let renewalTimer: ReturnType<typeof setTimeout> | null = null
  let retryTimer: ReturnType<typeof setTimeout> | null = null
  let retryResolve: (() => void) | null = null
  let ceilingObserved = false
  let listenersInstalled = false
  let channel: BroadcastChannel | null = null
  let credentialVersion = 0
  let refreshController: AbortController | null = null

  function install(): void {
    configureAuthenticationBridge({ beforeMutation, invalidate })
    if (listenersInstalled) return
    document.addEventListener('visibilitychange', handleVisibility)
    window.addEventListener('focus', handleFocus)
    if (typeof BroadcastChannel !== 'undefined') {
      channel = new BroadcastChannel(channelName)
      channel.addEventListener('message', handleBroadcast)
    }
    listenersInstalled = true
  }

  async function initialize(): Promise<void> {
    if (initializePromise !== null) return initializePromise
    install()
    clearRuntimeState()
    state.value = 'loading'
    error.value = ''
    const bootstrap = consumeBootstrapFragment()
    initializePromise = initializeRequest(bootstrap).finally(() => {
      initializePromise = null
    })
    return initializePromise
  }

  async function initializeRequest(bootstrap: string | null): Promise<void> {
    try {
      const response = await withCrossTabLock(() => initializeAuthentication(bootstrap))
      applySession(response, true, bootstrap === null ? 'session-renewed' : 'session-reset')
    } catch (reason: unknown) {
      classifyInitializationFailure(reason, bootstrap !== null)
      throw reason
    }
  }

  function classifyInitializationFailure(reason: unknown, hadBootstrap: boolean): void {
    clearRuntimeState()
    if (reason instanceof APIError && reason.code === sessionInvalidCode) {
      state.value = 'expired'
      error.value = '浏览器会话已失效。请重新执行 stackpilot open。'
      return
    }
    if (hadBootstrap && reason instanceof APIError && reason.code === 'AUTH_BOOTSTRAP_INVALID') {
      state.value = 'bootstrap-invalid'
      error.value = '此启动链接已失效或已使用。请重新执行 stackpilot open。'
      return
    }
    state.value = 'unreachable'
    error.value = reason instanceof Error ? reason.message : '无法连接本地控制面。'
  }

  async function beforeMutation(): Promise<string> {
    if (state.value !== 'ready' || Date.now() >= expiresAt.value) {
      invalidate(sessionInvalidCode)
      throw new AuthenticationUnavailableError()
    }
    if (!ceilingObserved && expiresAt.value-Date.now() <= renewalSafetyWindowMs) {
      await refresh()
    }
    if (state.value !== 'ready' || csrf.value === '') throw new AuthenticationUnavailableError()
    return csrf.value
  }

  async function ensureFreshness(): Promise<void> {
    if (state.value !== 'ready') return
    if (Date.now() >= expiresAt.value) {
      invalidate(sessionInvalidCode)
      return
    }
    if (!ceilingObserved && expiresAt.value-Date.now() <= renewalSafetyWindowMs) await refresh()
  }

  async function refresh(): Promise<void> {
    if (refreshPromise !== null) return refreshPromise
    if (state.value !== 'ready') throw new AuthenticationUnavailableError()
    const observedVersion = credentialVersion
    const controller = new AbortController()
    refreshController = controller
    const operation = withCrossTabLock(async () => {
      await new Promise<void>((resolve) => setTimeout(resolve, 0))
      if (credentialVersion !== observedVersion) return
      await refreshWithRetry(controller.signal)
    }).finally(() => {
      if (refreshPromise === operation) refreshPromise = null
      if (refreshController === controller) refreshController = null
    })
    refreshPromise = operation
    return operation
  }

  async function refreshWithRetry(signal?: AbortSignal): Promise<void> {
    let attempt = 0
    for (;;) {
      if (state.value !== 'ready' || Date.now() >= expiresAt.value) {
        invalidate(sessionInvalidCode)
        throw new AuthenticationUnavailableError()
      }
      try {
        const previousExpiry = expiresAt.value
        const response = await refreshAuthentication(signal)
        applySession(response, false, 'session-renewed')
        ceilingObserved = Date.parse(response.expiresAt) <= previousExpiry
        scheduleRenewal(false)
        return
      } catch (reason: unknown) {
        if (state.value !== 'ready' || !(reason instanceof TypeError)) throw reason
        attempt++
        const delay = Math.min(retryMaximumMs, 1000 * 2 ** (attempt - 1))
        if (Date.now()+delay+retryRequestBudgetMs >= expiresAt.value) {
          scheduleExpiry()
          throw reason
        }
        await waitForRetry(delay)
      }
    }
  }

  function applySession(
    response: SessionResponse,
    allowImmediate: boolean,
    broadcast: SessionBroadcast['type'] | null,
  ): void {
    const parsedExpiry = Date.parse(response.expiresAt)
    if (response.csrf === '' || !Number.isFinite(parsedExpiry) || parsedExpiry <= Date.now()) {
      throw new Error('服务端返回了无效的浏览器会话期限。')
    }
    csrf.value = response.csrf
    expiresAt.value = parsedExpiry
    credentialVersion++
    state.value = 'ready'
    error.value = ''
    scheduleRenewal(allowImmediate)
    if (broadcast !== null) channel?.postMessage({ type: broadcast, ...response } satisfies SessionBroadcast)
  }

  function scheduleRenewal(allowImmediate: boolean): void {
    clearRenewalTimer()
    const remaining = expiresAt.value-Date.now()
    const delay = remaining-renewalSafetyWindowMs
    if (delay > 0) {
      renewalTimer = setTimeout(triggerScheduledRefresh, delay)
      return
    }
    if (allowImmediate && !ceilingObserved) {
      renewalTimer = setTimeout(triggerScheduledRefresh, 0)
      return
    }
    scheduleExpiry()
  }

  function scheduleExpiry(): void {
    clearRenewalTimer()
    renewalTimer = setTimeout(() => invalidate(sessionInvalidCode), Math.max(0, expiresAt.value-Date.now()))
  }

  function triggerScheduledRefresh(): void {
    renewalTimer = null
    void refresh().catch(() => {
      if (state.value === 'ready') scheduleExpiry()
    })
  }

  function invalidate(code: string): void {
    if (code !== sessionInvalidCode || state.value === 'expired') return
    clearRuntimeState()
    state.value = 'expired'
    error.value = '浏览器会话已失效。请重新执行 stackpilot open 并使用新页面。'
  }

  async function logout(): Promise<void> {
    if (state.value === 'ready') await revokeAuthentication()
    clearRuntimeState()
    state.value = 'expired'
    error.value = '浏览器会话已注销。请重新执行 stackpilot open。'
  }

  function handleVisibility(): void {
    if (document.visibilityState === 'visible') void ensureFreshness().catch(() => undefined)
  }

  function handleFocus(): void {
    void ensureFreshness().catch(() => undefined)
  }

  function handleBroadcast(event: MessageEvent<unknown>): void {
    if (state.value !== 'ready' || !isSessionBroadcast(event.data)) return
    const incomingExpiry = Date.parse(event.data.expiresAt)
    if (event.data.type !== 'session-reset' && incomingExpiry < expiresAt.value) return
    applySession(event.data, false, null)
  }

  function clearRuntimeState(): void {
    refreshController?.abort()
    refreshController = null
    clearRenewalTimer()
    if (retryTimer !== null) clearTimeout(retryTimer)
    retryTimer = null
    retryResolve?.()
    retryResolve = null
    csrf.value = ''
    expiresAt.value = 0
    ceilingObserved = false
  }

  function clearRenewalTimer(): void {
    if (renewalTimer !== null) clearTimeout(renewalTimer)
    renewalTimer = null
  }

  async function waitForRetry(delay: number): Promise<void> {
    await new Promise<void>((resolve) => {
      retryResolve = resolve
      retryTimer = setTimeout(() => {
        retryTimer = null
        retryResolve = null
        resolve()
      }, delay)
    })
  }

  function dispose(): void {
    clearRuntimeState()
    if (listenersInstalled) {
      document.removeEventListener('visibilitychange', handleVisibility)
      window.removeEventListener('focus', handleFocus)
      channel?.close()
      channel = null
      listenersInstalled = false
    }
    configureAuthenticationBridge(null)
    state.value = 'idle'
  }

  return {
    state, error, expiresAt, ready,
    initialize, ensureFreshness, invalidate, logout, dispose,
  }
})

function isSessionBroadcast(value: unknown): value is SessionBroadcast {
  if (typeof value !== 'object' || value === null) return false
  const candidate = value as Partial<SessionBroadcast>
  return (candidate.type === 'session-renewed' || candidate.type === 'session-reset')
    && typeof candidate.csrf === 'string' && typeof candidate.expiresAt === 'string'
}

async function withCrossTabLock<T>(callback: () => Promise<T>): Promise<T> {
  if (typeof navigator === 'undefined' || navigator.locks === undefined) return callback()
  return navigator.locks.request(channelName, { mode: 'exclusive' }, callback)
}
