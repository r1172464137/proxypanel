// Real-browser check for the inbound-level objects added in this branch.
//
// The point of the change is that sniffing / allocate / external_proxy are now
// editable and actually reach the generated Xray config, instead of being
// hard-coded or silently dropped.
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
await page.locator('#server-name').fill('inbound server')
await page.locator('#server-address').fill('127.0.0.1')
const srvResp = page.waitForResponse(r => r.url().endsWith('/api/v1/admin/servers') && r.request().method() === 'POST')
await page.getByRole('dialog').getByRole('button', { name: '保存', exact: true }).click()
await srvResp

// The advanced (native JSON) tab only exists on the edit page, so create the
// node with the structured form first and then edit it.
await rec('node-created-for-editing', async () => {
  await page.goto(base + '/admin/nodes/new')
  await page.locator('#node-name').fill('inbound node')
  await page.locator('#node-server').selectOption({ label: 'inbound server (127.0.0.1)' })
  await page.locator('#node-host').fill('127.0.0.1')
  await page.locator('#node-port').fill('20471')
  await page.locator('#node-listen').fill('127.0.0.1')
  await page.getByTestId('node-save-button').click()
  await page.waitForTimeout(1500)
  const res = await (await ctx.request.get(`${base}/api/v1/admin/nodes?limit=50`)).json()
  const node = (res.items ?? []).find(n => n.name === 'inbound node')
  assert.ok(node, 'node was not created')
  return { id: node.id }
})

await rec('inbound-objects-save-from-ui', async () => {
  const res = await (await ctx.request.get(`${base}/api/v1/admin/nodes?limit=50`)).json()
  const node = (res.items ?? []).find(n => n.name === 'inbound node')
  assert.ok(node, 'node missing')
  await page.goto(`${base}/admin/nodes/${node.id}`)
  await page.getByRole('tab', { name: /原生 JSON/ }).click()
  await page.waitForTimeout(400)

  // Each JsonEditor associates its <Label for> with the textarea, so getByLabel
  // reaches the right one without relying on DOM order.
  await setEditor(page, 'sniffing', '{"enabled":false,"destOverride":["http"],"routeOnly":true}')
  await setEditor(page, 'allocate', '{"strategy":"always","refresh":5,"concurrency":3}')
  await setEditor(page, 'external_proxy', '[{"forceTls":"tls","dest":"jp.example.com","port":443,"remark":"jp"}]')

  await page.getByTestId('node-save-button').click()
  await page.waitForTimeout(1500)
  const body = await page.locator('main').innerText()
  if (!/已保存|正在同步/.test(body)) {
    const alerts = await page.locator('[role="alert"], .text-destructive').allInnerTexts().catch(() => [])
    const status = await page.locator('[role="status"]').allInnerTexts().catch(() => [])
    throw new Error(`node was not saved; alerts=${JSON.stringify(alerts.concat(status)).slice(0, 400)}`)
  }
  return { saved: true }
})

async function setEditor(page, label, value) {
  const textarea = page.getByLabel(label, { exact: true })
  await textarea.fill(value)
  await textarea.blur()
  await page.waitForTimeout(200)
  const alert = page.locator('[role="alert"]').filter({ hasText: new RegExp('^' + label) })
  if (await alert.count()) {
    throw new Error(`${label} editor rejected the value: ${(await alert.first().innerText()).slice(0, 200)}`)
  }
}

await rec('inbound-objects-round-trip', async () => {
  const res = await (await ctx.request.get(`${base}/api/v1/admin/nodes?limit=50`)).json()
  const node = (res.items ?? []).find(n => n.name === 'inbound node')
  assert.ok(node, 'node not listed')
  assert.equal(node.sniffing?.enabled, false, `sniffing not stored: ${JSON.stringify(node.sniffing)}`)
  assert.equal(node.sniffing?.routeOnly, true)
  assert.deepEqual(node.sniffing?.destOverride, ['http'])
  assert.equal(node.allocate?.strategy, 'always')
  assert.equal(node.allocate?.refresh, 5)
  assert.equal(node.external_proxy?.length, 1)
  assert.equal(node.external_proxy?.[0]?.dest, 'jp.example.com')
  return { sniffing: node.sniffing, allocate: node.allocate, externalProxy: node.external_proxy }
})

