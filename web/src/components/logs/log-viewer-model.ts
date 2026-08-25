import type { LogEntry } from '../../api/types'

export type DisplayLogLevel = 'trace' | 'debug' | 'info' | 'unknown' | 'warn' | 'error' | 'fatal'

export function displayLogLevel(level: string): DisplayLogLevel {
  const normalized = level.toLocaleLowerCase()
  if (normalized === 'trace' || normalized === 'debug' || normalized === 'info'
    || normalized === 'warn' || normalized === 'error' || normalized === 'fatal') return normalized
  return 'unknown'
}

export function filterLogEntries(entries: LogEntry[], query: string): LogEntry[] {
  const normalized = query.trim().toLocaleLowerCase()
  if (normalized === '') return entries
  return entries.filter((entry) =>
    `${entry.level} ${entry.stream} ${entry.message}`.toLocaleLowerCase().includes(normalized),
  )
}

export function errorSequences(entries: LogEntry[]): number[] {
  return entries
    .filter((entry) => {
      const level = displayLogLevel(entry.level)
      return level === 'error' || level === 'fatal'
    })
    .map((entry) => entry.sequence)
}

export function errorExcerpt(entries: LogEntry[], targetSequence: number): LogEntry[] {
  const targetIndex = entries.findIndex((entry) => entry.sequence === targetSequence)
  if (targetIndex < 0 || !isError(entries[targetIndex])) return []

  let blockStart = targetIndex
  let blockEnd = targetIndex
  while (previousErrorIndex(entries, blockStart) >= 0) {
    const previous = previousErrorIndex(entries, blockStart)
    if (blockStart - previous - 1 > 2) break
    blockStart = previous
  }
  while (nextErrorIndex(entries, blockEnd) >= 0) {
    const next = nextErrorIndex(entries, blockEnd)
    if (next - blockEnd - 1 > 2) break
    blockEnd = next
  }
  return entries.slice(Math.max(0, blockStart - 5), Math.min(entries.length, blockEnd + 6))
}

export function messagesOnly(entries: LogEntry[]): string {
  return [...entries].sort(bySequence).map((entry) => entry.message).join('\n')
}

export function formatLogExport(entries: LogEntry[]): string {
  return [...entries].sort(bySequence).map((entry) =>
    `${new Date(entry.timestamp).toISOString()} ${displayLogLevel(entry.level).toUpperCase()} ${entry.stream} ${entry.message}`,
  ).join('\n')
}

export function logExportFilename(
  systemName: string,
  serviceId: string,
  instanceId: string,
  now: Date,
  suffix = '',
): string {
  const system = safeFilenamePart(systemName, 'system')
  const service = safeFilenamePart(serviceId, 'service')
  const instance = safeFilenamePart(instanceId, 'instance').slice(0, 12)
  const timestamp = now.toISOString().replaceAll('-', '').replaceAll(':', '').replace(/\.\d{3}Z$/, 'Z')
  return `${system}-${service}-${instance}-${timestamp}${suffix}.txt`
}

export function safeFilenamePart(value: string, fallback: string): string {
  const normalized = value.normalize('NFKD').toLocaleLowerCase()
    .replace(/[<>:"/\\|?*\u0000-\u001f]/g, '-')
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/^[. -]+|[. -]+$/g, '')
    .replace(/-+/g, '-')
  if (normalized === '' || /^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)/i.test(normalized)) return fallback
  return normalized
}

function isError(entry: LogEntry | undefined): boolean {
  if (entry === undefined) return false
  const level = displayLogLevel(entry.level)
  return level === 'error' || level === 'fatal'
}

function previousErrorIndex(entries: LogEntry[], before: number): number {
  for (let index = before - 1; index >= 0; index -= 1) {
    if (isError(entries[index])) return index
  }
  return -1
}

function nextErrorIndex(entries: LogEntry[], after: number): number {
  for (let index = after + 1; index < entries.length; index += 1) {
    if (isError(entries[index])) return index
  }
  return -1
}

function bySequence(left: LogEntry, right: LogEntry): number {
  return left.sequence - right.sequence
}
