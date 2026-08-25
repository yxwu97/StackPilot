import type { ErrorEnvelope } from './types'
import { publishAuthenticationInvalidation, sessionInvalidCode } from './auth-lifecycle.ts'

export type StreamConnectionState = 'idle' | 'connecting' | 'connected' | 'reconnecting' | 'error'

export interface ServerSentEvent {
  id: string
  type: string
  data: string
}

export class StreamHTTPError extends Error {
  readonly status: number
  readonly code: string
  readonly traceId: string | null

  constructor(
    status: number,
    code: string,
    message: string,
    traceId: string | null,
  ) {
    super(message)
    this.name = 'StreamHTTPError'
    this.status = status
    this.code = code
    this.traceId = traceId
  }
}

interface StreamOptions {
  url: string | (() => string)
  cursor?: string | null
  onEvent: (event: ServerSentEvent) => void
  onState?: (state: StreamConnectionState, reason: Error | null) => void
  onCursorExpired?: () => Promise<string | null>
  onRetry?: () => Promise<string | null | undefined>
  cursorQueryParameter?: string
  fetcher?: typeof fetch
  retryMinimumMs?: number
  retryMaximumMs?: number
}

const defaultRetryMinimumMs = 500
const defaultRetryMaximumMs = 15_000

export class ResilientEventStream {
  private readonly options: StreamOptions
  private readonly fetcher: typeof fetch
  private readonly retryMinimumMs: number
  private readonly retryMaximumMs: number
  private controller: AbortController | null = null
  private cursor: string | null
  private running = false

  constructor(options: StreamOptions) {
    this.options = options
    this.fetcher = options.fetcher ?? window.fetch.bind(window)
    this.retryMinimumMs = options.retryMinimumMs ?? defaultRetryMinimumMs
    this.retryMaximumMs = options.retryMaximumMs ?? defaultRetryMaximumMs
    this.cursor = options.cursor ?? null
  }

  start(): void {
    if (this.running) return
    this.running = true
    this.controller = new AbortController()
    void this.run(this.controller.signal)
  }

  stop(): void {
    this.running = false
    this.controller?.abort()
    this.controller = null
    this.options.onState?.('idle', null)
  }

  private async run(signal: AbortSignal): Promise<void> {
    let attempt = 0
    while (this.running && !signal.aborted) {
      this.options.onState?.(attempt === 0 ? 'connecting' : 'reconnecting', null)
      try {
        const response = await this.connect(signal)
        if (await this.handleBoundaryResponse(response)) {
          attempt = 0
          continue
        }
        this.options.onState?.('connected', null)
        await consumeEventStream(response, (event) => this.handleEvent(event), signal)
        if (this.running && !signal.aborted) throw new Error('SSE 连接已关闭。')
      } catch (reason: unknown) {
        if (!this.running || signal.aborted) return
        const error = normalizeError(reason)
        if (isFatalStreamFailure(error)) {
          this.running = false
          this.options.onState?.('error', error)
          return
        }
        this.options.onState?.('reconnecting', error)
        await this.recoverCursor()
        attempt += 1
        await abortableDelay(retryDelay(attempt, this.retryMinimumMs, this.retryMaximumMs), signal)
      }
    }
  }

  private async connect(signal: AbortSignal): Promise<Response> {
    const headers = new Headers({ Accept: 'text/event-stream' })
    let url = typeof this.options.url === 'function' ? this.options.url() : this.options.url
    if (this.cursor !== null && this.options.cursorQueryParameter !== undefined) {
      const parsed = new URL(url, window.location.origin)
      parsed.searchParams.set(this.options.cursorQueryParameter, this.cursor)
      url = `${parsed.pathname}${parsed.search}`
    } else if (this.cursor !== null) {
      headers.set('Last-Event-ID', this.cursor)
    }
    return this.fetcher(url, {
      credentials: 'same-origin', headers, signal,
    })
  }

