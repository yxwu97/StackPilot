import assert from 'node:assert/strict'
import test from 'node:test'
import { createPinia, setActivePinia } from 'pinia'
import { listWorkspaces, refreshWorkspace, unregisterWorkspace } from '../src/api/client.ts'
import { useSessionStore } from '../src/stores/session.ts'

const browserWindow = Object.assign(new EventTarget(), {
  location: { hash: '', pathname: '/', search: '', origin: 'http://127.0.0.1', host: '127.0.0.1' },
  history: { replaceState: (_data: unknown, _unused: string, url?: string | URL | null) => { replacedURL = String(url) } },
  setTimeout,
  clearTimeout,
})
const browserDocument = Object.assign(new EventTarget(), { visibilityState: 'visible' })
let replacedURL = ''

Object.defineProperty(globalThis, 'window', { configurable: true, value: browserWindow })
Object.defineProperty(globalThis, 'document', { configurable: true, value: browserDocument })
Object.defineProperty(globalThis, 'BroadcastChannel', { configurable: true, value: undefined })

test('consumes one bootstrap fragment without persisting browser credentials', async () => {
  installBrowserGlobals()
  setActivePinia(createPinia())
  const requests: Array<{ url: string; init: RequestInit }> = []
  browserWindow.location.hash = '#bootstrap=one-time-code'
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    requests.push({ url: String(input), init: init ?? {} })
    return sessionResponse('csrf-initial', 1, 30 * 60 * 1000)
  }) as typeof fetch

  const session = useSessionStore()
  await session.initialize()
  assert.equal(replacedURL, '/')
  assert.equal(requests.length, 1)
  assert.equal(requests[0]?.url, '/api/v1/auth/session')
  assert.equal(requests[0]?.init.method, 'POST')
  assert.deepEqual(JSON.parse(String(requests[0]?.init.body)), { bootstrap: 'one-time-code' })
  assert.equal(session.state, 'ready')
  session.dispose()
})

test('coalesces timer, focus, visibility, and mutation renewal into one request', async () => {
  installBrowserGlobals()
  setActivePinia(createPinia())
  browserWindow.location.hash = ''
  const refreshed = deferred<Response>()
  const mutationHeaders: Headers[] = []
  let sessionGets = 0
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input)
    if (url === '/api/v1/auth/session') {
      sessionGets++
      if (sessionGets === 1) return sessionResponse('csrf-initial', 1, 4 * 60 * 1000)
      return refreshed.promise
    }
    mutationHeaders.push(new Headers(init?.headers))
    return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch

  const session = useSessionStore()
  await session.initialize()
  await waitUntil(() => sessionGets === 2)
  browserWindow.dispatchEvent(new Event('focus'))
  browserDocument.dispatchEvent(new Event('visibilitychange'))
  const first = session.ensureFreshness()
  const second = session.ensureFreshness()
  const mutation = refreshWorkspace('ws_test')
  await new Promise<void>((resolve) => setTimeout(resolve, 5))
  assert.equal(sessionGets, 2)
  assert.equal(mutationHeaders.length, 0)

  refreshed.resolve(sessionResponse('csrf-refreshed', 2, 30 * 60 * 1000))
  await Promise.all([first, second, mutation])
  assert.equal(mutationHeaders.length, 1)
  assert.equal(mutationHeaders[0]?.get('X-StackPilot-CSRF'), 'csrf-refreshed')
  session.dispose()
})

test('REST session invalidation is global and blocks later mutations', async () => {
  installBrowserGlobals()
  setActivePinia(createPinia())
  browserWindow.location.hash = ''
  let invalid = false
  globalThis.fetch = (async (input: string | URL | Request) => {
    if (String(input) === '/api/v1/auth/session') return sessionResponse('csrf-initial', 1, 30 * 60 * 1000)
    if (!invalid) {
      invalid = true
      return errorResponse(401, 'AUTH_SESSION_INVALID')
    }
    throw new Error('a blocked mutation reached fetch')
  }) as typeof fetch

  const session = useSessionStore()
  await session.initialize()
  await assert.rejects(listWorkspaces())
  assert.equal(session.state, 'expired')
  await assert.rejects(refreshWorkspace('ws_test'), { name: 'AuthenticationUnavailableError' })
  session.dispose()
})

test('workspace unregister sends the browser mutation security headers', async () => {
  installBrowserGlobals()
  setActivePinia(createPinia())
  browserWindow.location.hash = ''
  let deletion: RequestInit | undefined
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    if (String(input) === '/api/v1/auth/session') return sessionResponse('csrf-delete', 1, 30 * 60 * 1000)
    deletion = init
    return new Response(null, { status: 204 })
  }) as typeof fetch

  const session = useSessionStore()
  await session.initialize()
  await unregisterWorkspace('ws_01ARZ3NDEKTSV4RRFFQ69G5FAV')
  const headers = new Headers(deletion?.headers)
  assert.equal(deletion?.method, 'DELETE')
  assert.equal(headers.get('Content-Type'), 'application/json')
  assert.equal(headers.get('X-StackPilot-CSRF'), 'csrf-delete')
  session.dispose()
})

function sessionResponse(csrf: string, _revision: number, lifetimeMs: number): Response {
  return new Response(JSON.stringify({ csrf, expiresAt: new Date(Date.now()+lifetimeMs).toISOString() }), {
    status: 200, headers: { 'Content-Type': 'application/json' },
  })
}

function installBrowserGlobals(): void {
  Object.defineProperty(globalThis, 'window', { configurable: true, value: browserWindow })
  Object.defineProperty(globalThis, 'document', { configurable: true, value: browserDocument })
  Object.defineProperty(globalThis, 'BroadcastChannel', { configurable: true, value: undefined })
}

function errorResponse(status: number, code: string): Response {
  return new Response(JSON.stringify({ error: { code, message: code, details: {}, traceId: 'trace-test' } }), {
    status, headers: { 'Content-Type': 'application/json' },
  })
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolvePromise: ((value: T) => void) | null = null
  const promise = new Promise<T>((resolve) => { resolvePromise = resolve })
  return { promise, resolve: (value) => resolvePromise?.(value) }
}

async function waitUntil(predicate: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt++) {
    if (predicate()) return
    await new Promise<void>((resolve) => setTimeout(resolve, 1))
  }
  throw new Error('timed out waiting for session request')
}
