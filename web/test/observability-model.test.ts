import assert from 'node:assert/strict'
import test from 'node:test'
import type { MetricPoint, Operation } from '../src/api/types.ts'
import { formatBytes, latestAvailable, planIDFromOperation, sparklinePoints } from '../src/observability/model.ts'

test('keeps unavailable metrics explicit and selects the newest available point', () => {
  const points: MetricPoint[] = [
    { observedAt: '2026-08-31T10:00:00Z', status: 'available', cpuPercent: 2 },
    { observedAt: '2026-08-31T10:00:15Z', status: 'unavailable', reasonCode: 'PROCESS_IDENTITY_UNAVAILABLE' },
  ]
  assert.equal(latestAvailable(points)?.cpuPercent, 2)
  assert.equal(latestAvailable(points.slice(1)), null)
})

test('builds bounded sparkline coordinates without treating missing points as zero', () => {
  const points: MetricPoint[] = [
    { observedAt: '2026-08-31T10:00:00Z', status: 'available', cpuPercent: 10 },
    { observedAt: '2026-08-31T10:00:15Z', status: 'unsupported' },
    { observedAt: '2026-08-31T10:00:30Z', status: 'available', cpuPercent: 30 },
  ]
  assert.equal(sparklinePoints(points, 'cpuPercent'), '0.0,32.0 100.0,4.0')
  assert.equal(sparklinePoints(points.slice(1, 2), 'cpuPercent'), '')
  assert.equal(formatBytes(1024 * 1024), '1.0 MiB')
})

test('reads a plan id only from a successful persist-plan step', () => {
  const operation = {
    id: 'op_fixture', workspaceId: 'ws_fixture', systemId: 'fixture', type: 'change-plan', state: 'succeeded', cancellable: true,
    createdAt: '2026-08-31T10:00:00Z',
    steps: [{ number: 1, key: 'persist-plan', state: 'succeeded', attempt: 1, detailRef: 'plan_fixture' }],
  } satisfies Operation
  assert.equal(planIDFromOperation(operation), 'plan_fixture')
  assert.equal(planIDFromOperation({ ...operation, steps: [{ ...operation.steps[0], state: 'failed' }] }), null)
})
