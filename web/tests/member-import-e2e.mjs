// Focused real-browser regression for the node/member simplification:
//   1. a REALITY node saved with an empty server key gets a generated pair, and
//      client_settings.public_key is derived from the private key (43 chars)
//   2. the member import UI advertises exactly Mihomo (Clash) and Xray
//   3. the server still renders the formats that are only hidden in the UI
//
// Run through the shared fixture so it uses its own Postgres, a real Agent and
// the official cores:
//   WEBDEMO_RUNNER=$PWD/web/tests/member-import-e2e.mjs \
//     WEBDEMO_EVIDENCE_DIR=... bash web/tests/webdemo-env.sh
import { chromium } from '@playwright/test'
import { readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import assert from 'node:assert/strict'

const runDir = process.env.WEBDEMO_RUN_DIR
const evidenceDir = process.env.WEBDEMO_EVIDENCE_DIR
const state = JSON.parse(await readFile(join(runDir, 'state.json'), 'utf8'))
const base = state.base
const results = []
const rec = (id, fn) => (async () => {
  try { results.push({ id, status: 'PASS', detail: await fn() }); console.log(`PASS ${id}`) }
  catch (e) { results.push({ id, status: 'FAIL', error: String(e.message).slice(0, 400) }); console.log(`FAIL ${id}: ${String(e.message).slice(0, 200)}`) }
})()

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ ignoreHTTPSErrors: true })
const page = await ctx.newPage()

await page.goto(base + '/login')
await page.getByLabel('用户名', { exact: true }).fill('admin')
await page.getByLabel('密码', { exact: true }).fill(state.admin_password)
await page.getByRole('button', { name: '登录', exact: true }).click()
await page.waitForURL(u => !u.pathname.startsWith('/login'))

const sess = await (await ctx.request.get(`${base}/api/v1/auth/session`)).json()
const H = { 'X-CSRF-Token': sess.data.csrf_token }

await page.goto(base + '/admin/servers')
await page.getByTestId('server-create-button').click()
await page.locator('#server-name').fill('derive-e2e server')
await page.locator('#server-address').fill('127.0.0.1')
const srvResp = page.waitForResponse(r => r.url().endsWith('/api/v1/admin/servers') && r.request().method() === 'POST')
await page.getByRole('dialog').getByRole('button', { name: '保存', exact: true }).click()
await srvResp

await rec('reality-node-saves-with-empty-server-key', async () => {
  await page.goto(base + '/admin/nodes/new')
  await page.locator('#node-name').fill('derive reality')
  await page.locator('#node-server').selectOption({ label: 'derive-e2e server (127.0.0.1)' })
  await page.locator('#node-host').fill('127.0.0.1')
  await page.locator('#node-port').fill('20443')
  await page.locator('#node-listen').fill('127.0.0.1')
  await page.getByRole('button', { name: '展开', exact: true }).click()
  await page.getByRole('button', { name: /VLESS \+ REALITY/ }).first().click()
  await page.waitForTimeout(800)
  // Explicitly clear the key so the auto-generation path is what gets exercised.
  await page.locator('#node-server-key').fill('')
  await page.getByTestId('node-save-button').click()
  await page.waitForTimeout(1500)
  const alertText = await page.locator('[role="alert"], .text-destructive').first().innerText().catch(() => '')
  const body = await page.locator('main').innerText()
  assert.ok(/已保存|正在同步/.test(body), `node not saved; alert=${alertText.slice(0, 200)}`)
  return { saved: true }
})

await rec('public-key-derived-from-private', async () => {
  const res = await (await ctx.request.get(`${base}/api/v1/admin/nodes?limit=50`)).json()
  const node = (res.items ?? []).find(n => n.name === 'derive reality')
  assert.ok(node, 'created node not listed')
  assert.equal(node.server_key_set, true, 'server key was not stored')
  const pub = node.client_settings?.public_key ?? node.client_settings?.publicKey
  assert.ok(pub, `client public key missing: ${JSON.stringify(node.client_settings)}`)
  // A raw X25519 public key is 32 bytes -> 43 base64url chars, no padding.
  assert.equal(pub.length, 43, `public key length ${pub.length} != 43`)
  assert.doesNotMatch(pub, /[+/=]/, 'public key is not base64url')
  return { serverKeySet: node.server_key_set, publicKeyLength: pub.length }
})

