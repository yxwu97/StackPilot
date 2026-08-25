import assert from 'node:assert/strict'
import test from 'node:test'
import { ResilientEventStream, consumeEventStream, parseSSEBuffer, retryDelay } from '../src/api/stream.ts'
import { configureAuthenticationBridge } from '../src/api/auth-lifecycle.ts'

Object.defineProperty(globalThis, 'window', {
  configurable: true,
  value: { location: { origin: 'http://127.0.0.1' }, setTimeout, clearTimeout },
})

test('parses chunked CRLF events, comments, and multiline data', async () => {
  const encoder = new TextEncoder()
  const response = new Response(new ReadableStream({
    start(controller) {
      controller.enqueue(encoder.encode(': heartbeat\r\nid: 41\r\nevent: operation.state.changed\r\ndata: {"state":\r\n'))
      controller.enqueue(encoder.encode('data: "running"}\r\n\r\n'))
      controller.close()
    },
  }))
  const events: unknown[] = []
  await consumeEventStream(response, (event) => events.push(event))
  assert.deepEqual(events, [{ id: '41', type: 'operation.state.changed', data: '{"state":\n"running"}' }])
})

test('keeps incomplete records for the next network chunk', () => {
  const first = parseSSEBuffer('id: 7\ndata: fir')
  assert.deepEqual(first.events, [])
  const second = parseSSEBuffer(`${first.remainder}st\n\nid: 8\ndata: next\n\n`)
  assert.deepEqual(second.events.map((event) => event.id), ['7', '8'])
})

test('bounds exponential reconnect delay', () => {
  assert.equal(retryDelay(1, 500, 4_000), 500)
  assert.equal(retryDelay(4, 500, 4_000), 4_000)
  assert.equal(retryDelay(20, 500, 4_000), 4_000)
})

test('refreshes snapshots after an expired cursor and reconnects without it', async () => {
  const requests: RequestInit[] = []
  let stream: ResilientEventStream
  let refreshes = 0
  const fetcher = (async (_input: string | URL | Request, init?: RequestInit) => {
    requests.push(init ?? {})
    if (requests.length === 1) {
      return errorResponse(409, 'EVENT_CURSOR_EXPIRED')
    }
    return openEventResponse()
  }) as typeof fetch
  stream = new ResilientEventStream({
    url: '/api/v1/events', cursor: '3', fetcher,
    onEvent() {},
    onState(state) {
      if (state === 'connected') stream.stop()
    },
    async onCursorExpired() {
      refreshes += 1
      return null
    },
  })
  stream.start()
  await waitUntil(() => requests.length === 2)
  assert.equal(new Headers(requests[0]?.headers).get('Last-Event-ID'), '3')
  assert.equal(new Headers(requests[1]?.headers).has('Last-Event-ID'), false)
  assert.equal(refreshes, 1)
})

test('reloads a log window and resumes with its newest sequence', async () => {
  const urls: string[] = []
  let stream: ResilientEventStream
  let connections = 0
  const fetcher = (async (input: string | URL | Request) => {
    urls.push(String(input))
    return urls.length === 1
      ? new Response('id: 5\nevent: log.entry\ndata: {}\n\n')
      : openEventResponse()
  }) as typeof fetch
  stream = new ResilientEventStream({
    url: '/api/v1/log-stream?serviceId=backend', cursor: '2', cursorQueryParameter: 'afterSequence', fetcher,
    retryMinimumMs: 0, retryMaximumMs: 0,
    onEvent() {},
    onState(state) {
      if (state === 'connected' && ++connections === 2) stream.stop()
    },
    async onRetry() {
      return '7'
    },
  })
  stream.start()
  await waitUntil(() => urls.length === 2)
  assert.equal(new URL(urls[0]!, 'http://127.0.0.1').searchParams.get('afterSequence'), '2')
  assert.equal(new URL(urls[1]!, 'http://127.0.0.1').searchParams.get('afterSequence'), '7')
})

test('stops reconnecting after authentication failure', async () => {
  let calls = 0
  let invalidations = 0
  let finalState = ''
  configureAuthenticationBridge({
    async beforeMutation() { return 'unused' },
    invalidate() { invalidations += 1 },
  })
  const stream = new ResilientEventStream({
    url: '/api/v1/events',
    fetcher: (async () => {
      calls += 1
      return errorResponse(401, 'AUTH_SESSION_INVALID')
    }) as typeof fetch,
    onEvent() {},
    onState(state) {
      finalState = state
    },
  })
  stream.start()
  await waitUntil(() => finalState === 'error')
  await new Promise<void>((resolve) => setTimeout(resolve, 5))
  assert.equal(calls, 1)
  assert.equal(invalidations, 1)
  assert.equal(finalState, 'error')
  configureAuthenticationBridge(null)
})

function errorResponse(status: number, code: string): Response {
  return new Response(JSON.stringify({ error: { code, message: code, details: {}, traceId: 'trace-test' } }), {
    status, headers: { 'Content-Type': 'application/json' },
  })
}

function openEventResponse(): Response {
  return new Response(new ReadableStream({ start() {} }), { headers: { 'Content-Type': 'text/event-stream' } })
}

async function waitUntil(predicate: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (predicate()) return
    await new Promise<void>((resolve) => setTimeout(resolve, 1))
  }
  throw new Error('timed out waiting for stream state')
}
