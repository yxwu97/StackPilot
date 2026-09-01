import type { ChangeRisk, MetricPoint, Operation } from '../api/types'

export function latestAvailable(points: MetricPoint[]): MetricPoint | null {
  for (let index = points.length - 1; index >= 0; index -= 1) {
    if (points[index]?.status === 'available') return points[index] ?? null
  }
  return null
}

export function formatBytes(value: number | undefined): string {
  if (value === undefined) return '--'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let current = value
  let unit = 0
  while (current >= 1024 && unit < units.length - 1) {
    current /= 1024
    unit += 1
  }
  return `${current.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}

export function sparklinePoints(points: MetricPoint[], field: 'cpuPercent' | 'memoryBytes'): string {
  const values = points.flatMap((point) => point.status === 'available' && point[field] !== undefined ? [point[field]] : [])
  if (values.length === 0) return ''
  const maximum = Math.max(...values)
  const minimum = Math.min(...values)
  const span = Math.max(1, maximum - minimum)
  return values.map((value, index) => {
    const x = values.length === 1 ? 50 : (index / (values.length - 1)) * 100
    const y = 32 - ((value - minimum) / span) * 28
    return `${x.toFixed(1)},${y.toFixed(1)}`
  }).join(' ')
}

export function planIDFromOperation(operation: Operation): string | null {
  const step = operation.steps.find((candidate) => candidate.key === 'persist-plan' && candidate.state === 'succeeded')
  return step?.detailRef ?? null
}

export function riskTag(risk: ChangeRisk): 'danger' | 'warning' | 'success' | 'info' {
  if (risk === 'blocked') return 'danger'
  if (risk === 'high' || risk === 'medium') return 'warning'
  if (risk === 'low') return 'success'
  return 'info'
}