await rec('member-sees-only-mihomo-and-xray', async () => {
  const nodeRes = await (await ctx.request.get(`${base}/api/v1/admin/nodes?limit=50`)).json()
  const node = (nodeRes.items ?? []).find(n => n.name === 'derive reality')
  assert.ok(node, 'node missing for plan')
  const grp = await (await ctx.request.post(`${base}/api/v1/admin/node-groups`, {
    data: { name: 'derive group', node_ids: [node.id] }, headers: H,
  })).json()
  assert.ok(grp.data?.id, `node group failed: ${JSON.stringify(grp).slice(0, 200)}`)
  const sub = await (await ctx.request.post(`${base}/api/v1/admin/subscriptions`, {
    data: {
      name: 'derive plan', node_group_ids: [grp.data.id], quota_bytes: 1073741824,
      timezone: 'UTC', anchor_day: 1, allow_new_bindings: true, access_enabled: true,
    }, headers: H,
  })).json()
  assert.ok(sub.data?.id, `subscription failed: ${JSON.stringify(sub).slice(0, 200)}`)
  const invBody = await (await ctx.request.post(`${base}/api/v1/admin/invitations`, {
    data: { subscription_id: sub.data.id }, headers: H,
  })).json()
  assert.ok(invBody.data?.code, `invitation failed: ${JSON.stringify(invBody).slice(0, 200)}`)

  const mctx = await browser.newContext({ ignoreHTTPSErrors: true })
  const mp = await mctx.newPage()
  await mp.goto(base + '/redeem')
  await mp.locator('#redeem-code').fill(invBody.data.code)
  await mp.locator('#redeem-username').fill('derive_member')
  await mp.locator('#redeem-password').fill('derive-member-pass-1')
  await mp.locator('#redeem-confirm').fill('derive-member-pass-1')
  await mp.getByRole('button', { name: '兑换并登录', exact: true }).click()
  await mp.waitForURL(u => !u.pathname.startsWith('/redeem'))
  await mp.goto(base + '/me/connect')
  await mp.getByTestId('client-import-select').waitFor()
  const options = await mp.getByTestId('client-import-select').locator('option').allTextContents()
  assert.equal(options.length, 2, `expected exactly 2 clients, got ${JSON.stringify(options)}`)
  assert.match(options.join('|'), /Clash|Mihomo/i)
  assert.match(options.join('|'), /Xray/)
  assert.doesNotMatch(options.join('|'), /v2rayN|sing-box/i)

  // Hiding v2rayN/sing-box is a UI decision only: /sub must still serve them so
  // links that were handed out earlier keep working. Prove it with the real
  // subscription endpoint rather than by inspecting the UI list.
  const me = await (await mp.request.get(`${base}/api/v1/me/subscription`)).json()
  assert.ok(me.data?.token && me.data?.url, `member subscription unavailable: ${JSON.stringify(me).slice(0, 200)}`)
  const served = {}
  for (const format of ['xray', 'mihomo', 'v2rayn', 'singbox']) {
    const rendered = me.data.url.replace('{token}', encodeURIComponent(me.data.token))
    // The API may return either an absolute URL or a panel-relative path.
    const url = new URL(/^https?:/i.test(rendered) ? rendered : `${base}${rendered}`)
    url.searchParams.set('format', format)
    const res = await mp.request.get(url.toString())
    assert.equal(res.status(), 200, `${format} subscription returned ${res.status()}`)
    const body = await res.text()
    assert.ok(body.length > 20, `${format} subscription body too short (${body.length})`)
    served[format] = { status: res.status(), bytes: body.length }
  }
  await mctx.close()
  return { options, served }
})

await browser.close()
const failed = results.filter(r => r.status === 'FAIL')
const report = { results, failed: failed.length }
if (evidenceDir) await writeFile(join(evidenceDir, 'derive-report.json'), JSON.stringify(report, null, 2))
console.log(JSON.stringify(report, null, 2))
if (failed.length) process.exitCode = 1
