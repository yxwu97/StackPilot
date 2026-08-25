import { createPinia } from 'pinia'
import { createApp, h, ref } from 'vue'
import 'element-plus/dist/index.css'
import '../../src/styles.css'
import ServiceLogViewer from '../../src/components/logs/ServiceLogViewer.vue'
import type { ServiceRuntimeStatus } from '../../src/api/types'
import { makeLogFixture } from '../fixtures/logs'

const logs = makeLogFixture(5000)
const encoder = new TextEncoder()
const originalFetch = globalThis.fetch
let streamController: ReadableStreamDefaultController<Uint8Array> | null = null
let nextSequence = 5001

declare global {
  interface Window {
    __stackPilotPushFixtureLogs: (count: number) => void
    __stackPilotCopiedText: string
  }
}

window.__stackPilotCopiedText = ''
Object.defineProperty(navigator, 'clipboard', {
  configurable: true,
  value: {
    async writeText(value: string): Promise<void> {
      window.__stackPilotCopiedText = value
    },
  },
})

window.__stackPilotPushFixtureLogs = (count: number): void => {
  if (streamController === null) return
  for (let offset = 0; offset < count; offset += 1) {
    const entry = makeLogFixture(1, nextSequence)[0]
    nextSequence += 1
    if (entry !== undefined) streamController.enqueue(encoder.encode(
      `id: ${entry.sequence}\nevent: log.entry\ndata: ${JSON.stringify(entry)}\n\n`,
    ))
  }
}

globalThis.fetch = async (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
  const url = String(input instanceof Request ? input.url : input)
  if (url.includes('/api/v1/services/') && url.includes('/logs?')) {
    return Response.json({ items: logs.slice(0, 500), nextCursor: 500 })
  }
  if (url.includes('/api/v1/log-stream')) {
    return new Response(new ReadableStream<Uint8Array>({
      start(controller) {
        streamController = controller
        const chunks: string[] = []
        for (const entry of logs.slice(500)) {
          chunks.push(`id: ${entry.sequence}\nevent: log.entry\ndata: ${JSON.stringify(entry)}\n\n`)
          if (chunks.length === 100) {
            controller.enqueue(encoder.encode(chunks.join('')))
            chunks.length = 0
          }
        }
        if (chunks.length > 0) controller.enqueue(encoder.encode(chunks.join('')))
      },
      cancel() {
        streamController = null
      },
    }), { headers: { 'Content-Type': 'text/event-stream' } })
  }
  return originalFetch(input, init)
}

const service: ServiceRuntimeStatus = {
  serviceId: 'fixture-service',
  serviceInstanceId: 'fixture-service-instance-01',
  driver: 'process',
  mode: 'daemon',
  state: 'ready',
  stateVersion: 1,
  pid: 4242,
  processStartedAt: '2026-08-19T07:30:00Z',
  commandDigest: 'sha256:fictional-command-digest',
  dependsOn: [],
  containers: [],
}

createApp({
  setup() {
    const open = ref(true)
    return () => h(ServiceLogViewer, {
      modelValue: open.value,
      service,
      systemId: 'fixture-system',
      systemName: 'Fixture System',
      instanceId: 'fixture-instance-01',
      manifestInvalid: false,
      operationActive: false,
      restarting: false,
      'onUpdate:modelValue': (value: boolean) => { open.value = value },
    })
  },
}).use(createPinia()).mount('#app')
