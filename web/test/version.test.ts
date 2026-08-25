import assert from 'node:assert/strict'
import test from 'node:test'
import { getVersion } from '../src/api/client.ts'

test('loads the running executable version from the root endpoint', async () => {
  let requestedURL = ''
  globalThis.fetch = (async (input: string | URL | Request) => {
    requestedURL = String(input)
    return Response.json({
      version: '0.1.0', commit: 'abc123', buildTime: '2026-08-19T12:00:00Z', apiVersion: 'v1', capabilities: [],
    })
  }) as typeof fetch

  const version = await getVersion()
  assert.equal(requestedURL, '/version')
  assert.equal(version.version, '0.1.0')
})

test('rejects a noncanonical executable version response', async () => {
  globalThis.fetch = (async () => Response.json({
    version: 'dev', commit: 'unknown', buildTime: 'unknown', apiVersion: 'v1', capabilities: [],
  })) as typeof fetch

  await assert.rejects(getVersion(), /无效的系统版本信息/)
})

test('reports version endpoint failures without treating them as data', async () => {
  globalThis.fetch = (async () => Response.json({
    error: { code: 'INTERNAL', message: 'unavailable', details: {}, traceId: 'tr_test' },
  }, { status: 503 })) as typeof fetch

  await assert.rejects(getVersion(), /unavailable/)
})
