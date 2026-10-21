import { chromium, expect } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'

/**
 * A12 real-browser acceptance: admin + member key flows on a real panel
 * (real PostgreSQL/Redis behind the real HTTP handler), desktop 1440px and
 * mobile 390px, plus representative loading / error / empty states.
 *
 * REAL CHAIN — no interception, no fixtures, no fabricated payloads:
 *   login, the 12 admin pages, server creation through the UI, the new
 *   server's real "no Agent / no core instance" empty states, the members list
 *   filtered to zero (explicit empty state), member detail for alice with the
 *   same account/proxy verdicts as the list plus a real no-op save that proves
 *   the version round-trip, a real 404 error state produced by the panel, the
 *   5 member pages, member API + router permission denial. Every real page load
 *   also asserts: no top-level error banner, and no document-level horizontal
 *   overflow (wide tables must scroll inside .table-wrap instead). The old
 *   ".error-state" assertion was vacuous — that class does not exist anywhere in
 *   the Vue app; the real selector is ErrorState's ".banner--danger[role=alert]"
 *   under ".content".
 *
 * INJECTED — Playwright network interception, logged as INJECTED, never mixed
 * into the real chain:
 *   1) a 503 envelope for one real API call so the error state renders, then
 *      the interception is removed and the retry button must recover through
 *      the real API (the code is INJECTED_FAULT so it can never be mistaken
 *      for a real panel error);
 *   2) a response delay for one real API call so the loading skeleton is
 *      observable; the real response is still passed through (route.continue).
 */

const base = process.env.PANEL_BROWSER_URL
const password = process.env.PANEL_BROWSER_PASSWORD
if (!base?.startsWith('http://127.0.0.1:') || !password) throw new Error('Isolated test server and ephemeral credentials required')

// Injection targets are the real panel endpoints the pages already call.
const endpoint = {
  adminOverview: /\/api\/v1\/admin\/overview(?:\?.*)?$/,
  adminMembers: /\/api\/v1\/admin\/members(?:\?.*)?$/,
  meStatus: /\/api\/v1\/me\/status(?:\?.*)?$/,
  meSubscription: /\/api\/v1\/me\/subscription(?:\?.*)?$/,
}
const LOADING_LABEL = {
  members: '[role="status"][aria-label="正在读取成员"]',
  subscription: '[role="status"][aria-label="正在读取订阅"]',
}
const INJECTED_ERROR_BODY = JSON.stringify({
  error: {
    code: 'INJECTED_FAULT',
    message: '注入故障：面板依赖不可用（Playwright 网络注入，非真实后端行为）',
    retryable: true,
  },
})
const UNKNOWN_MEMBER_ID = '00000000-0000-4000-8000-000000000000'

expect.configure({ timeout: 10000 })

const pageErrors = []
const injectedNotes = []
let overflowChecks = 0
let tableLocalScrolls = 0
let step = 'start'

const browser = await chromium.launch({ headless: true })
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

async function newContext(options, label) {
  const context = await browser.newContext(options)
  // Unhandled runtime errors are collected per page and fail the run at the end.
  context.on('page', (page) => {
    page.on('pageerror', (error) => {
      pageErrors.push(`${label} ${error.name}: ${String(error.message).split('\n')[0]}`)
    })
  })
  return context
}

async function login(context, username) {
  const page = await context.newPage()
  await page.goto(`${base}/login`)
  await page.getByLabel('用户名', { exact: true }).fill(username)
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.waitForURL(username === 'admin' ? '**/admin/overview' : '**/me/subscription')
  return page
}

/** Real-chain page hygiene: no error banner, and no document-level side scroll. */
async function assertCleanPage(page, label) {
  await expect(page.locator('.content [role="alert"]'), `${label}: unexpected error banner`).toHaveCount(0)
  await assertNoDocumentOverflow(page, label)
}

async function assertNoDocumentOverflow(page, label) {
  const view = await page.evaluate(() => {
    const doc = document.documentElement
    const wraps = [...document.querySelectorAll('.table-wrap')].map((el) => ({ client: el.clientWidth, scroll: el.scrollWidth }))
    return {
      documentScroll: Math.max(doc.scrollWidth, document.body ? document.body.scrollWidth : 0),
      viewport: window.innerWidth,
      wraps,
    }
  })
  overflowChecks += 1
  if (view.documentScroll > view.viewport + 1) {
    throw new Error(`${label}: document scrolls horizontally ${view.documentScroll}px > viewport ${view.viewport}px`)
  }
  const local = view.wraps.filter((wrap) => wrap.scroll > wrap.client + 1)
  tableLocalScrolls += local.length
  return local.length ? `${local.length} table(s) scroll inside .table-wrap` : 'no horizontal overflow'
}

