// Real-browser regression for the toast placement.
//
// vue-sonner positions its <ol data-sonner-toaster> purely through its own
// stylesheet. When that import is missing the element falls back to normal
// document flow: it renders at the bottom of the page, visually at the
// bottom-left on top of the sidebar, and gets clipped by the viewport. Nothing
// fails at build time, so this checks the geometry after a real save.
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
const ctx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 } })
const page = await ctx.newPage()

await page.goto(base + '/login')
await page.getByLabel('用户名', { exact: true }).fill('admin')
await page.getByLabel('密码', { exact: true }).fill(state.admin_password)
await page.getByRole('button', { name: '登录', exact: true }).click()
await page.waitForURL(u => !u.pathname.startsWith('/login'))

// Create a server first: saving a node emits the "节点已保存" success toast,
// which is the exact path the placement regression showed up on.
await page.goto(base + '/admin/servers')
await page.getByTestId('server-create-button').click()
await page.locator('#server-name').fill('toast server')
await page.locator('#server-address').fill('127.0.0.1')
const srvResp = page.waitForResponse(r => r.url().endsWith('/api/v1/admin/servers') && r.request().method() === 'POST')
await page.getByRole('dialog').getByRole('button', { name: '保存', exact: true }).click()
await srvResp

await rec('toast-is-fixed-bottom-right-and-fully-visible', async () => {
  await page.goto(base + '/admin/nodes/new')
  await page.locator('#node-name').fill('toast node')
  await page.locator('#node-server').selectOption({ label: 'toast server (127.0.0.1)' })
  await page.locator('#node-host').fill('127.0.0.1')
  await page.locator('#node-port').fill('20444')
  await page.locator('#node-listen').fill('127.0.0.1')
  await page.getByTestId('node-save-button').click()

  const toast = page.locator('[data-sonner-toast]').first()
  await toast.waitFor({ state: 'visible', timeout: 20000 })
  const toaster = page.locator('[data-sonner-toaster]')

  // The structural proof: without vue-sonner/style.css the toaster is `static`
  // and everything lands in normal document flow at the bottom-left.
  const toasterInfo = await toaster.evaluate(el => {
    const cs = getComputedStyle(el)
    const r = el.getBoundingClientRect()
    return {
      position: cs.position,
      dataX: el.getAttribute('data-x-position'),
      dataY: el.getAttribute('data-y-position'),
      rect: { x: r.x, y: r.y, width: r.width, height: r.height },
      vw: window.innerWidth, vh: window.innerHeight,
    }
  })
  assert.equal(toasterInfo.position, 'fixed',
    `toaster is "${toasterInfo.position}", not fixed — vue-sonner style.css is missing`)
  assert.equal(toasterInfo.dataX, 'right')
  assert.equal(toasterInfo.dataY, 'bottom')

  // Geometry is measured on the toast itself: the toaster <ol> intentionally has
  // height 0 because sonner absolutely positions the toasts inside it.
  //
  // sonner animates the enter transition from translateY(100%), so the first
  // frame sits below the viewport edge. Wait for the transition to settle
  // (bounded) instead of measuring mid-animation; a real placement regression
  // never settles inside the viewport.
  const vw = toasterInfo.vw
  const vh = toasterInfo.vh
  let box = null
  const deadline = Date.now() + 5000
  for (;;) {
    box = await toast.boundingBox()
    const settled = box && box.height > 0 && box.y >= 0 && box.y + box.height <= vh
    if (settled || Date.now() > deadline) break
    await page.waitForTimeout(100)
  }
  assert.ok(box, 'toast has no box')
  assert.ok(box.width > 100 && box.height > 20, `toast box collapsed: ${JSON.stringify(box)}`)
  assert.ok(box.x > vw / 2, `toast sits on the left (x=${box.x}, vw=${vw})`)
  assert.ok(box.y + box.height / 2 > vh / 2, `toast sits in the top half (y=${box.y})`)
  // Fully inside the viewport: the clipped/cut-off symptom is gone.
  assert.ok(box.y >= 0 && box.y + box.height <= vh,
    `toast is clipped vertically: y=${box.y} h=${box.height} vh=${vh}`)
  assert.ok(box.x >= 0 && box.x + box.width <= vw,
    `toast is clipped horizontally: x=${box.x} w=${box.width} vw=${vw}`)

  const info = { toaster: toasterInfo, toastBox: box }
  return info
})

await rec('toast-text-is-readable', async () => {
  const toast = page.locator('[data-sonner-toast]').first()
  await toast.waitFor({ state: 'visible', timeout: 10000 })
  const text = (await toast.innerText()).trim()
  assert.ok(text.length > 0, 'toast has no text')
  const box = await toast.boundingBox()
  assert.ok(box && box.width > 100 && box.height > 20, `toast box collapsed: ${JSON.stringify(box)}`)
  return { text: text.slice(0, 80) }
})

await browser.close()
const failed = results.filter(r => r.status === 'FAIL')
const report = { results, failed: failed.length }
if (evidenceDir) await writeFile(join(evidenceDir, 'toast-report.json'), JSON.stringify(report, null, 2))
console.log(JSON.stringify(report, null, 2))
if (failed.length) process.exitCode = 1
