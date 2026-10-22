// Real-browser deployment smoke test for both Go-SPA and nginx-static modes.
// PANEL_BASE=https://panel.example.com PANEL_ADMIN_PASSWORD=... node tests/host-nginx-smoke.mjs
// For a disposable self-signed fixture only: PANEL_SMOKE_SELF_SIGNED=1.
import assert from 'node:assert/strict'
import { chromium } from '@playwright/test'

const base = process.env.PANEL_BASE?.replace(/\/$/, '')
const password = process.env.PANEL_ADMIN_PASSWORD
assert(base?.startsWith('https://'), 'PANEL_BASE must be an HTTPS origin')
assert(password, 'PANEL_ADMIN_PASSWORD is required')
const browser = await chromium.launch({ headless: true })
try {
  const context = await browser.newContext({
    ignoreHTTPSErrors: process.env.PANEL_SMOKE_SELF_SIGNED === '1',
  })
  const page = await context.newPage()
  const errors = []
  page.on('pageerror', (error) => errors.push(error.message))
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    const url = msg.location().url
    if (/status of 401/.test(msg.text()) && /\/api\/v1\/auth\/session$/.test(url || '')) return
    errors.push(`${msg.text()} (${url})`)
  })
  await page.goto(`${base}/login`, { waitUntil: 'networkidle' })
  await page.locator('input').first().fill(process.env.PANEL_ADMIN_USER || 'admin')
  await page.locator('input[type="password"]').first().fill(password)
  await page.getByRole('button', { name: /登录/ }).first().click()
  await page.waitForURL('**/admin/overview')
  await page.goto(`${base}/admin/nodes`, { waitUntil: 'networkidle' })
  assert(new URL(page.url()).pathname === '/admin/nodes', 'deep route lost authenticated session')
  assert((await page.locator('main').innerText()).length > 20, 'nodes page did not render')
  await page.reload({ waitUntil: 'networkidle' })
  assert(new URL(page.url()).pathname === '/admin/nodes', 'SPA reload failed')
  const session = await context.request.get(`${base}/api/v1/auth/session`)
  assert.equal(session.status(), 200)
  assert.equal((await session.json()).data.user.username, process.env.PANEL_ADMIN_USER || 'admin')
  const cookies = await context.cookies(base)
  assert(cookies.some((c) => c.name === 'pp_session' && c.secure && c.httpOnly), 'Secure HttpOnly session cookie absent')
  const missing = await context.request.get(`${base}/assets/missing-smoke.js`)
  assert.equal(missing.status(), 404)
  assert.deepEqual(errors, [], 'unexpected browser errors')
  console.log('HOST_NGINX_BROWSER_VERIFIED')
} finally {
  await browser.close()
}