async function snapshot(page, name, fullPage = false) {
  if (!process.env.PI_SCRATCH_DIR) return
  const dir = join(process.env.PI_SCRATCH_DIR, 'browser-evidence')
  await mkdir(dir, { recursive: true })
  await page.screenshot({ path: join(dir, name), fullPage })
}

/** INJECTED delay: holds the real response, never replaces it. */
async function injectDelay(page, pattern, label, ms = 1400) {
  await page.route(pattern, async (route) => {
    await sleep(ms)
    await route.continue()
  })
  injectedNotes.push(`delay ${ms}ms on ${label} (route.continue, real response)`)
}

/** INJECTED 503: test-only error envelope so the error state is deterministic. */
async function injectFailure(page, pattern, label) {
  let hits = 0
  await page.route(pattern, async (route) => {
    hits += 1
    await route.fulfill({ status: 503, contentType: 'application/json; charset=utf-8', body: INJECTED_ERROR_BODY })
  })
  injectedNotes.push(`503 INJECTED_FAULT on ${label} (fulfilled, then removed before retry)`)
  return () => hits
}

try {
  /* ───────────────── desktop administrator (1440px), real chain ───────────────── */
  step = 'administrator login'
  const admin = await newContext({ viewport: { width: 1440, height: 1000 } }, 'admin-desktop')
  const page = await login(admin, 'admin')
  const routes = ['overview', 'servers', 'nodes', 'node-groups', 'subscriptions', 'members', 'invitations', 'usage', 'events', 'tasks', 'audit', 'settings']
  for (const route of routes) {
    step = `admin ${route}`
    const failures = []
    const onResponse = (response) => {
      if (response.url().includes('/api/v1/admin/') && response.status() >= 400) failures.push(response.status())
    }
    page.on('response', onResponse)
    await page.goto(`${base}/admin/${route}`)
    await page.waitForLoadState('networkidle')
    await expect(page.locator('h1').first()).toBeVisible()
    await assertCleanPage(page, `admin/${route} (1440px)`)
    page.off('response', onResponse)
    if (failures.length) throw new Error(`admin ${route} API status ${failures.join(',')}`)
  }

  step = 'create server through real UI'
  await page.goto(`${base}/admin/servers`)
  await page.getByRole('button', { name: '新建服务器', exact: true }).first().click()
  await page.getByLabel('名称', { exact: true }).fill('browser-created-server')
  await page.getByLabel('地址', { exact: true }).fill('127.0.0.1')
  await page.getByRole('button', { name: '保存', exact: true }).click()
  await expect(page.getByText('browser-created-server', { exact: true }).first()).toBeVisible()

  step = 'real empty state: runtime of the just-created server'
  await page.getByRole('link', { name: 'browser-created-server', exact: true }).first().click()
  await page.waitForURL('**/admin/servers/*/runtime')
  // A brand-new server cannot own an Agent or a core instance, so both lists are
  // genuinely empty (real GET /admin/agents + /admin/core-instances, no fixtures).
  await expect(page.getByRole('heading', { name: '该服务器未注册 Agent', exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: '没有核心实例', exact: true })).toBeVisible()
  await assertCleanPage(page, 'admin/servers/:id/runtime empty state (1440px)')
  await snapshot(page, 'admin-runtime-empty.png')
  const runtimePath = new URL(page.url()).pathname

  step = 'real error state: unknown member id (real panel 404)'
  await page.goto(`${base}/admin/members/${UNKNOWN_MEMBER_ID}`)
  const notFound = page.locator('.content [role="alert"]').first()
  await expect(notFound).toBeVisible()
  await expect(notFound).toContainText('对象不存在')
  await expect(notFound).toContainText('NOT_FOUND')
  // 404 is not retryable: the contract forbids offering a retry that cannot help.
  await expect(notFound.getByRole('button', { name: '重试', exact: true })).toHaveCount(0)
  await assertNoDocumentOverflow(page, 'admin/members/:unknown 404 (1440px)')

  step = 'INJECTED 503 error state on overview, then real retry'
  const overviewHits = await injectFailure(page, endpoint.adminOverview, '/api/v1/admin/overview')
  await page.goto(`${base}/admin/overview`)
  const injectedAlert = page.locator('.content [role="alert"]').first()
  await expect(injectedAlert).toBeVisible()
  await expect(injectedAlert).toContainText('面板或依赖服务故障')
  await expect(injectedAlert).toContainText('INJECTED_FAULT')
  await expect(injectedAlert).toContainText('注入故障')
  await page.unroute(endpoint.adminOverview)
  await injectedAlert.getByRole('button', { name: '重试', exact: true }).click()
  await expect(page.locator('.kpi').first()).toBeVisible()
  await expect(page.locator('.content [role="alert"]'), 'overview did not recover after injected 503').toHaveCount(0)
  if (overviewHits() < 1) throw new Error('injected overview fault never reached the page')
  await assertNoDocumentOverflow(page, 'admin/overview after retry (1440px)')

  step = 'INJECTED delay: admin members loading skeleton'
  await injectDelay(page, endpoint.adminMembers, '/api/v1/admin/members')
  await page.goto(`${base}/admin/members`)
  await expect(page.locator(LOADING_LABEL.members)).toBeVisible()
  await expect(page.locator('table.table tbody tr').first()).toBeVisible()
  await expect(page.locator(LOADING_LABEL.members)).toHaveCount(0)
  await page.unroute(endpoint.adminMembers)
  await assertCleanPage(page, 'admin/members after delayed load (1440px)')

  step = 'real member detail: alice via the members list (strong)'
  // 同一成员在列表页与详情页必须给出同一套判定结果：先记录列表页的徽标与订阅，再进详情页比对。
  await expect(page.locator('table.table tbody tr').first()).toBeVisible()
  const aliceRow = page.locator('table.table tbody tr', { has: page.getByRole('link', { name: 'alice', exact: true }) }).first()
  const aliceCells = aliceRow.locator('td')
  const listAccountBadge = (await aliceCells.nth(2).locator('.badge').nth(0).innerText()).trim()
  const listProxyBadge = (await aliceCells.nth(2).locator('.badge').nth(1).innerText()).trim()
  const listSubscription = (await aliceCells.nth(3).innerText()).trim()
  if (!listSubscription || listSubscription === '未绑定') throw new Error('seed member alice has no subscription on the real panel')
  await aliceRow.getByRole('link', { name: 'alice', exact: true }).click()
  await page.waitForURL('**/admin/members/*')
  await expect(page.locator('h1').first()).toHaveText('成员 · alice')
  // 状态/订阅分处两个 .dl，按标签在全页定位，避免取错列。
  const detailField = (label) => page.locator('.content dt', { hasText: label }).locator('xpath=following-sibling::dd[1]')
  await expect(page.locator('.dl').first()).toContainText('alice')
  await expect(detailField('角色')).toHaveText('成员')
  await expect(detailField('账户状态')).toHaveText(listAccountBadge)
  await expect(detailField('代理访问').locator('.badge').first()).toHaveText(listProxyBadge)
  const detailSubscription = (await detailField('订阅').innerText()).trim()
  // id 形如 prefix_base32（internal/panel/ids），告警文案里的“未绑定”必须被排除。
  if (!/^[a-z]{2,8}_[a-z0-9]{16,}$/i.test(detailSubscription)) {
    throw new Error(`member detail does not show a real subscription id: ${JSON.stringify(detailSubscription)}`)
  }
  await expect(detailField('已用 / 额度')).toContainText('/')
  // 原始响应必须带 version（详情页 PATCH 依赖）和 access 派生值（解包失败时三者都缺失）。
  const rawDetail = (await page.locator('pre.code-block').first().textContent()) ?? ''
  if (!/"version":\s*\d+/.test(rawDetail)) throw new Error('member detail payload has no version; PATCH would send undefined')
  if (!/"proxy_access":\s*"[a-z_]+"/.test(rawDetail)) throw new Error('member detail payload has no derived proxy_access')
  if (!/"used_bytes":\s*\d+/.test(rawDetail)) throw new Error('member detail payload has no derived used_bytes')
  await assertCleanPage(page, 'admin/members/:id detail (1440px)')

  step = 'real member update: no-op save proves the version round-trip'
  await page.goto(`${base}/admin/members`)
  await page.waitForLoadState('networkidle')
  const editRow = page.locator('table.table tbody tr', { has: page.getByRole('link', { name: 'alice', exact: true }) }).first()
  await editRow.getByRole('button', { name: '编辑', exact: true }).click()
  const editDialog = page.getByRole('dialog', { name: '编辑成员 · alice' })
  await expect(editDialog).toBeVisible()
  // 表单回填真实成员值：解包失败时用户名为空。
  await expect(editDialog.getByLabel('用户名', { exact: true })).toHaveValue('alice')
  const patchStatuses = []
  const onPatch = (response) => {
    if (response.request().method() === 'PATCH' && response.url().includes('/api/v1/admin/members/')) patchStatuses.push(response.status())
  }
  page.on('response', onPatch)
  // 不修改任何字段直接保存：值未变、可逆、不带副作用，只验证 version 与 PATCH 契约。
  await editDialog.getByRole('button', { name: '保存', exact: true }).click()
  await expect(page.getByText('成员已更新', { exact: true }).first()).toBeVisible()
  page.off('response', onPatch)
  if (patchStatuses.length !== 1 || patchStatuses[0] !== 200) throw new Error(`member PATCH statuses ${JSON.stringify(patchStatuses)}`)
  await expect(editDialog).toHaveCount(0)
  await expect(page.getByRole('link', { name: 'alice', exact: true }).first()).toBeVisible()

  step = 'screenshot admin overview'
  await page.goto(`${base}/admin/overview`)
  await page.waitForLoadState('networkidle')
  await snapshot(page, 'admin-overview.png', true)

  /* ───────────────── mobile administrator (390px), loading/error/empty ───────────────── */
  step = 'administrator login (390px)'
  const adminMobile = await newContext({ viewport: { width: 390, height: 844 } }, 'admin-mobile')
  const mobilePage = await login(adminMobile, 'admin')

  step = 'INJECTED delay: admin members loading skeleton (390px)'
  await injectDelay(mobilePage, endpoint.adminMembers, '/api/v1/admin/members @390px')
  await mobilePage.goto(`${base}/admin/members`)
  await expect(mobilePage.locator(LOADING_LABEL.members)).toBeVisible()
  await expect(mobilePage.locator('table.table tbody tr').first()).toBeVisible()
  await expect(mobilePage.locator(LOADING_LABEL.members)).toHaveCount(0)
  await mobilePage.unroute(endpoint.adminMembers)
  const mobileMembersLayout = await assertNoDocumentOverflow(mobilePage, 'admin/members (390px)')
  await assertCleanPage(mobilePage, 'admin/members (390px)')
  await snapshot(mobilePage, 'admin-members-390.png')

  step = 'real empty state: member list filter with no match (390px)'
  // 过滤只作用于当前页；无匹配时必须是显式空状态，不能只剩一个空表体。
  await mobilePage.getByLabel('过滤成员').fill('zzz-no-such-member')
  await expect(mobilePage.getByRole('heading', { name: '当前页没有匹配的成员', exact: true })).toBeVisible()
  await expect(mobilePage.locator('table.table')).toHaveCount(0)
  await assertCleanPage(mobilePage, 'admin/members filtered-empty (390px)')
  await mobilePage.getByRole('button', { name: '清空过滤', exact: true }).click()
  await expect(mobilePage.locator('table.table tbody tr').first()).toBeVisible()
  await expect(mobilePage.getByRole('heading', { name: '当前页没有匹配的成员', exact: true })).toHaveCount(0)

  step = 'INJECTED 503 on members list (390px), then real retry'
  const mobileHits = await injectFailure(mobilePage, endpoint.adminMembers, '/api/v1/admin/members @390px')
  await mobilePage.goto(`${base}/admin/members`)
  const mobileAlert = mobilePage.locator('.content [role="alert"]').first()
  await expect(mobileAlert).toBeVisible()
  await expect(mobileAlert).toContainText('INJECTED_FAULT')
  await mobilePage.unroute(endpoint.adminMembers)
  await mobileAlert.getByRole('button', { name: '重试', exact: true }).click()
  await expect(mobilePage.locator('table.table tbody tr').first()).toBeVisible()
  await expect(mobilePage.locator('.content [role="alert"]'), 'members did not recover after injected 503').toHaveCount(0)
  if (mobileHits() < 1) throw new Error('injected members fault never reached the page')

  step = 'real empty state: runtime page (390px)'
  await mobilePage.goto(`${base}${runtimePath}`)
  await expect(mobilePage.getByRole('heading', { name: '该服务器未注册 Agent', exact: true })).toBeVisible()
  await expect(mobilePage.getByRole('heading', { name: '没有核心实例', exact: true })).toBeVisible()
  await assertCleanPage(mobilePage, 'admin/servers/:id/runtime empty state (390px)')

  /* ───────────────── mobile member (390px), real chain + loading/error ───────────────── */
  step = 'member login'
  const member = await newContext({ viewport: { width: 390, height: 844 } }, 'member-mobile')
  const memberPage = await login(member, 'alice')
  for (const route of ['subscription', 'usage', 'credentials', 'status', 'security']) {
    step = `member ${route}`
    await memberPage.goto(`${base}/me/${route}`)
    await memberPage.waitForLoadState('networkidle')
    await expect(memberPage.locator('h1').first()).toBeVisible()
    await assertCleanPage(memberPage, `me/${route} (390px)`)
  }

  step = 'INJECTED delay: member subscription loading skeleton'
  await injectDelay(memberPage, endpoint.meSubscription, '/api/v1/me/subscription')
  await memberPage.goto(`${base}/me/subscription`)
  await expect(memberPage.locator(LOADING_LABEL.subscription)).toBeVisible()
  // After the delayed real response: either the subscription card or the real empty state.
  await expect(memberPage.locator('#format').or(memberPage.getByRole('heading', { name: '当前没有可用订阅', exact: true })).first()).toBeVisible()
  await expect(memberPage.locator(LOADING_LABEL.subscription)).toHaveCount(0)
  await memberPage.unroute(endpoint.meSubscription)
  await assertCleanPage(memberPage, 'me/subscription after delayed load (390px)')

  step = 'INJECTED 503 on member status, then real retry'
  const statusHits = await injectFailure(memberPage, endpoint.meStatus, '/api/v1/me/status')
  await memberPage.goto(`${base}/me/status`)
  const statusAlert = memberPage.locator('.content [role="alert"]').first()
  await expect(statusAlert).toBeVisible()
  await expect(statusAlert).toContainText('INJECTED_FAULT')
  await memberPage.unroute(endpoint.meStatus)
  await statusAlert.getByRole('button', { name: '重试', exact: true }).click()
  await expect(memberPage.getByRole('heading', { name: '状态', exact: true })).toBeVisible()
  await expect(memberPage.locator('.content [role="alert"]'), 'member status did not recover after injected 503').toHaveCount(0)
  if (statusHits() < 1) throw new Error('injected member status fault never reached the page')
  await assertNoDocumentOverflow(memberPage, 'me/status after retry (390px)')

  step = 'member authorization boundaries'
  const denied = await member.request.get(`${base}/api/v1/admin/members`)
  if (denied.status() !== 403) throw new Error('member accessed admin API')
  await memberPage.goto(`${base}/admin/members`)
  await memberPage.waitForURL('**/me/subscription?denied=**')

  if (pageErrors.length) throw new Error(`browser runtime errors: ${pageErrors.join(' | ')}`)
  for (const note of injectedNotes) console.log(`INJECTED: ${note}`)
  console.log(`REAL (no interception): admin login + 12 admin pages, server creation via UI, runtime Agent/core empty states, members filter empty state, member detail (alice: subscription/verdicts/version) + no-op save (PATCH 200), real 404 error state, 5 member pages (390px), API/router permission denial`)
  console.log(`LAYOUT: ${overflowChecks} overflow checks (desktop 1440px / mobile 390px); ${tableLocalScrolls} local table scroll(s) inside .table-wrap; admin/members @390px: ${mobileMembersLayout}`)
  console.log('PASS: real admin + member flows with desktop/mobile loading, error and empty states; no unhandled page errors')
} catch (error) {
  // Never dump DOM/network responses: subscription links contain credentials.
  console.error(`FAIL at ${step}: ${String(error.message).split('\n')[0]}`)
  process.exitCode = 1
} finally {
  await browser.close()
}