  private async handleBoundaryResponse(response: Response): Promise<boolean> {
    if (response.ok && response.body !== null) return false
    const error = await streamHTTPError(response)
    if (response.status === 401 && error.code === sessionInvalidCode) {
      publishAuthenticationInvalidation(error.code)
    }
    if (response.status === 409 && error.code.endsWith('CURSOR_EXPIRED') && this.options.onCursorExpired !== undefined) {
      this.cursor = await this.options.onCursorExpired()
      return true
    }
    throw error
  }

  private handleEvent(event: ServerSentEvent): void {
    if (event.id !== '') this.cursor = event.id
    this.options.onEvent(event)
  }

  private async recoverCursor(): Promise<void> {
    if (this.options.onRetry === undefined) return
    try {
      const recovered = await this.options.onRetry()
      if (recovered !== undefined) this.cursor = recovered
    } catch {
      // The retry loop remains responsible for recovery when the REST snapshot is also unavailable.
    }
  }
}

export async function consumeEventStream(
  response: Response,
  onEvent: (event: ServerSentEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  if (response.body === null) throw new Error('SSE 响应缺少正文。')
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  try {
    while (!signal?.aborted) {
      const { done, value } = await reader.read()
      buffer += decoder.decode(value, { stream: !done })
      const parsed = parseSSEBuffer(buffer, done)
      buffer = parsed.remainder
      for (const event of parsed.events) onEvent(event)
      if (done) return
    }
  } finally {
    reader.releaseLock()
  }
}

export function parseSSEBuffer(value: string, flush = false): { events: ServerSentEvent[]; remainder: string } {
  const blocks: string[] = []
  const boundary = /\r\n\r\n|\n\n|\r\r/g
  let start = 0
  for (let match = boundary.exec(value); match !== null; match = boundary.exec(value)) {
    blocks.push(value.slice(start, match.index))
    start = match.index + match[0].length
  }
  if (flush && start < value.length) blocks.push(value.slice(start))
  const remainder = flush ? '' : value.slice(start)
  const events: ServerSentEvent[] = []
  for (const block of blocks) {
    const event = parseEventBlock(block.replace(/\r\n/g, '\n').replace(/\r/g, '\n'))
    if (event !== null) events.push(event)
  }
  return { events, remainder }
}

export function retryDelay(attempt: number, minimumMs = defaultRetryMinimumMs, maximumMs = defaultRetryMaximumMs): number {
  return Math.min(maximumMs, minimumMs * 2 ** Math.max(0, attempt - 1))
}

function parseEventBlock(block: string): ServerSentEvent | null {
  let id = ''
  let type = 'message'
  const data: string[] = []
  for (const line of block.split('\n')) {
    if (line === '' || line.startsWith(':')) continue
    const separator = line.indexOf(':')
    const field = separator === -1 ? line : line.slice(0, separator)
    let content = separator === -1 ? '' : line.slice(separator + 1)
    if (content.startsWith(' ')) content = content.slice(1)
    if (field === 'id' && !content.includes('\0')) id = content
    if (field === 'event') type = content
    if (field === 'data') data.push(content)
  }
  return data.length === 0 ? null : { id, type, data: data.join('\n') }
}

async function streamHTTPError(response: Response): Promise<StreamHTTPError> {
  try {
    const body = await response.json() as ErrorEnvelope
    return new StreamHTTPError(response.status, body.error.code, body.error.message, body.error.traceId)
  } catch {
    return new StreamHTTPError(response.status, 'STREAM_HTTP_ERROR', `SSE 请求失败 (${response.status})。`, null)
  }
}

function isFatalStreamFailure(error: Error): boolean {
  return error instanceof StreamHTTPError && error.status >= 400 && error.status < 500
}

function normalizeError(reason: unknown): Error {
  return reason instanceof Error ? reason : new Error('SSE 连接失败。')
}

async function abortableDelay(milliseconds: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) return
  await new Promise<void>((resolve) => {
    const finish = () => {
      window.clearTimeout(timer)
      signal.removeEventListener('abort', finish)
      resolve()
    }
    const timer = window.setTimeout(finish, milliseconds)
    signal.addEventListener('abort', finish, { once: true })
  })
}