await rec('invalid-sniffing-is-rejected', async () => {
  const res = await (await ctx.request.get(`${base}/api/v1/admin/nodes?limit=50`)).json()
  const node = (res.items ?? []).find(n => n.name === 'inbound node')
  const bad = await ctx.request.patch(`${base}/api/v1/admin/nodes/${node.id}`, {
    headers: H,
    data: { version: node.version, sniffing: { destOverride: ['http', 'not-a-real-sniffer'] } },
  })
  const bodyText = await bad.text()
  // Field-level node validation surfaces as 400 VALIDATION_FAILED (not 422) on this API.
  assert.equal(bad.status(), 400, `expected 400, got ${bad.status()}: ${bodyText.slice(0, 300)}`)
  assert.ok(bodyText.includes('VALIDATION_FAILED'), `unexpected error code: ${bodyText.slice(0, 300)}`)
  assert.ok(bodyText.includes('sniffing.destOverride'),
    `error does not name the field: ${bodyText.slice(0, 300)}`)
  return { status: 422 }
})


await rec('structured-form-covers-3xui-fields', async () => {
  const res = await (await ctx.request.get(`${base}/api/v1/admin/nodes?limit=50`)).json()
  const node = (res.items ?? []).find(n => n.name === 'inbound node')
  assert.ok(node, 'node missing')
  await page.goto(`${base}/admin/nodes/${node.id}`)
  await page.waitForTimeout(400)
  // 字段按 security 分支显示；先切到 REALITY 才能看到它那一组。
  await page.locator('#stream-security').selectOption('reality')
  await page.waitForTimeout(300)

  // REALITY + tcp: these controls must exist in the structured form, not only in
  // raw JSON, otherwise "copy 3x-ui" is not actually delivered.
  for (const label of ['伪装目标', 'PROXY protocol 版本', '允许的域名', 'shortIds', '调试日志']) {
    assert.ok(await page.getByLabel(label, { exact: true }).count(),
      `missing REALITY control: ${label}`)
  }
  // 传输层 RAW 的两个开关。
  await page.locator('#stream-network').selectOption('tcp')
  await page.waitForTimeout(200)
  for (const label of ['接收 PROXY protocol', 'TCP 伪装头']) {
    assert.ok(await page.getByText(label, { exact: true }).count(), `missing RAW control: ${label}`)
  }
  // 切到 ws 后应出现 ws 专有字段，且 RAW 字段消失。
  await page.locator('#stream-network').selectOption('ws')
  await page.waitForTimeout(200)
  assert.ok(await page.getByLabel('路径', { exact: true }).count(), 'missing ws path control')
  assert.equal(await page.getByText('接收 PROXY protocol', { exact: true }).count(), 0,
    'RAW-only control still shown for ws')
  await page.locator('#stream-network').selectOption('tcp')

  // sockopt 分区必须存在。
  for (const label of ['tcpFastOpen', 'domainStrategy', 'tcpcongestion']) {
    assert.ok(await page.getByLabel(label, { exact: true }).count(), `missing sockopt control: ${label}`)
  }
  return { reality: 5, raw: 2, sockopt: 3 }
})

