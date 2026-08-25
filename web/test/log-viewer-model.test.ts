import assert from 'node:assert/strict'
import test from 'node:test'
import {
  clearLogWindow,
  createLogWindowState,
  ingestLogEntries,
  maximumLogEntries,
  setLogWindowPaused,
} from '../src/components/logs/log-window.ts'
import {
  displayLogLevel,
  errorExcerpt,
  errorSequences,
  filterLogEntries,
  formatLogExport,
  logExportFilename,
  messagesOnly,
  safeFilenamePart,
} from '../src/components/logs/log-viewer-model.ts'
import { makeLogEntry, makeLogFixture } from './fixtures/logs.ts'

test('loads, deduplicates, sorts, and bounds the visible log window', () => {
  let state = createLogWindowState(makeLogFixture(500))
  assert.equal(state.entries.length, 500)
  assert.equal(state.lastReceivedSequence, 500)
  state = ingestLogEntries(state, [makeLogEntry(501), makeLogEntry(500, { message: 'replacement' })])
  assert.equal(state.entries.at(-1)?.sequence, 501)
  assert.equal(state.entries.find((entry) => entry.sequence === 500)?.message, 'replacement')

  state = ingestLogEntries(state, makeLogFixture(maximumLogEntries, 502))
  assert.equal(state.entries.length, maximumLogEntries)
  assert.equal(state.entries[0]?.sequence, 502)
  assert.equal(state.entries.at(-1)?.sequence, 5501)
})

test('pauses without changing visible entries and resumes in sequence order', () => {
  let state = setLogWindowPaused(createLogWindowState(makeLogFixture(3)), true)
  state = ingestLogEntries(state, [makeLogEntry(5), makeLogEntry(4), makeLogEntry(4)])
  assert.deepEqual(state.entries.map(sequence), [1, 2, 3])
  assert.deepEqual(state.pausedEntries.map(sequence), [4, 5])
  state = setLogWindowPaused(state, false)
  assert.deepEqual(state.entries.map(sequence), [1, 2, 3, 4, 5])
  assert.equal(state.pausedEntries.length, 0)
})

test('bounds the paused buffer and records overflow', () => {
  let state = setLogWindowPaused(createLogWindowState([makeLogEntry(1)]), true)
  state = ingestLogEntries(state, makeLogFixture(maximumLogEntries + 10, 2))
  assert.equal(state.pausedEntries.length, maximumLogEntries)
  assert.equal(state.pausedEntries[0]?.sequence, 12)
  assert.equal(state.bufferOverflow, true)
  assert.equal(state.lastReceivedSequence, maximumLogEntries + 11)
})

test('clears visible and paused logs at the highest received sequence', () => {
  let state = setLogWindowPaused(createLogWindowState(makeLogFixture(3)), true)
  state = ingestLogEntries(state, [makeLogEntry(4), makeLogEntry(5)])
  state = clearLogWindow(state)
  assert.equal(state.viewFloorSequence, 5)
  assert.equal(state.entries.length, 0)
  assert.equal(state.pausedEntries.length, 0)
  assert.equal(state.paused, true)
  assert.equal(state.bufferOverflow, false)
  assert.equal(state.viewCleared, true)

  state = ingestLogEntries(state, [makeLogEntry(5), makeLogEntry(6)])
  assert.deepEqual(state.pausedEntries.map(sequence), [6])
})

test('filters every merge and replace recovery path through the view floor', () => {
  let state = clearLogWindow(createLogWindowState(makeLogFixture(10)))
  state = ingestLogEntries(state, makeLogFixture(7, 4), 'merge')
  assert.equal(state.entries.length, 0)
  assert.equal(state.lastReceivedSequence, 10)

  state = ingestLogEntries(state, [makeLogEntry(8), makeLogEntry(11)], 'replace')
  assert.deepEqual(state.entries.map(sequence), [11])
  assert.equal(state.lastReceivedSequence, 11)

  state = ingestLogEntries(clearLogWindow(state), makeLogFixture(11), 'replace')
  assert.equal(state.entries.length, 0)
  assert.equal(state.lastReceivedSequence, 11)
})

