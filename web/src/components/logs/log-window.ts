import type { LogEntry } from '../../api/types'

export const maximumLogEntries = 5000

export interface LogWindowState {
  entries: LogEntry[]
  pausedEntries: LogEntry[]
  paused: boolean
  bufferOverflow: boolean
  lastReceivedSequence: number
  viewFloorSequence: number
  viewCleared: boolean
}

export function createLogWindowState(entries: LogEntry[] = []): LogWindowState {
  const bounded = mergeLogEntries([], entries)
  return {
    entries: bounded,
    pausedEntries: [],
    paused: false,
    bufferOverflow: false,
    lastReceivedSequence: highestSequence(entries),
    viewFloorSequence: 0,
    viewCleared: false,
  }
}

export function ingestLogEntries(
  state: LogWindowState,
  incoming: LogEntry[],
  mode: 'merge' | 'replace' = 'merge',
): LogWindowState {
  const lastReceivedSequence = Math.max(state.lastReceivedSequence, highestSequence(incoming))
  const visibleIncoming = incoming.filter((entry) => entry.sequence > state.viewFloorSequence)
  if (!state.paused) {
    return {
      ...state,
      entries: mode === 'replace'
        ? mergeLogEntries([], visibleIncoming)
        : mergeLogEntries(state.entries, visibleIncoming),
      lastReceivedSequence,
    }
  }

  const visibleSequences = new Set(state.entries.map((entry) => entry.sequence))
  const pending = visibleIncoming.filter((entry) => !visibleSequences.has(entry.sequence))
  const unboundedPendingCount = uniqueSequenceCount(state.pausedEntries, pending)
  return {
    ...state,
    pausedEntries: mergeLogEntries(state.pausedEntries, pending),
    bufferOverflow: state.bufferOverflow || unboundedPendingCount > maximumLogEntries,
    lastReceivedSequence,
  }
}

export function setLogWindowPaused(state: LogWindowState, paused: boolean): LogWindowState {
  if (paused === state.paused) return state
  if (paused) return { ...state, paused: true }
  return {
    ...state,
    entries: mergeLogEntries(state.entries, state.pausedEntries),
    pausedEntries: [],
    paused: false,
  }
}

export function clearLogWindow(state: LogWindowState): LogWindowState {
  return {
    ...state,
    entries: [],
    pausedEntries: [],
    bufferOverflow: false,
    viewFloorSequence: state.lastReceivedSequence,
    viewCleared: true,
  }
}

export function markLogWindowRecovered(state: LogWindowState): LogWindowState {
  return { ...state, bufferOverflow: false }
}

export function mergeLogEntries(current: LogEntry[], incoming: LogEntry[]): LogEntry[] {
  const bySequence = new Map<number, LogEntry>()
  for (const entry of current) bySequence.set(entry.sequence, entry)
  for (const entry of incoming) bySequence.set(entry.sequence, entry)
  const sorted = [...bySequence.values()].sort((left, right) => left.sequence - right.sequence)
  return sorted.length <= maximumLogEntries ? sorted : sorted.slice(sorted.length - maximumLogEntries)
}

function highestSequence(entries: LogEntry[]): number {
  return entries.reduce((highest, entry) => Math.max(highest, entry.sequence), 0)
}

function uniqueSequenceCount(current: LogEntry[], incoming: LogEntry[]): number {
  return new Set([...current, ...incoming].map((entry) => entry.sequence)).size
}