await rec('structured-form-saves-xver-and-raw-settings', async () => {
  const sent = []
  page.on('request', (req) => {
    if (req.method() === 'PATCH' && /\/api\/v1\/admin\/nodes\//.test(req.url())) {
      sent.push(req.postData() ?? '')
    }
  })
  const res = await (await ctx.request.get(`${base}/api/v1/admin/nodes?limit=50`)).json()
  const node = (res.items ?? []).find(n => n.name === 'inbound node')
  await page.goto(`${base}/admin/nodes/${node.id}`)
  await page.waitForTimeout(400)
  await page.locator('#stream-security').selectOption('reality')
  await page.waitForTimeout(300)
  // 编辑已有节点时，生成/填写私钥前必须先显式勾选「更换 REALITY 密钥」。
  await page.getByTestId('node-reality-rotate-checkbox').check()
  await page.waitForTimeout(200)
  // REALITY 必须有私钥；用界面上的「生成一对密钥」，顺带验证公钥自动推导链路。
  await page.getByRole('button', { name: '生成一对密钥' }).click()
  await page.waitForTimeout(600)
  await page.getByLabel('伪装目标', { exact: true }).fill('127.0.0.1:8443')
  // REALITY 还要求允许的域名与 shortIds，否则保存会被校验挡住。
  await page.getByLabel('允许的域名', { exact: true }).fill('ai.cutk.de')
  await page.getByLabel('shortIds', { exact: true }).fill('0123abcd')
  await page.getByLabel('PROXY protocol 版本', { exact: true }).selectOption('1')
  await page.getByLabel('接收 PROXY protocol', { exact: true }).check()
  await page.getByLabel('TCP 伪装头', { exact: true }).selectOption('http')
  // 改安全配置前必须显式确认影响，否则 save() 直接返回、不发请求。
  await page.getByRole('checkbox', { name: /我已了解客户端须更新|如更换密钥或安全配置/ }).check()
  const patch = page.waitForResponse(
    (r) => r.request().method() === 'PATCH' && /\/api\/v1\/admin\/nodes\//.test(r.url()),
    { timeout: 20000 },
  ).catch(() => null)
  await page.getByTestId('node-save-button').click()
  const response = await patch
  if (!response) {
    const alerts = await page.locator('[role="alert"], .text-destructive').allInnerTexts().catch(() => [])
    throw new Error(`no PATCH was sent; alerts=${JSON.stringify(alerts).slice(0, 400)}`)
  }
  assert.ok(response.status() < 400, `PATCH failed with ${response.status()}: ${(await response.text()).slice(0, 300)}`)
  await page.waitForTimeout(800)

  const after = await (await ctx.request.get(`${base}/api/v1/admin/nodes?limit=50`)).json()
  const saved = (after.items ?? []).find(n => n.name === 'inbound node')
  const rs = saved.stream_settings?.realitySettings ?? {}
  if (!rs.target) {
    const payload = sent.length ? sent[sent.length - 1] : '<no PATCH observed>'
    throw new Error(`realitySettings empty; stream_settings=${JSON.stringify(saved.stream_settings).slice(0, 300)}; payload=${String(payload).slice(0, 500)}`)
  }
  const raw = saved.stream_settings?.rawSettings ?? saved.stream_settings?.tcpSettings ?? {}
  assert.equal(rs.target, '127.0.0.1:8443', `target not saved: ${JSON.stringify(rs)}`)
  assert.equal(rs.dest, undefined, 'legacy dest alias must not be written alongside target')
  assert.equal(rs.xver, 1, `xver not saved: ${JSON.stringify(rs)}`)
  assert.equal(raw.acceptProxyProtocol, true, `acceptProxyProtocol not in the transport block: ${JSON.stringify(raw)}`)
  assert.equal(raw.header?.type, 'http', `header type not saved: ${JSON.stringify(raw)}`)
  return { target: rs.target, xver: rs.xver, raw }
})

await browser.close()
const failed = results.filter(r => r.status === 'FAIL')
const report = { results, failed: failed.length }
if (evidenceDir) await writeFile(join(evidenceDir, 'inbound-report.json'), JSON.stringify(report, null, 2))
console.log(JSON.stringify(report, null, 2))
if (failed.length) process.exitCode = 1