test('keeps transport cursor monotonic when a replacement window is older or empty', () => {
  let state = createLogWindowState([makeLogEntry(20)])
  state = clearLogWindow(state)
  state = ingestLogEntries(state, [makeLogEntry(10), makeLogEntry(19)], 'replace')
  assert.equal(state.entries.length, 0)
  assert.equal(state.lastReceivedSequence, 20)
  state = ingestLogEntries(state, [], 'replace')
  assert.equal(state.lastReceivedSequence, 20)
})

test('opening a new scope creates a fresh floor and pause state', () => {
  let previous = clearLogWindow(createLogWindowState([makeLogEntry(50)]))
  previous = setLogWindowPaused(previous, true)
  const next = createLogWindowState([makeLogEntry(3, { serviceId: 'next-service' })])
  assert.equal(previous.viewFloorSequence, 50)
  assert.equal(next.viewFloorSequence, 0)
  assert.equal(next.paused, false)
  assert.deepEqual(next.entries.map(sequence), [3])
})

test('normalizes levels and derives errors without upgrading stderr', () => {
  const entries = [
    makeLogEntry(1, { stream: 'stderr', level: 'info' }),
    makeLogEntry(2, { level: 'ERROR' }),
    makeLogEntry(3, { level: 'future-level' }),
    makeLogEntry(4, { level: 'fatal' }),
  ]
  assert.equal(displayLogLevel('future-level'), 'unknown')
  assert.deepEqual(errorSequences(entries), [2, 4])
})

test('merges nearby errors into a block with five outer context entries', () => {
  const entries = Array.from({ length: 24 }, (_, index) => makeLogEntry(index + 1, { level: 'info', message: `m${index + 1}` }))
  entries[7] = makeLogEntry(8, { level: 'error', message: 'error-a' })
  entries[10] = makeLogEntry(11, { level: 'fatal', message: 'error-b' })
  entries[14] = makeLogEntry(15, { level: 'error', message: 'separate-error' })
  assert.deepEqual(errorExcerpt(entries, 8).map(sequence), [3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16])
  assert.equal(errorExcerpt(entries, 1).length, 0)
  assert.equal(messagesOnly(errorExcerpt(entries, 8)).includes('error-a'), true)
})

test('filters the bounded snapshot and exports stable UTC text in sequence order', () => {
  const entries = [
    makeLogEntry(2, { message: 'second', level: 'warn' }),
    makeLogEntry(1, { message: 'first', level: 'info' }),
  ]
  assert.deepEqual(filterLogEntries(entries, 'WARN').map(sequence), [2])
  assert.equal(formatLogExport(entries), [
    '2026-08-19T07:30:01.000Z INFO stdout first',
    '2026-08-19T07:30:02.000Z WARN stdout second',
  ].join('\n'))
})

test('creates a safe TXT filename and rejects Windows device names', () => {
  assert.equal(safeFilenamePart('CON', 'fallback'), 'fallback')
  assert.equal(safeFilenamePart('../BTC:Backend', 'fallback'), 'btc-backend')
  assert.equal(
    logExportFilename('../BTC', 'backend:web', 'si_01/unsafe', new Date('2026-08-19T07:30:00Z')),
    'btc-backend-web-si_01-unsafe-20260819T073000Z.txt',
  )
})

test('the stress fixture is bounded, fictional, and covers dynamic-height content', () => {
  const fixture = makeLogFixture(maximumLogEntries)
  assert.equal(fixture.length, maximumLogEntries)
  assert.equal(new Set(fixture.map((entry) => displayLogLevel(entry.level))).size, 7)
  assert.equal(fixture.some((entry) => entry.message.includes('\n')), true)
  assert.equal(fixture.some((entry) => entry.message.length > 5000), true)
  assert.equal(fixture.every((entry) => entry.systemId === 'fixture-system'), true)
})

function sequence(entry: { sequence: number }): number {
  return entry.sequence
}
