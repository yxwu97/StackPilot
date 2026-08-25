import { defineStore } from 'pinia'
import { ref } from 'vue'
import { ResilientEventStream, StreamHTTPError } from '../api/stream'
import { isAuthenticationFailure } from '../api/auth-lifecycle'
import type { ServerSentEvent, StreamConnectionState } from '../api/stream'

const snapshotDebounceMs = 100

export const useEventStore = defineStore('events', () => {
  const connectionState = ref<StreamConnectionState>('idle')
  const connectionError = ref<string | null>(null)
  const lastEventId = ref<string | null>(null)
  let stream: ResilientEventStream | null = null
  let refreshTimer: number | null = null
  let refreshSnapshots: (() => Promise<void>) | null = null

  function start(refresh: () => Promise<void>): void {
    stop()
    refreshSnapshots = refresh
    stream = new ResilientEventStream({
      url: '/api/v1/events',
      cursor: lastEventId.value,
      onEvent: handleEvent,
      onState(state, reason) {
        connectionState.value = state
        connectionError.value = state === 'connected' || state === 'idle' ? null : streamErrorMessage(reason)
      },
      async onCursorExpired() {
        lastEventId.value = null
        await refresh()
        return null
      },
    })
    stream.start()
  }

  function handleEvent(event: ServerSentEvent): void {
    if (event.id !== '') lastEventId.value = event.id
    scheduleSnapshotRefresh()
  }

  function scheduleSnapshotRefresh(): void {
    if (refreshTimer !== null || refreshSnapshots === null) return
    refreshTimer = window.setTimeout(() => {
      refreshTimer = null
      if (refreshSnapshots !== null) void refreshSnapshots()
    }, snapshotDebounceMs)
  }

  function stop(): void {
    stream?.stop()
    stream = null
    if (refreshTimer !== null) window.clearTimeout(refreshTimer)
    refreshTimer = null
    refreshSnapshots = null
  }

  return { connectionState, connectionError, lastEventId, start, stop }
})

function streamErrorMessage(reason: Error | null): string | null {
  if (reason === null) return null
  if (isAuthenticationFailure(reason)) return null
  return reason instanceof StreamHTTPError ? `${reason.code}: ${reason.message}` : reason.message
}
