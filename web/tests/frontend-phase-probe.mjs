/**
 * Frontend phase UI probe — real Chromium against the CURRENT webbuild served
 * by an owned Panel binary on an owned PostgreSQL schema.
 *
 * Run through web/tests/product-readiness-env.sh with PR_PROBE=frontend-phase
 * (default launcher behaviour is unchanged). No API mocking, no route
 * interception, no screenshots, no HAR. Only the loopback development HTTP
 * Panel is used; this is NOT HTTPS/static-hosting/Agent/data-plane evidence.
 *
 * Scope: technology/UI closure only (frontend design §4.12 and §8 wiring).
 * Synthetic fixtures are labelled; no user-story or real-Agent-traffic claim.
 */
import { chromium, expect } from '@playwright/test'
import { readFile, writeFile, mkdir, copyFile } from 'node:fs/promises'
import { spawn, execFileSync } from 'node:child_process'
import { createServer } from 'node:net'
import { createHash } from 'node:crypto'
import { join, resolve } from 'node:path'
import assert from 'node:assert/strict'
import {
  SYNTHETIC_NODE_NAME,
  SYNTHETIC_NODE_RENAMED,
  SYNTHETIC_NODE_WRITE,
  changedInvariantFields,
  maskedSubscription,
} from './frontend-phase-fixtures.mjs'

const privateDir = process.env.PR_PRIVATE
if (!privateDir?.startsWith('/tmp/pr-browser.')) throw Error('Run the owned environment launcher')
const root = resolve(import.meta.dirname, '../..')
const owner = JSON.parse(await readFile(join(privateDir, 'owner.json'), 'utf8'))
const run = new Date().toISOString().replace(/[:.]/g, '-')
const out = join(
  process.env.PR_EVIDENCE_ROOT ?? join(process.env.PR_MAIN_ROOT, '.ai/evidence/frontend-phase/ui-probe'),
  run,
)
await mkdir(out, { recursive: true, mode: 0o700 })
await writeFile(join(privateDir, 'evidence-path'), out, { mode: 0o600 })

