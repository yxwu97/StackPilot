async (page) => {
let authCalls = 0
let expiredRequests = 0
let expireSession = false
const initialCSRF = 'ccccccccccccccccccccccccccccccccccccccccccc'
const renewedCSRF = 'rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr'

await page.route('**/api/v1/**', async (route) => {
  const request = route.request()
  const path = request.url().replace(/^https?:\/\/[^/]+/, '').split('?')[0]
  if (expireSession && path !== '/api/v1/events') {
    expiredRequests++
    await route.fulfill({
      status: 401,
      contentType: 'application/json',
      body: JSON.stringify({ error: { code: 'AUTH_SESSION_INVALID', message: 'Session invalid.', details: {}, traceId: 'trace-fixture' } }),
    })
    return
  }
  if (path === '/api/v1/auth/session') {
    authCalls++
    const lifetime = authCalls === 1 ? 4 * 60 * 1000 : 30 * 60 * 1000
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: { 'Set-Cookie': `stackpilot_session=fixture; Path=/; HttpOnly; SameSite=Strict; Max-Age=${lifetime / 1000}` },
      body: JSON.stringify({ csrf: authCalls === 1 ? initialCSRF : renewedCSRF, expiresAt: new Date(Date.now() + lifetime).toISOString() }),
    })
    return
  }
  if (path === '/api/v1/systems' || path === '/api/v1/workspaces') {
    await route.fulfill({ status: 200, contentType: 'application/json', body: '{"items":[]}' })
    return
  }
  if (path === '/api/v1/events') {
    await route.fulfill({ status: 200, contentType: 'text/event-stream', body: ': fixture\n\n' })
    return
  }
  await route.fulfill({ status: 404, contentType: 'application/json', body: '{}' })
})

await page.reload()
await page.locator('.app-shell').waitFor({ state: 'visible' })
for (let attempt = 0; attempt < 100 && authCalls < 2; attempt++) await page.waitForTimeout(20)
if (authCalls !== 2) throw new Error(`expected one initial session request and one renewal, got ${authCalls}`)

const cookies = await page.context().cookies()
const sessionCookie = cookies.find((cookie) => cookie.name === 'stackpilot_session')
if (sessionCookie === undefined || !sessionCookie.httpOnly || sessionCookie.sameSite !== 'Strict' || sessionCookie.path !== '/') {
  throw new Error('session cookie attributes were not preserved')
}
const storage = await page.evaluate(async () => ({
  local: Object.keys(localStorage),
  session: Object.keys(sessionStorage),
  indexedDB: typeof indexedDB.databases === 'function' ? (await indexedDB.databases()).map((entry) => entry.name) : [],
}))
if (storage.local.length !== 0 || storage.session.length !== 0 || storage.indexedDB.length !== 0) {
  throw new Error('authentication material reached persistent browser storage')
}

await page.screenshot({ path: 'output/playwright/session-active.png', fullPage: true })
expireSession = true
await page.getByRole('button', { name: '重新加载', exact: true }).click()
await page.getByText('浏览器会话已失效', { exact: true }).waitFor({ state: 'visible' })
const requestCountAfterInvalidation = expiredRequests
await page.waitForTimeout(750)
if (expiredRequests !== requestCountAfterInvalidation || expiredRequests < 1 || expiredRequests > 2) {
  throw new Error(`session invalidation did not quiesce requests: ${expiredRequests}`)
}
if (await page.getByRole('button', { name: '重新检测', exact: true }).count() !== 0) {
  throw new Error('expired session exposed an invalid ordinary retry action')
}
await page.screenshot({ path: 'output/playwright/session-invalidated.png', fullPage: true })
console.log(JSON.stringify({ authCalls, expiredRequests, storage, state: 'expired' }))
}
