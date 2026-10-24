// Real HTTPS browser test. For disposable fixtures only: PANEL_SMOKE_SELF_SIGNED=1.
// current saves a test session; upgrade reuses it after candidate replacement.
import assert from 'node:assert/strict'
import { chromium } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
import { createHash } from 'node:crypto'

const base = process.env.PANEL_BASE?.replace(/\/$/, '')
const password = process.env.PANEL_ADMIN_PASSWORD
const phase = process.env.PANEL_SMOKE_PHASE || 'current'
const output = process.env.PANEL_BROWSER_EVIDENCE
const state = process.env.PANEL_BROWSER_STATE
assert(base?.startsWith('https://'), 'PANEL_BASE must be an HTTPS origin')
assert(password, 'PANEL_ADMIN_PASSWORD is required')
if (output) await mkdir(output, { recursive: true, mode: 0o700 })
const report = { phase, base, status: 'failed', checks: [], errors: [], responses: [] }
const browser = await chromium.launch({ headless: true })
let context
try {
  context = await browser.newContext({
    ignoreHTTPSErrors: process.env.PANEL_SMOKE_SELF_SIGNED === '1',
    viewport: { width: 1440, height: 1000 },
    ...(phase === 'upgrade' && state ? { storageState: state } : {}),
  })
  await context.tracing.start({ screenshots: true, snapshots: true, sources: true })
  const page = await context.newPage()
  page.on('pageerror', (error) => report.errors.push(error.message))
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    const url = msg.location().url
    if (/status of 401/.test(msg.text()) && /\/api\/v1\/auth\/session$/.test(url || '')) return
    report.errors.push(`${msg.text()} (${url})`)
  })
  page.on('response', (r) => {
    if (new URL(r.url()).origin === base) report.responses.push({ url: r.url(), status: r.status() })
  })
  const login = async () => {
    await page.goto(`${base}/login`, { waitUntil: 'networkidle' })
    await page.locator('input').first().fill(process.env.PANEL_ADMIN_USER || 'admin')
    await page.locator('input[type="password"]').first().fill(password)
    await page.getByRole('button', { name: /登录/ }).first().click()
    await page.waitForURL('**/admin/overview')
    report.checks.push('real browser form login')
  }
  if (phase !== 'upgrade') {
    const anonymous = await context.request.get(`${base}/api/v1/admin/nodes`)
    assert.equal(anonymous.status(), 401, 'anonymous protected API allowed')
    await page.goto(`${base}/admin/nodes`, { waitUntil: 'networkidle' })
    assert.equal(new URL(page.url()).pathname, '/login', 'anonymous deep route unprotected')
    report.checks.push('anonymous API and route protection')
    await login()
  }
  await page.goto(`${base}/admin/nodes`, { waitUntil: 'networkidle' })
  assert.equal(new URL(page.url()).pathname, '/admin/nodes', 'deep route lost authenticated session')
  assert((await page.locator('main').innerText()).length > 20, 'nodes page did not render')
  await page.reload({ waitUntil: 'networkidle' })
  assert.equal(new URL(page.url()).pathname, '/admin/nodes', 'SPA reload failed')
  report.checks.push(phase === 'upgrade' ? 'persisted session after candidate replacement and deep refresh' : 'deep navigation and refresh')
  const session = await context.request.get(`${base}/api/v1/auth/session`)
  assert.equal(session.status(), 200)
  assert.equal((await session.json()).data.user.username, process.env.PANEL_ADMIN_USER || 'admin')
  assert.equal((await context.request.get(`${base}/api/v1/admin/nodes`)).status(), 200)
  const cookies = await context.cookies(base)
  assert(cookies.some((c) => c.name === 'pp_session' && c.secure && c.httpOnly), 'Secure HttpOnly session cookie absent')
  report.checks.push('authenticated API and Secure HttpOnly cookie')
  for (const path of ['/assets/missing-smoke.js', '/api/unknown', '/sub/unknown']) {
    const response = await context.request.get(`${base}${path}`)
    assert.equal(response.status(), path === '/sub/unknown' ? 403 : 404, path)
    const body = await response.text()
    assert(!(path.startsWith('/assets/') ? /id="app"/i : /<html|id="app"/i).test(body), `${path} returned SPA HTML`)
  }
  report.checks.push('missing asset/unknown API non-HTML 404; invalid subscription non-HTML 403')
  const script = await page.locator('script[type="module"][src]').first().getAttribute('src')
  const asset = await context.request.get(new URL(script, base).href)
  assert.equal(asset.status(), 200)
  report.asset = { path: script, sha256: createHash('sha256').update(await asset.body()).digest('hex') }
  if (output) await page.screenshot({ path: `${output}/authenticated-deep-refresh.png`, fullPage: true })
  await page.getByRole('button', { name: '退出登录', exact: true }).click()
  await page.waitForURL('**/login')
  assert.equal((await context.request.get(`${base}/api/v1/admin/nodes`)).status(), 401)
  assert.equal((await context.request.get(`${base}/api/v1/auth/session`)).status(), 401)
  report.checks.push('browser logout then protected API/session reject')
  // Leave a genuine browser-created session for the upgrade phase.
  if (phase === 'current' && state) {
    await login()
    await context.storageState({ path: state })
  }
  assert.deepEqual(report.errors, [], 'unexpected browser errors')
  report.status = 'passed'
  console.log('HOST_NGINX_BROWSER_VERIFIED', JSON.stringify(report))
} catch (error) {
  report.failure = error.stack
  if (output && context) await context.pages()[0]?.screenshot({ path: `${output}/failure.png`, fullPage: true }).catch(() => {})
  throw error
} finally {
  if (output) {
    await writeFile(`${output}/report.json`, JSON.stringify(report, null, 2), { mode: 0o600 })
    await context?.tracing.stop({ path: `${output}/trace.zip` })
  }
  await browser.close()
}