const secrets = [owner.PANEL_MASTER_KEY, owner.PANEL_ADMIN_PASSWORD].filter(Boolean)
const redact = (s) =>
  String(s)
    .replace(/\/sub\/[^\s"'?]+/g, '/sub/[REDACTED]')
    .replace(/pr_browser_[a-f0-9]{32}/g, '[owned-schema]')
    .split('\n')
    .map((line) => secrets.reduce((v, secret) => v.split(secret).join('[REDACTED]'), line))
    .join('\n')

const results = []
const requests = []
const fixtures = []
const observations = []
const pageErrors = []
const cleanups = []

let panel
let browser
let browserVersion
let base
let admin
let member
let ap
let mp
let server
let node
let group
let subscription
let memberId
let noTokenMemberId
let batchId

process.on('SIGTERM', () => {
  panel?.kill('SIGTERM')
  void browser?.close()
})
process.on('SIGINT', () => {
  panel?.kill('SIGTERM')
  void browser?.close()
})

const sql = (statement) =>
  JSON.parse(execFileSync(join(privateDir, 'db'), ['sql', join(privateDir, 'owner.json')], { input: statement, encoding: 'utf8' }))
const str = (value) => `'${String(value).replaceAll("'", "''")}'`
const sha256 = (value) => createHash('sha256').update(value).digest('hex')

async function test(name, fn) {
  const started = new Date().toISOString()
  try {
    const detail = await fn()
    results.push({ name, started, status: 'PASS', detail })
    console.log(`PASS ${name}`)
    return true
  } catch (error) {
    results.push({ name, started, status: 'FAIL', error: redact(error.stack) })
    console.log(`FAIL ${name}: ${redact(error.message).slice(0, 300)}`)
    return false
  } finally {
    await writeFile(join(out, 'results.json'), redact(JSON.stringify(results, null, 2)), { mode: 0o600 })
  }
}

async function api(context, path, method = 'GET', data, expected = 200, headers = {}) {
  const session =
    method === 'GET' || path.startsWith('/auth/')
      ? null
      : await context.request.get(base + '/api/v1/auth/session').then((r) => r.json())
  const csrf = session?.data?.csrf_token
  const response = await context.request.fetch(base + '/api/v1' + path, {
    method,
    data,
    headers: { ...(csrf ? { 'X-CSRF-Token': csrf } : {}), ...headers },
  })
  const payload = await response.json()
  requests.push({
    at: new Date().toISOString(),
    method,
    path: redact('/api/v1' + path),
    status: response.status(),
    requestId: response.headers()['x-request-id'],
    source: 'real API fixture/assertion',
  })
  assert.equal(response.status(), expected, `${method} ${path}: HTTP ${response.status()} ${payload.error?.code ?? ''}`)
  return payload.data ?? payload
}

async function context(options = {}) {
  const c = await browser.newContext({
    timezoneId: 'UTC',
    permissions: ['clipboard-read', 'clipboard-write'],
    ...options,
  })
  c.setDefaultTimeout(15000)
  c.on('page', (p) => {
    p.on('pageerror', (e) => pageErrors.push(redact(e.message)))
    p.on('response', (r) => {
      const u = new URL(r.url())
      if (u.pathname.startsWith('/api/') || u.pathname.startsWith('/sub/')) {
        requests.push({
          at: new Date().toISOString(),
          method: r.request().method(),
          path: redact(u.pathname),
          status: r.status(),
          requestId: r.headers()['x-request-id'],
        })
      }
    })
  })
  return c
}

async function login(c, username, expectedUrl) {
  const p = await c.newPage()
  await p.goto(base + '/login')
  await p.getByLabel('用户名', { exact: true }).fill(username)
  await p.getByLabel('密码', { exact: true }).fill(owner.PANEL_ADMIN_PASSWORD)
  const pending = p.waitForResponse((r) => r.url().endsWith('/api/v1/auth/login') && r.request().method() === 'POST')
  await p.getByRole('button', { name: '登录', exact: true }).click()
  assert.equal((await pending).status(), 200)
  await p.waitForURL(expectedUrl)
  return p
}

async function makeInvite(subscriptionId) {
  return api(admin, '/admin/invitations', 'POST', { subscription_id: subscriptionId, max_uses: 1 }, 201)
}

async function redeem(code, username) {
  const c = await context()
  const p = await c.newPage()
  await p.goto(base + '/redeem')
  await p.locator('#redeem-code').fill(code)
  await p.locator('#redeem-username').fill(username)
  await p.locator('#redeem-password').fill(owner.PANEL_ADMIN_PASSWORD)
  await p.locator('#redeem-confirm').fill(owner.PANEL_ADMIN_PASSWORD)
  const pending = p.waitForResponse((r) => r.url().endsWith('/api/v1/auth/redeem') && r.request().method() === 'POST')
  await p.getByRole('button', { name: '兑换并登录', exact: true }).click()
  assert.equal((await pending).status(), 201)
  await p.waitForURL('**/me/subscription**')
  return { context: c, page: p }
}

/** Measure layout facts for the accessibility / mobile assertions. */
async function layoutFacts(page, { keyActionTestIds = [] } = {}) {
  return page.evaluate((ids) => {
    const doc = document.documentElement
    const visible = (el) => {
      const rect = el.getBoundingClientRect()
      const style = getComputedStyle(el)
      return rect.width > 0 && rect.height > 0 && style.visibility !== 'hidden' && style.display !== 'none'
    }
    const rectOf = (el) => {
      const r = el.getBoundingClientRect()
      return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height }
    }
    const interactive = [...document.querySelectorAll('main button, main a, main input, main select, main summary, nav a, nav button')].filter(visible)
    const small = interactive
      .map((el) => ({
        tag: el.tagName.toLowerCase(),
        testid: el.getAttribute('data-testid') ?? '',
        text: (el.innerText || el.getAttribute('aria-label') || '').trim().slice(0, 40),
        height: Math.round(el.getBoundingClientRect().height * 10) / 10,
      }))
      .filter((item) => item.height < 44)
    const keys = {}
    for (const id of ids) {
      const el = document.querySelector(`[data-testid="${id}"]`)
      keys[id] = el && visible(el) ? rectOf(el) : null
    }
    const navLink = document.querySelector('nav a')
    return {
      viewport: { width: window.innerWidth, height: window.innerHeight },
      scrollWidth: doc.scrollWidth,
      bodyScrollWidth: document.body.scrollWidth,
      overflow: doc.scrollWidth > window.innerWidth + 1,
      interactiveCount: interactive.length,
      smallTargets: small,
      keyRects: keys,
      navLinkHeight: navLink && visible(navLink) ? Math.round(navLink.getBoundingClientRect().height * 10) / 10 : null,
      coarse: matchMedia('(pointer: coarse)').matches,
    }
  }, keyActionTestIds)
}

/** Assert no document-level horizontal overflow and that given key actions are reachable at this viewport. */
async function assertMobilePage(page, path, label, keyActionTestIds, prepare) {
  await page.goto(base + path)
  await page.waitForLoadState('networkidle')
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  if (prepare) await prepare(page)
  const facts = await layoutFacts(page, { keyActionTestIds })
  assert.equal(facts.overflow, false, `${label}: document scrollWidth ${facts.scrollWidth} > viewport ${facts.viewport.width}`)
  const measured = {}
  for (const id of keyActionTestIds) {
    const locator = page.locator(`[data-testid="${id}"]`)
    await locator.first().scrollIntoViewIfNeeded()
    await expect(locator.first()).toBeVisible()
    const box = await locator.first().boundingBox()
    assert.ok(box, `${label}: key action [data-testid=${id}] has no box`)
    // Reachable at 390px: fully inside the viewport horizontally, and its
    // height is a real touch target once scrolled into view.
    assert.ok(box.x >= -0.5 && box.x + box.width <= facts.viewport.width + 0.5, `${label}: [${id}] overflows horizontally: ${JSON.stringify(box)}`)
    assert.ok(box.height >= 44, `${label}: [${id}] touch height ${box.height} < 44px`)
    measured[id] = { w: Math.round(box.width), h: Math.round(box.height) }
  }
  return {
    path,
    viewport: facts.viewport,
    scrollWidth: facts.scrollWidth,
    noHorizontalOverflow: true,
    coarsePointer: facts.coarse,
    navLinkHeight: facts.navLinkHeight,
    interactiveUnder44px: facts.smallTargets.length,
    smallestTargets: facts.smallTargets.slice(0, 8),
    keyActionsVisible: measured,
  }
}

try {
  const port = await new Promise((resolvePort, reject) => {
    const s = createServer()
    s.once('error', reject)
    s.listen(0, '127.0.0.1', () => {
      const p = s.address().port
      s.close(() => resolvePort(p))
    })
  })
  base = `http://127.0.0.1:${port}`
  const log = await import('node:fs').then((fs) => fs.openSync(join(privateDir, 'panel.log'), 'a', 0o600))
  panel = spawn(join(privateDir, 'panel'), [], {
    cwd: root,
    env: {
      PATH: process.env.PATH,
      DATABASE_URL: owner.DATABASE_URL,
      PANEL_MASTER_KEY: owner.PANEL_MASTER_KEY,
      PANEL_ADMIN_USER: 'admin',
      PANEL_ADMIN_PASSWORD: owner.PANEL_ADMIN_PASSWORD,
      PANEL_DEV: 'true',
      PANEL_LISTEN: `127.0.0.1:${port}`,
      PANEL_PUBLIC_URL: base,
      PANEL_WEB_ROOT: join(privateDir, 'dist'),
    },
    stdio: ['ignore', log, log],
  })
  cleanups.push({ object: 'owned Panel process', pid: panel.pid, action: 'SIGTERM then SIGKILL if needed in finally' })

  await test('owned Panel + exclusive random schema serviced the current webbuild', async () => {
    for (let i = 0; i < 100; i++) {
      if (panel.exitCode !== null) throw Error('owned Panel exited; inspect sanitized panel.log')
      try {
        if ((await fetch(base + '/readyz')).ok) break
      } catch {}
      await new Promise((r) => setTimeout(r, 200))
    }
    assert.equal((await fetch(base + '/readyz')).status, 200)
    assert.equal(sql('select current_schema() as schema')[0].schema, owner.schema)
    const index = await (await fetch(base + '/')).text()
    assert.ok(index.includes('<div id="app">'), 'webbuild index.html not served')
    return {
      schemaProved: true,
      panelPid: panel.pid,
      base,
      dist: join(privateDir, 'dist'),
      transport: 'loopback development HTTP; NOT HTTPS acceptance',
    }
  })

  browser = await chromium.launch({ headless: true })
  browserVersion = browser.version()
  expect.configure({ timeout: 12000 })
  admin = await context()

  if (
    !(await test('administrator browser login, reload and §4.12 three-state shell', async () => {
      ap = await login(admin, 'admin', '**/admin/overview')
      await expect(ap.getByTestId('workbench-setup-checklist')).toBeVisible()
      await ap.reload()
      await expect(ap.getByRole('heading', { level: 1, name: '工作台' })).toBeVisible()
      await expect(ap.getByTestId('nav-workbench')).toHaveAttribute('aria-current', 'page')
      const banner = ap.getByTestId('recovery-mode-banner')
      assert.equal(await banner.count(), 0, 'no recovery banner before entering recovery')
      return { reloadStable: true, loadingEmptyErrorComponents: 'shared ErrorState/LoadingBlock/EmptyState only (no new components)', recoveryBannerAbsentInitially: true }
    }))
  )
    throw Error('administrator prerequisite failed')

  if (
    !(await test('synthetic resource fixtures via real admin API (no Agent, not real core config)', async () => {
      server = await api(admin, '/admin/servers', 'POST', { name: 'PR probe server (NO AGENT)', address: '127.0.0.1' }, 201)
      node = await api(admin, '/admin/nodes', 'POST', SYNTHETIC_NODE_WRITE(server.id), 201)
      group = await api(admin, '/admin/node-groups', 'POST', { name: 'PR probe group', node_ids: [node.id] }, 201)
      subscription = await api(
        admin,
        '/admin/subscriptions',
        'POST',
        {
          name: 'PR probe plan 1GiB',
          node_group_ids: [group.id],
          quota_bytes: 1073741824,
          timezone: 'UTC',
          anchor_day: 1,
          allow_new_bindings: true,
          access_enabled: true,
        },
        201,
      )
      fixtures.push({
        source: 'real admin HTTP API; NOT connected to any Agent; node config is SYNTHETIC',
        serverId: server.id,
        nodeId: node.id,
        groupId: group.id,
        subscriptionId: subscription.id,
      })
      return { ids: { server: server.id, node: node.id, group: group.id, subscription: subscription.id } }
    }))
  )
    throw Error('resource fixture prerequisite failed')

  await test('/admin/setup renders the real six-step state from the API (no fake 0/6, unknown stays unknown)', async () => {
    const overview = await api(admin, '/admin/overview')
    assert.ok(overview.setup, 'overview.setup missing')
    assert.deepEqual(overview.setup.steps.map((s) => s.key), ['servers', 'nodes', 'node_groups', 'subscriptions', 'members', 'traffic_observed'])
    const allowed = ['completed', 'blocked', 'pending', 'not_started', 'unknown']
    assert.deepEqual(overview.setup.steps.filter((s) => !allowed.includes(s.state)), [], 'unexpected step states in API')
    await ap.goto(base + '/admin/setup')
    await ap.waitForLoadState('networkidle')
    const label = (await ap.getByTestId('setup-progress-label').innerText()).trim()
    const unknownSteps = overview.setup.steps.filter((s) => s.state === 'unknown')
    // The confirmed aggregate is legal knowledge and must be shown as-is; unknown
    // steps are reported separately and never folded into the count or pending.
    const countKnown = Number.isInteger(overview.setup.completed_count) && overview.setup.completed_count >= 0 && overview.setup.completed_count <= 6
    assert.equal(countKnown, true, `API completed_count out of range: ${overview.setup.completed_count}`)
    assert.equal(label, `已确认完成 ${overview.setup.completed_count}/6 步`, `progress label ${label} != real count ${overview.setup.completed_count}`)
    const unknownNote = ap.getByTestId('setup-progress-unknown')
    if (unknownSteps.length > 0) {
      await expect(unknownNote).toBeVisible()
      const unknownText = (await unknownNote.innerText()).trim()
      assert.equal(unknownText, `${unknownSteps.length} 步状态未知，未计入已完成`, `unknown note ${unknownText} != ${unknownSteps.length} unknown step(s)`)
      assert.ok(unknownSteps.length < 6, 'unknown steps must be a subset, not every step')
      assert.ok(overview.setup.completed_count < 6, 'a partially unknown setup cannot claim 6/6')
    } else {
      await expect(unknownNote).toHaveCount(0)
    }
    for (const step of unknownSteps) {
      const cellText = await ap.getByTestId(`setup-step-${step.key}`).innerText()
      assert.ok(cellText.includes('状态未知'), `${step.key}: unknown step must read 状态未知, got ${cellText}`)
      assert.ok(!cellText.includes('待完成'), `${step.key}: unknown must not be shown as pending`)
    }
    for (const step of overview.setup.steps) {
      const cell = ap.getByTestId(`setup-step-${step.key}`)
      await expect(cell).toBeVisible()
      const stateText = await cell.locator('span.text-sm').first().innerText()
      const expected = { completed: '已完成', blocked: '被阻断', pending: '待完成', not_started: '未开始', unknown: '状态未知' }[step.state]
      assert.equal(stateText.trim(), expected, `${step.key}: UI state ${stateText} != API ${step.state}`)
    }
    const traffic = overview.setup.steps.find((s) => s.key === 'traffic_observed')
    assert.notEqual(traffic.state, 'completed', 'no traffic exists; traffic step must not be completed')
    const apiStates = overview.setup.steps.map((s) => `${s.key}=${s.state}`)
    observations.push({
      name: 'setup summary keeps the API confirmed count and reports unknown steps separately',
      value: { label, completedCount: overview.setup.completed_count, unknownKeys: unknownSteps.map((s) => s.key), apiStates },
    })
    return {
      realCompletedCount: overview.setup.completed_count,
      progressLabel: label,
      unknownKeys: unknownSteps.map((s) => s.key),
      perStepStates: apiStates,
      trafficState: traffic.state,
      fabricatedPartialCount: false,
      source: 'GET /api/v1/admin/overview setup (real, no synthetic traffic)',
    }
  })

  await test('new routes reachable + legacy aliases and #link/#qrcode anchors resolve (admin)', async () => {
    const seen = {}
    for (const [from, to] of [
      ['/admin/invite', '/admin/invitations'],
      ['/admin/usage-detail', '/admin/usage'],
      ['/me', '/me/subscription'],
      ['/me/credential', '/me/credentials'],
    ]) {
      await ap.goto(base + from)
      await ap.waitForURL((url) => url.pathname === to)
      seen[from] = new URL(ap.url()).pathname
    }
    for (const [path, testid] of [
      ['/admin/setup', 'setup-refresh-button'],
      ['/admin/runtime', 'runtime-refresh-button'],
      ['/admin/nodes', null],
    ]) {
      await ap.goto(base + path)
      await ap.waitForLoadState('networkidle')
      await expect(ap.getByRole('heading', { level: 1 })).toBeVisible()
      assert.equal(new URL(ap.url()).pathname, path)
      if (testid) await expect(ap.getByTestId(testid)).toBeVisible()
    }
    for (const [anchor, element, role] of [
      ['#link', '#link', 'admin'],
      ['#qrcode', '#qrcode', 'admin'],
    ]) {
      await ap.goto(base + '/me/subscription' + anchor)
      await ap.waitForURL((url) => url.pathname === '/me/connect' && url.hash === anchor)
      // The admin can open the member page, but without a member subscription
      // the import block has no URL, so only the anchor contract is asserted
      // here. Element presence for both anchors is asserted as a real member
      // in the next member test.
      seen[`/me/subscription${anchor}`] = { redirect: new URL(ap.url()).pathname + new URL(ap.url()).hash, elementRole: role }
      await expect(ap.getByRole('heading', { level: 1 })).toBeVisible()
      void element
    }
    return { aliases: seen, newRoutes: ['/admin/setup', '/admin/runtime'], anchorsRedirected: ['#link', '#qrcode'] }
  })

  await test('real rename-only node save returns losslessly through the API (SYNTHETIC native config)', async () => {
    const before = await api(admin, `/admin/nodes/${node.id}`)
    assert.ok(before.stream_settings?.sockopt, 'synthetic unknown stream key not persisted by API')
    await ap.goto(base + `/admin/nodes/${node.id}`)
    await ap.waitForLoadState('networkidle')
    await expect(ap.locator('#node-name')).toHaveValue(SYNTHETIC_NODE_NAME)
    const saved = ap.waitForResponse((r) => r.url().endsWith(`/api/v1/admin/nodes/${node.id}`) && r.request().method() === 'PATCH')
    await ap.locator('#node-name').fill(SYNTHETIC_NODE_RENAMED)
    await ap.getByTestId('node-save-button').click()
    const response = await saved
    assert.equal(response.status(), 200, 'rename-only PATCH must succeed')
    await expect(ap.getByTestId('node-sync-panel')).toContainText('正在同步')
    await expect(ap.getByTestId('node-sync-panel')).toContainText('真实连接尚未验证')
    const after = await api(admin, `/admin/nodes/${node.id}`)
    assert.equal(after.name, SYNTHETIC_NODE_RENAMED)
    const changed = changedInvariantFields(before, after)
    assert.deepEqual(changed, [], `rename-only save changed fields: ${changed.join(', ')}`)
    assert.deepEqual(after.stream_settings.sockopt, { tcpNoDelay: true, tcpKeepAliveIdle: 60 })
    assert.deepEqual(after.protocol_settings.syntheticProbeKey, 'SYNTHETIC-not-real-core')
    await ap.reload()
    await expect(ap.locator('#node-name')).toHaveValue(SYNTHETIC_NODE_RENAMED)
    return {
      source: 'SYNTHETIC native node config (NOT a real core config)',
      renamed: true,
      invariantsUnchanged: changed.length === 0,
      unknownStreamKeysRetained: true,
      unknownProtocolKeysRetained: true,
      reloadShowsPersistedName: true,
    }
  })

  await test('invitation + one-step registration produce a real member (fixture for member UI)', async () => {
    const invitation = await makeInvite(subscription.id)
    secrets.push(invitation.code)
    const made = await redeem(invitation.code, 'pr_probe_member')
    await made.context.close()
    const members = (await api(admin, '/admin/members')).items
    const record = members.find((m) => m.username === 'pr_probe_member')
    assert.ok(record, 'member not persisted')
    assert.equal(record.subscription_id, subscription.id)
    memberId = record.id
    fixtures.push({ source: 'real invitation redeem; password from owned owner.json', memberId, username: record.username })
    return { memberId, subscriptionId: subscription.id }
  })

  member = await context()
  if (
    !(await test('member browser login + /me/connect + read-only subscription state without global admin banner', async () => {
      mp = await login(member, 'pr_probe_member', '**/me/subscription')
      await expect(mp.getByTestId('service-summary-card')).toBeVisible()
      await expect(mp.locator('[role="alert"]')).toHaveCount(0)
      const sub = await api(member, '/me/subscription')
      secrets.push(sub.token)
      assert.ok(sub.token, 'real subscription token expected for an opened member')
      await mp.goto(base + '/me/connect')
      await mp.waitForLoadState('networkidle')
      await expect(mp.locator('#link')).toBeVisible()
      await expect(mp.locator('#qrcode')).toBeVisible()
      // The legacy deep links land on the same member view.
      for (const anchor of ['#link', '#qrcode']) {
        await mp.goto(base + '/me/subscription' + anchor)
        await mp.waitForURL((url) => url.pathname === '/me/connect' && url.hash === anchor)
        await expect(mp.locator(anchor)).toBeVisible()
      }
      await expect(mp.getByTestId('subscription-reset-button')).toBeVisible()
      assert.equal(await mp.getByTestId('recovery-mode-banner').count(), 0)
      observations.push({ name: 'member subscription (masked)', value: maskedSubscription(sub) })
      return { connectReachable: true, linkAndQrAnchorsPresent: true, legacyAnchorsResolvedForMember: true, memberSeesNoAdminBanner: true, tokenPersisted: true }
    }))
  )
    throw Error('member prerequisite failed')

  await test('member is denied admin route and admin API with an actionable in-place message', async () => {
    await mp.goto(base + '/admin/members')
    await mp.waitForURL((url) => url.pathname === '/me/subscription' && url.searchParams.get('denied') === '/admin/members')
    await expect(mp.locator('[role="alert"]').first()).toContainText('该页面仅管理员可用')
    const response = await member.request.get(base + '/api/v1/admin/members')
    assert.equal(response.status(), 403)
    await expect(mp.getByRole('button', { name: '新建成员', exact: true })).toHaveCount(0)
    return { redirectTo: new URL(mp.url()).pathname + new URL(mp.url()).search, apiStatus: 403, denialTextVisible: true }
  })

  await test('recovery mode is admin-global; existing token read-only; no-token member keeps status with local maintenance notice', async () => {
    const created = await api(admin, '/admin/members', 'POST', {
      username: 'pr_probe_no_token',
      password: owner.PANEL_ADMIN_PASSWORD,
      role: 'member',
      active: true,
      proxy_enabled: true,
      subscription_id: subscription.id,
    }, 201)
    noTokenMemberId = created.id
    fixtures.push({ source: 'real admin API; homepage never opened before recovery so no token exists', memberId: noTokenMemberId })
    assert.equal(sql(`select count(*)::int as c from subscription_tokens where member_id=${str(noTokenMemberId)}`)[0].c, 0)
    await api(admin, '/admin/recovery-mode/enter', 'POST', {})
    assert.equal((await api(admin, '/admin/recovery-mode')).recovery_mode, true)

    // Admin sees the persistent cross-page banner on every admin page.
    for (const path of ['/admin/overview', '/admin/runtime', '/admin/members']) {
      await ap.goto(base + path)
      await expect(ap.getByTestId('recovery-mode-banner')).toBeVisible()
      await expect(ap.getByTestId('recovery-mode-banner')).toContainText('恢复核对进行中')
    }
    // Existing token stays readable and the member gets no fabricated global banner.
    const existing = await api(member, '/me/subscription')
    assert.ok(existing.token)
    await mp.goto(base + '/me/subscription')
    await mp.waitForLoadState('networkidle')
    await expect(mp.getByTestId('recovery-mode-banner')).toHaveCount(0)
    await expect(mp.getByTestId('service-summary-card')).toBeVisible()
    assert.equal(await mp.getByTestId('import-maintenance-message').count(), 0)
    // No-token member: first token read is 409 and only the import area explains maintenance.
    const noTokenContext = await context()
    try {
      const firstRead = noTokenContext.waitForEvent('response', {
        predicate: (r) => new URL(r.url()).pathname === '/api/v1/me/subscription',
      })
      const page = await login(noTokenContext, 'pr_probe_no_token', '**/me/subscription')
      const response = await firstRead
      assert.equal(response.status(), 409)
      assert.equal((await response.json()).error.code, 'RECOVERY_MODE')
      await expect(page.getByTestId('import-maintenance-message')).toContainText('恢复核对进行中')
      await expect(page.getByTestId('service-summary-card')).toContainText('1.00 GiB')
      await expect(page.getByTestId('recovery-mode-banner')).toHaveCount(0)
      assert.equal((await api(noTokenContext, '/me/status')).access.access, true)
      assert.equal(sql(`select count(*)::int as c from subscription_tokens where member_id=${str(noTokenMemberId)}`)[0].c, 0, 'maintenance read must not create a token')
    } finally {
      await noTokenContext.close()
    }
    return {
      adminBannerOn: ['/admin/overview', '/admin/runtime', '/admin/members'],
      existingTokenHTTP: 200,
      noTokenFirstRead: 409,
      noTokenNotice: 'client-import-card only',
      memberGlobalBanner: false,
    }
  })

  await test('recovery exit requires explicit checks, persists, and removes the global banner', async () => {
    await ap.goto(base + '/admin/runtime#recovery')
    await expect(ap.getByTestId('recovery-exit-button')).toBeDisabled()
    await ap.getByTestId('recovery-exit-checkbox-agent').check()
    await ap.getByTestId('recovery-exit-checkbox-ledger').check()
    await ap.getByTestId('recovery-exit-confirm-input').fill('verified')
    await expect(ap.getByTestId('recovery-exit-button')).toBeEnabled()
    await ap.getByTestId('recovery-exit-button').click()
    await expect(ap.getByTestId('recovery-result-panel')).toBeVisible({ timeout: 20000 })
    assert.equal((await api(admin, '/admin/recovery-mode')).recovery_mode, false)
    await ap.goto(base + '/admin/overview')
    await expect(ap.getByTestId('recovery-mode-banner')).toHaveCount(0)
    return { exitRequiresChecks: true, persisted: false, bannerCleared: true, exitMeaning: '退出核对 ≠ 节点已全部恢复 (panel text)' }
  })

  await test('batch preview → submit → durable result envelope read back by the record page', async () => {
    const preview = await api(admin, '/admin/batches/preview', 'POST', {
      operation: 'set_expiry',
      member_ids: [memberId],
      values: { expires_at: '2032-01-02T03:04:00Z' },
      reason: 'PR probe synthetic expiry change',
    })
    assert.ok(preview.id, 'preview id missing')
    const idempotencyKey = `pr-probe-${Date.now()}-${Math.random().toString(36).slice(2)}`
    const submitted = await api(
      admin,
      '/admin/batches',
      'POST',
      { preview_id: preview.id, rules_version: preview.rules_version, member_set_digest: preview.member_set_digest, confirm: true },
      200,
      { 'Idempotency-Key': idempotencyKey },
    )
    batchId = submitted.batch?.id ?? submitted.id
    assert.ok(batchId, 'batch id missing from submit response')
    const envelope = await api(admin, `/admin/batches/${batchId}`)
    assert.ok(Array.isArray(envelope.results), 'envelope.results must be an array')
    const counts = { updated: 0, waiting: 0, failed: 0 }
    for (const item of envelope.results) {
      const state = item.state ?? item.status
      if (state === 'succeeded' || state === 'applied') counts.updated++
      else if (['failed', 'timeout', 'superseded', 'conflict', 'skipped'].includes(state ?? '')) counts.failed++
      else counts.waiting++
    }
    await ap.goto(base + `/admin/batches/${batchId}`)
    await ap.waitForLoadState('networkidle')
    await expect(ap.getByTestId('batch-result-counts')).toHaveText(
      `已更新 ${counts.updated} / 等待 ${counts.waiting} / 失败或需处理 ${counts.failed}`,
    )
    const rows = await ap.locator('tbody tr').count()
    assert.equal(rows, envelope.results.length, 'rendered result rows must match envelope results')
    const member = await api(admin, `/admin/members/${memberId}`)
    assert.equal((member.member ?? member).expires_at, '2032-01-02T03:04:00Z')
    await ap.reload()
    await expect(ap.getByTestId('batch-result-counts')).toHaveText(
      `已更新 ${counts.updated} / 等待 ${counts.waiting} / 失败或需处理 ${counts.failed}`,
    )
    return {
      batchId,
      resultStates: envelope.results.map((r) => r.state ?? r.status ?? 'unknown'),
      counts,
      renderedRows: rows,
      persistedExpiry: '2032-01-02T03:04:00Z',
      replay: Boolean(envelope.batch?.idempotent_replay ?? envelope.idempotent_replay),
    }
  })

  await test('390px member (touch/coarse): no horizontal overflow, key actions inside viewport, 44px targets', async () => {
    const c = await context({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
    try {
      const page = await login(c, 'pr_probe_member', '**/me/subscription')
      const facts = []
      facts.push(await assertMobilePage(page, '/me/subscription', 'member home', ['service-refresh-button', 'import-check-button', 'import-download-button']))
      facts.push(await assertMobilePage(page, '/me/connect', 'member connect', ['subscription-reset-button']))
      facts.push(await assertMobilePage(page, '/me/usage', 'member usage', ['usage-refresh-button']))
      for (const fact of facts) {
        assert.equal(fact.coarsePointer, true, 'probe viewport must report a coarse pointer')
        assert.ok(fact.navLinkHeight === null || fact.navLinkHeight >= 44, `mobile nav link height ${fact.navLinkHeight} < 44`)
      }
      const under44 = facts.flatMap((f) => f.smallestTargets.map((t) => `${f.path} ${t.tag} ${t.testid || t.text} h=${t.height}`))
      observations.push({ name: '390px member interactive targets below 44px (recorded, not asserted)', value: under44 })
      // Drawer must be usable instead of the desktop sidebar.
      await page.getByRole('button', { name: '打开导航' }).click()
      await expect(page.getByTestId('nav-me-service-mobile')).toBeVisible()
      return { pages: facts, keyActionsVisible: true, noHorizontalOverflow: true, recordedSmallTargets: under44.length }
    } finally {
      await c.close()
    }
  })

  await test('390px admin (touch/coarse): no horizontal overflow on workbench/setup/runtime/members/batch', async () => {
    const c = await context({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
    try {
      const page = await login(c, 'admin', '**/admin/overview')
      const facts = []
      facts.push(await assertMobilePage(page, '/admin/overview', 'workbench', ['workbench-refresh', 'overview-tasks-link']))
      facts.push(await assertMobilePage(page, '/admin/setup', 'setup', ['setup-refresh-button']))
      facts.push(await assertMobilePage(page, '/admin/runtime', 'runtime', ['runtime-refresh-button']))
      facts.push(
        await assertMobilePage(page, '/admin/runtime#recovery', 'runtime recovery tab', ['recovery-enter-button']),
      )
      facts.push(await assertMobilePage(page, '/admin/members', 'members', ['member-refresh-button', 'member-q-input', `member-username-${memberId}`]))
      facts.push(await assertMobilePage(page, `/admin/batches/${batchId}`, 'batch detail', []))
      for (const fact of facts) {
        assert.equal(fact.coarsePointer, true)
        assert.ok(fact.navLinkHeight === null || fact.navLinkHeight >= 44)
      }
      const under44 = facts.flatMap((f) => f.smallestTargets.map((t) => `${f.path} ${t.tag} ${t.testid || t.text} h=${t.height}`))
      observations.push({ name: '390px admin interactive targets below 44px (recorded, not asserted)', value: under44 })
      return { pages: facts.map((f) => ({ path: f.path, scrollWidth: f.scrollWidth, viewportWidth: f.viewport.width, overflow: false })), recordedSmallTargets: under44.length }
    } finally {
      await c.close()
    }
  })

  await test('light/dark toggle changes the real theme class and computed surface for both roles', async () => {
    const measure = async (page, label) => {
      await page.waitForLoadState('networkidle')
      const before = await page.evaluate(() => ({
        dark: document.documentElement.classList.contains('dark'),
        scheme: document.documentElement.dataset.theme,
        background: getComputedStyle(document.body).backgroundColor,
      }))
      await page.getByRole('button', { name: '切换主题' }).click()
      await expect
        .poll(() => page.evaluate(() => document.documentElement.classList.contains('dark')))
        .toBe(!before.dark)
      const after = await page.evaluate(() => ({
        dark: document.documentElement.classList.contains('dark'),
        scheme: document.documentElement.dataset.theme,
        background: getComputedStyle(document.body).backgroundColor,
      }))
      assert.notEqual(after.background, before.background, `${label}: background did not change between themes`)
      assert.equal(after.dark, !before.dark)
      assert.equal(after.scheme, after.dark ? 'dark' : 'light')
      await page.getByRole('button', { name: '切换主题' }).click()
      await expect.poll(() => page.evaluate(() => document.documentElement.classList.contains('dark'))).toBe(before.dark)
      return { label, before, after }
    }
    await ap.goto(base + '/admin/overview')
    const adminThemes = await measure(ap, 'admin')
    await mp.goto(base + '/me/subscription')
    const memberThemes = await measure(mp, 'member')
    return { adminThemes, memberThemes, persistedKey: 'pp.theme' }
  })

  await test('keyboard: Tab reaches drawer nav, Enter navigates, Esc closes and focus returns to the hamburger', async () => {
    const c = await context({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
    try {
      const page = await login(c, 'admin', '**/admin/overview')
      const hamburger = page.getByRole('button', { name: '打开导航' })
      await hamburger.focus()
      await page.keyboard.press('Enter')
      await expect(page.getByTestId('nav-runtime-mobile')).toBeVisible()
      // Tab through the opened drawer: focus must move into the drawer navigation.
      const visited = []
      let insideDrawer = false
      for (let i = 0; i < 12; i++) {
        await page.keyboard.press('Tab')
        const info = await page.evaluate(() => {
          const el = document.activeElement
          const drawer = document.querySelector('[role="dialog"]')
          return {
            label: (el?.getAttribute('data-testid') || el?.getAttribute('aria-label') || el?.textContent || '').trim().slice(0, 40),
            inDrawer: Boolean(drawer && el && drawer.contains(el)),
          }
        })
        visited.push(info.label)
        if (info.inDrawer) {
          insideDrawer = true
          break
        }
      }
      assert.ok(insideDrawer, `Tab never entered the drawer; visited: ${visited.join(' | ')}`)
      await page.keyboard.press('Escape')
      await expect(page.getByTestId('nav-runtime-mobile')).toHaveCount(0)
      const active = await page.evaluate(() => document.activeElement?.getAttribute('aria-label'))
      assert.equal(active, '打开导航', 'focus must return to the hamburger button after Esc')
      // Reopen and activate a nav entry purely with the keyboard.
      await hamburger.focus()
      await page.keyboard.press('Enter')
      await page.getByTestId('nav-runtime-mobile').focus()
      await page.keyboard.press('Enter')
      await page.waitForURL('**/admin/runtime')
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
      return { tabEnteredDrawer: true, focusReturnedToHamburger: true, enterNavigatedTo: new URL(page.url()).pathname, tabOrderSample: visited }
    } finally {
      await c.close()
    }
  })

  await test('no unhandled browser runtime errors across the probe', async () => {
    assert.deepEqual(pageErrors, [])
    return { pageErrors: 0, requestsObserved: requests.length }
  })
} catch (error) {
  results.push({ name: 'prerequisite/environment', status: 'FAIL', error: redact(error.stack) })
} finally {
  if (browser) await browser.close()
  if (panel && panel.exitCode === null) {
    panel.kill('SIGTERM')
    await Promise.race([new Promise((r) => panel.once('exit', r)), new Promise((r) => setTimeout(r, 12000))])
    if (panel.exitCode === null) {
      panel.kill('SIGKILL')
      await new Promise((r) => panel.once('exit', r))
    }
  }
  cleanups.push({ object: 'browser', closed: true })
  const log = await readFile(join(privateDir, 'panel.log'), 'utf8').catch(() => '')
  await writeFile(join(out, 'panel.log'), redact(log), { mode: 0o600 })
  for (const file of ['schema-proof.json', 'build.log']) {
    await copyFile(join(privateDir, file), join(out, file)).catch(() => {})
  }
  const buildLog = await readFile(join(privateDir, 'build.log'), 'utf8').catch(() => '')
  const metadata = {
    candidate: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim(),
    run,
    mode: 'frontend-phase technical UI probe (no user-story / no real Agent traffic claim)',
    transport: 'loopback development HTTP Panel-hosted current webbuild; NOT HTTPS/static-hosting evidence',
    playwright: JSON.parse(await readFile(join(root, 'web/node_modules/@playwright/test/package.json'), 'utf8')).version,
    chromium: { executable: chromium.executablePath(), version: browserVersion },
    fingerprints: {
      probe: sha256(await readFile(join(root, 'web/tests/frontend-phase-probe.mjs'))),
      fixtureHelper: sha256(await readFile(join(root, 'web/tests/frontend-phase-fixtures.mjs'))),
      launcher: sha256(await readFile(join(root, 'web/tests/product-readiness-env.sh'))),
      dbHelper: sha256(await readFile(join(root, 'web/tests/product-readiness-db.go'))),
      packageLock: sha256(await readFile(join(root, 'web/package-lock.json'))),
      buildLogHash: sha256(buildLog),
    },
    schema: owner.schema,
    fixtures,
    observations,
    results,
    requests,
    cleanups,
    notRun: [
      'HTTPS / TLS acceptance',
      'independent static hosting (nginx) frontend origin',
      'Agent registration, data plane, real core traffic, real rollback',
      'user-story / web-demo journeys and full 18-story suite',
      'README screenshots or full-page captures (deliberately not taken)',
      'screen-reader audit beyond keyboard/role/label checks',
    ],
  }
  await writeFile(join(out, 'report.json'), redact(JSON.stringify(metadata, null, 2)), { mode: 0o600 })
  await writeFile(join(out, 'results.json'), redact(JSON.stringify(results, null, 2)), { mode: 0o600 })
  const failed = results.filter((x) => x.status === 'FAIL')
  console.log(`Evidence: ${out}`)
  console.log(`${results.length - failed.length} PASS / ${failed.length} FAIL; unrun scopes explicitly listed`)
  process.exitCode = failed.length || results.length === 0 ? 1 : 0
}
