import type { LogEntry } from '../../src/api/types'

const levels = ['trace', 'debug', 'info', 'unknown', 'warn', 'error', 'fatal'] as const

export function makeLogEntry(sequence: number, overrides: Partial<LogEntry> = {}): LogEntry {
  return {
    timestamp: new Date(Date.UTC(2026, 7, 19, 7, 30, sequence % 60)).toISOString(),
    systemId: 'fixture-system',
    instanceId: 'fixture-instance-01',
    serviceId: 'fixture-service',
    stream: sequence % 4 === 0 ? 'stderr' : 'stdout',
    level: levels[sequence % levels.length] ?? 'unknown',
    message: fixtureMessage(sequence),
    sequence,
    truncated: false,
    ...overrides,
  }
}

export function makeLogFixture(count = 5000, startSequence = 1): LogEntry[] {
  return Array.from({ length: count }, (_, index) => makeLogEntry(startSequence + index))
}

function fixtureMessage(sequence: number): string {
  if (sequence % 101 === 0) return `stack frame ${sequence}\n  at fixture.module.run(file.ts:42:7)\n  at fixture.main(file.ts:8:3)`
  if (sequence % 97 === 0) return `https://fixture.invalid/services/${sequence}/events?cursor=${'x'.repeat(180)}`
  if (sequence % 89 === 0) return `unbroken-${'A'.repeat(240)}`
  if (sequence % 83 === 0) return `${'near-limit fictional text '.repeat(400)}[truncated fixture]`
  return `fictional service log line ${sequence}`
}
