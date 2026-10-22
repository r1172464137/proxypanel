/**
 * Full Web UI end-to-end matrix for proxypanel.
 *
 * REAL browser + REAL backend. The only network interception is the single
 * loading-state case, explicitly marked INJECTED (route delay, real response
 * still passed through).
 *
 * Coverage:
 *   1. Rendering matrix: every route in web/src/router/index.ts (the 29 listed
 *      in docs/verification/full-e2e-matrix.md plus the router's not-found
 *      fallback) x desktop/mobile x light/dark.
 *      Each combo asserts HTTP 200, main content > 30 chars, no console errors,
 *      no pageerrors, no document horizontal overflow, and (dark only) a rough
 *      foreground/background contrast check.
 *   2. State matrix: real empty state, INJECTED loading skeleton, real 404 error.
 *   3. 14 interaction cases with real UI operations and data verification.
 *   4. UI permission matrix for a real member (labuser) and admin /me access.
 *
 * Run:
 *   cd web
 *   PANEL_ADMIN_PASSWORD='...' node tests/e2e-ui-full.mjs
 *
 * Env:
 *   PANEL_BASE            default https://192.168.5.203:8443
 *   PANEL_ADMIN_USER      default admin
 *   PANEL_ADMIN_PASSWORD  required
 *   PANEL_MEMBER_USER     default labuser
 *   PANEL_MEMBER_PASSWORD default LabUser-pass-123
 *   E2E_OUT               default /home/compile/Work/evidence/e2e
 */
import { chromium } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
import { join } from 'node:path'

const BASE = (process.env.PANEL_BASE || 'https://192.168.5.203:8443').replace(/\/$/, '')
const ADMIN_USER = process.env.PANEL_ADMIN_USER || 'admin'
const ADMIN_PASSWORD = process.env.PANEL_ADMIN_PASSWORD || ''
const MEMBER_USER = process.env.PANEL_MEMBER_USER || 'labuser'
const MEMBER_PASSWORD = process.env.PANEL_MEMBER_PASSWORD || 'LabUser-pass-123'
const OUT = process.env.E2E_OUT || '/home/compile/Work/evidence/e2e'
const PREFIX = `e2e-ui-${Date.now()}-`
const RUN_STARTED = Date.now()

if (!ADMIN_PASSWORD) {
  console.error('PANEL_ADMIN_PASSWORD is required (admin password for the real panel).')
  process.exit(2)
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const results = []
const rawConsoleErrors = []
const rawPageErrors = []
const screenshots = []
let caseSeq = 0

function nextId() {
  caseSeq += 1
  return `C${String(caseSeq).padStart(4, '0')}`
}

function record(name, expectation, actual, pass, extra = {}) {
  const item = {
    id: extra.id || nextId(),
    case: name,
    expectation,
    actual,
    status: pass ? 'PASS' : 'FAIL',
    ...('injected' in extra ? { injected: extra.injected } : {}),
    ...(extra.screenshot ? { screenshot: extra.screenshot } : {}),
    ...(extra.note ? { note: extra.note } : {}),
    ...(extra.messages ? { messages: extra.messages } : {}),
  }
  results.push(item)
  const tag = pass ? 'PASS' : 'FAIL'
  console.log(`${tag} [${item.id}] ${name}`)
  console.log(`     expected: ${expectation}`)
  console.log(`     actual  : ${actual}`)
  return item
}

function safeName(name) {
  return name
    .replace(/[^\p{L}\p{N}._-]+/gu, '_')
    .replace(/_+/g, '_')
    .slice(0, 120)
}

async function snap(page, caseName) {
  const file = join(OUT, `ui-${safeName(caseName)}.png`)
  try {
    await page.screenshot({ path: file, fullPage: true })
    screenshots.push(file)
    return file
  } catch {
    return null
  }
}

async function recordFailure(page, name, expectation, actual, extra = {}) {
  const shot = page ? await snap(page, name) : null
  return record(name, expectation, actual, false, { ...extra, ...(shot ? { screenshot: shot } : {}) })
}

// 未登录页面（/、/login、/redeem）启动时会探测一次当前会话，后端按契约返回
// 401。Chrome 把这个失败的请求记成一条 "Failed to load resource ... 401"
// console error，JS 侧无法抑制（fetch 的 catch 不影响浏览器的网络层记录）。
// 这是 SPA 的常态，不是缺陷，所以单列出来；出现**其他**任何 console error
// 仍然算 FAIL。
// console 的 text 里没有请求 URL，必须用 msg.location().url 才能认出是哪一次请求
// （实测：{url: ".../api/v1/auth/session", line: 0, column: 0}）。
function isAnonymousSessionProbe(msg) {
  if (!/status of 401/.test(msg.text())) return false
  const url = msg.location?.()?.url || ''
  return /\/api\/v1\/auth\/session$/.test(url)
}

function attachLogs(page, bucket) {
  bucket.anonymousProbe = []
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    const entry = `${page.url()} :: ${msg.text().split('\n')[0]}`
    if (isAnonymousSessionProbe(msg)) {
      bucket.anonymousProbe.push(entry)
      return
    }
    bucket.console.push(entry)
    rawConsoleErrors.push(entry)
  })
  page.on('pageerror', (err) => {
    const entry = `${page.url()} :: ${err.name}: ${String(err.message).split('\n')[0]}`
    bucket.pageerror.push(entry)
    rawPageErrors.push(entry)
  })
  page.on('requestfailed', (req) => {
    // Not a FAIL by itself, but useful context when a page looks empty.
    const failure = req.failure()?.errorText || 'unknown'
    if (failure.includes('net::ERR_ABORTED')) return
    bucket.requestfailed.push(`${req.method()} ${req.url()} :: ${failure}`)
  })
}

/* ------------------------------------------------------------------ */
/* HTTP helper: uses the browser context's real session + CSRF cookie  */
/* ------------------------------------------------------------------ */
async function apiCall(context, path, { method = 'GET', body, csrf, headers = {} } = {}) {
  const res = await context.request.fetch(`${BASE}${path}`, {
    method,
    failOnStatusCode: false,
    headers: {
      Accept: 'application/json',
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      ...(csrf ? { 'X-CSRF-Token': csrf } : {}),
      ...headers,
    },
    ...(body !== undefined ? { data: JSON.stringify(body) } : {}),
  })
  const text = await res.text()
  let payload = null
  try {
    payload = text ? JSON.parse(text) : null
  } catch {
    payload = text.slice(0, 500)
  }
  return { status: res.status(), body: payload }
}

async function apiData(context, path, opts = {}) {
  const res = await apiCall(context, path, opts)
  if (res.status < 200 || res.status >= 300) {
    throw new Error(`${opts.method || 'GET'} ${path} -> HTTP ${res.status}: ${JSON.stringify(res.body).slice(0, 300)}`)
  }
  return res.body && typeof res.body === 'object' && 'data' in res.body ? res.body.data : res.body
}

async function apiPage(context, path, opts = {}) {
  const data = await apiData(context, path, opts)
  if (data && Array.isArray(data.items)) return data.items
  if (Array.isArray(data)) return data
  return []
}

/* ------------------------------------------------------------------ */
/* Login helpers                                                       */
/* ------------------------------------------------------------------ */
function isRateLimited(text) {
  return /频繁|too many login|RATE_LIMITED|429/i.test(text || '')
}

async function fillAndSubmitLogin(page, username, password, { navigate = true } = {}) {
  if (navigate) await page.goto(`${BASE}/login`, { waitUntil: 'networkidle', timeout: 30000 })
  await page.locator('#login-username').fill(username)
  await page.locator('#login-password').fill(password)
  await page.locator('button[type=submit]').click()
  await page.waitForTimeout(1200)
}

async function loginAs(page, username, password, { retryRateLimit = true, navigate = true } = {}) {
  await fillAndSubmitLogin(page, username, password, { navigate })
  if (!page.url().includes('/login')) return { ok: true }
  const alert = (await page.locator('[role=alert]').allInnerTexts().catch(() => [])).join(' | ')
  if (retryRateLimit && isRateLimited(alert)) {
    console.log(`     login rate limited for ${username}; waiting 65s for the real limiter window`)
    await sleep(65000)
    await fillAndSubmitLogin(page, username, password, { navigate: true })
    if (!page.url().includes('/login')) return { ok: true, waited: true }
  }
  return {
    ok: false,
    alert: (await page.locator('[role=alert]').allInnerTexts().catch(() => [])).join(' | ') || '(no alert)',
  }
}

async function mainTextOf(page) {
  return page.evaluate(() => {
    const main = document.querySelector('main')
    const target = main || document.body
    return (target?.innerText || '').trim()
  })
}

async function pageMetrics(page) {
  return page.evaluate(() => {
    const doc = document.documentElement
    const body = document.body
    const cs = getComputedStyle(body)
    function parseRgb(value) {
      const text = String(value).trim()
      const rgbMatch = text.match(/rgba?\(([^)]+)\)/)
      if (rgbMatch) {
        const parts = rgbMatch[1].split(/[,\s/]+/).filter(Boolean).map(Number)
        return { r: parts[0], g: parts[1], b: parts[2], a: parts[3] ?? 1 }
      }
      // Chromium keeps CSS Color 4 values in their authored space (the panel's
      // theme uses OKLCH). Convert OKLCH -> OKLab -> linear sRGB -> sRGB so the
      // contrast/luminance checks see real numbers instead of "无法计算".
      const oklchMatch = text.match(/oklch\(([^)]+)\)/)
      if (oklchMatch) {
        const parts = oklchMatch[1].split(/[\s/]+/).filter(Boolean)
        let L = Number(parts[0])
        const C = Number(parts[1])
        const H = Number(parts[2])
        if (String(parts[0]).endsWith('%')) L /= 100
        if (![L, C, H].every(Number.isFinite)) return null
        const hRad = (H * Math.PI) / 180
        const a = C * Math.cos(hRad)
        const b = C * Math.sin(hRad)
        const l_ = L + 0.3963377774 * a + 0.2158037573 * b
        const m_ = L - 0.1055613458 * a - 0.0638541728 * b
        const s_ = L - 0.0894841775 * a - 1.291485548 * b
        const l = l_ ** 3
        const m = m_ ** 3
        const s = s_ ** 3
        const lin = [
          4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
          -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
          -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
        ]
        const toSrgb = (c) => (c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055)
        const [r, g, bb] = lin.map((c) => Math.max(0, Math.min(255, Math.round(toSrgb(c) * 255))))
        const alpha = parts[3] === undefined ? 1 : Number(parts[3])
        return { r, g, b: bb, a: alpha }
      }
      return null
    }
    function luminance({ r, g, b }) {
      const convert = (v) => {
        const c = v / 255
        return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
      }
      return 0.2126 * convert(r) + 0.7152 * convert(g) + 0.0722 * convert(b)
    }
    const fg = parseRgb(cs.color)
    const bg = parseRgb(cs.backgroundColor)
    let contrast = null
    let bgLuminance = null
    if (bg && bg.a > 0) bgLuminance = luminance(bg)
    if (fg && bg && bg.a > 0) {
      const l1 = luminance(fg)
      const l2 = luminance(bg)
      contrast = (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05)
    }
    return {
      scrollWidth: doc.scrollWidth,
      clientWidth: doc.clientWidth,
      color: cs.color,
      backgroundColor: cs.backgroundColor,
      contrast,
      bgLuminance,
      theme: doc.dataset.theme || null,
      htmlClass: doc.className || '',
    }
  })
}

/* ------------------------------------------------------------------ */
/* Route matrix                                                        */
/* ------------------------------------------------------------------ */
const ROUTES = [
  { path: '/', public: true, label: '首页重定向' },
  { path: '/login', public: true, label: '登录' },
  { path: '/redeem', public: true, label: '邀请兑换' },
  { path: '/admin/overview', label: '总览' },
  { path: '/admin/servers', label: '服务器' },
  { path: '/admin/servers/:id/runtime', label: '服务器运行时' },
  { path: '/admin/nodes', label: '节点' },
  { path: '/admin/nodes/new', label: '新建节点' },
  { path: '/admin/nodes/:id', label: '编辑节点' },
  { path: '/admin/node-groups', label: '节点组' },
  { path: '/admin/subscriptions', label: '订阅' },
  { path: '/admin/subscriptions/new', label: '新建订阅' },
  { path: '/admin/subscriptions/:id', label: '编辑订阅' },
  { path: '/admin/members', label: '成员' },
  { path: '/admin/members/:id', label: '成员详情' },
  { path: '/admin/invitations', label: '邀请' },
  { path: '/admin/batches', label: '批量任务' },
  { path: '/admin/batches/:id', label: '批量任务详情' },
  { path: '/admin/usage', label: '用量' },
  { path: '/admin/events', label: '事件与告警' },
  { path: '/admin/tasks', label: '任务与同步' },
  { path: '/admin/audit', label: '审计' },
  { path: '/admin/settings', label: '设置' },
  { path: '/admin/templates', label: '模板' },
  { path: '/me/subscription', label: '我的订阅' },
  { path: '/me/usage', label: '我的用量' },
  { path: '/me/credentials', label: '凭据' },
  { path: '/me/status', label: '账户状态' },
  { path: '/me/security', label: '账号安全' },
  // The router's catch-all. full-e2e-matrix says 30 routes but enumerates 29,
  // so the router's real not-found route is the 30th.
  { path: '/__e2e_not_found__', label: '路由兜底 404' },
]

function resolveRoute(route, ids) {
  return route.path
    .replace(':id/runtime', `${ids.server}/runtime`)
    .replace('/admin/nodes/:id', `/admin/nodes/${ids.node}`)
    .replace('/admin/subscriptions/:id', `/admin/subscriptions/${ids.subscription}`)
    .replace('/admin/members/:id', `/admin/members/${ids.member}`)
    .replace('/admin/batches/:id', `/admin/batches/${ids.batch}`)
}

async function runRenderingMatrix(browser, state, ids) {
  const viewports = [
    { name: 'desktop', width: 1440, height: 900 },
    { name: 'mobile', width: 390, height: 844 },
  ]
  const themes = ['light', 'dark']

  for (const vp of viewports) {
    for (const theme of themes) {
      const context = await browser.newContext({
        ignoreHTTPSErrors: true,
        viewport: { width: vp.width, height: vp.height },
        locale: 'zh-CN',
        storageState: state,
      })
      // Public pages must be rendered as a logged-out visitor: an authenticated
      // visitor is redirected home by the router's public guard.
      const publicContext = await browser.newContext({
        ignoreHTTPSErrors: true,
        viewport: { width: vp.width, height: vp.height },
        locale: 'zh-CN',
      })
      // Theme is read from localStorage before the app mounts; this makes the
      // combo deterministic without clicking through the app for every route.
      await context.addInitScript((t) => {
        try {
          window.localStorage.setItem('pp.theme', t)
        } catch {}
      }, theme)
      await publicContext.addInitScript((t) => {
        try {
          window.localStorage.setItem('pp.theme', t)
        } catch {}
      }, theme)

      for (const route of ROUTES) {
        const id = `R-${vp.name}-${theme}-${route.path === '/' ? 'root' : route.path.replace(/[^\w]+/g, '_')}`
        const name = `渲染 ${route.path} (${route.label}) @${vp.width}x${vp.height} ${theme}`
        const page = await (route.public ? publicContext : context).newPage()
        const bucket = { console: [], pageerror: [], requestfailed: [] }
        attachLogs(page, bucket)
        let status = null
        let navError = null
        try {
          const resp = await page.goto(`${BASE}${resolveRoute(route, ids)}`, {
            waitUntil: 'networkidle',
            timeout: 30000,
          })
          status = resp?.status() ?? null
          // Wait for the real LoadingBlock to disappear before measuring text /
          // overflow. networkidle can fire after the XHR but before Vue paints
          // the data; measuring early gives false PASSes (notably detail pages
          // with wide tables).
          await page.waitForFunction(() => {
            return ![...document.querySelectorAll('[role="status"]')].some((el) => {
              const visible = el.getClientRects().length > 0 && getComputedStyle(el).visibility !== 'hidden'
              return visible && /正在读取|加载中|正在请求/.test(el.textContent || '')
            })
          }, null, { timeout: 15000 }).catch(() => {})
          await page.waitForTimeout(250)
        } catch (caught) {
          navError = caught instanceof Error ? caught.message.split('\n')[0] : String(caught)
        }

        if (navError) {
          await recordFailure(page, name, 'HTTP 200，页面可加载', `导航失败：${navError}`, { id })
          await page.close()
          continue
        }

        const url = page.url()
        const text = await mainTextOf(page).catch(() => '')
        const metrics = await pageMetrics(page).catch(() => null)
        const finalPath = (() => {
          try {
            return new URL(url).pathname
          } catch {
            return url
          }
        })()

        // 1) HTTP 200
        record(
          `${name} · HTTP`,
          'HTTP 200',
          `HTTP ${status} (final ${finalPath})`,
          status === 200,
          { id: `${id}-http`, ...(status !== 200 ? { screenshot: await snap(page, `${name}_http`) } : {}) },
        )

        // 2) main content non-empty
        const okText = text.length > 30
        record(
          `${name} · 主内容`,
          '主内容 > 30 字符',
          `${text.length} 字符${okText ? '' : `：${JSON.stringify(text.slice(0, 80))}`}`,
          okText,
          { id: `${id}-text`, ...(!okText ? { screenshot: await snap(page, `${name}_text`) } : {}) },
        )

        // 3) zero console errors (every message is preserved in messages)
        const probeNote = bucket.anonymousProbe?.length
          ? `（另有 ${bucket.anonymousProbe.length} 条未登录会话探测的 401，已豁免）`
          : ''
        const okConsole = bucket.console.length === 0
        if (!okConsole) {
          record(
            `${name} · console error`,
            '0 条 console error（未登录页的 401 会话探测除外）',
            `${bucket.console.length} 条：${bucket.console.join(' | ').slice(0, 500)}${probeNote}`,
            false,
            {
              id: `${id}-console`,
              messages: bucket.console,
              screenshot: await snap(page, `${name}_console`),
            },
          )
        } else {
          record(`${name} · console error`, '0 条 console error（未登录页的 401 会话探测除外）', `0 条${probeNote}`, true, { id: `${id}-console` })
        }

        // 4) zero pageerror
        const okPageError = bucket.pageerror.length === 0
        record(
          `${name} · pageerror`,
          '0 条 pageerror',
          okPageError ? '0 条' : `${bucket.pageerror.length} 条：${bucket.pageerror.join(' | ').slice(0, 500)}`,
          okPageError,
          {
            id: `${id}-pageerror`,
            ...(!okPageError ? { messages: bucket.pageerror, screenshot: await snap(page, `${name}_pageerror`) } : {}),
          },
        )

        // 5) no document horizontal overflow
        const okOverflow = metrics ? metrics.scrollWidth <= metrics.clientWidth + 1 : false
        record(
          `${name} · 横向溢出`,
          'scrollWidth <= clientWidth + 1',
          metrics
            ? `scrollWidth=${metrics.scrollWidth}, clientWidth=${metrics.clientWidth}`
            : '无法读取尺寸',
          okOverflow,
          { id: `${id}-overflow`, ...(!okOverflow ? { screenshot: await snap(page, `${name}_overflow`) } : {}) },
        )

        // 6) dark mode contrast (rough check; the task asks for a coarse look)
        if (theme === 'dark') {
          const okDark = metrics?.theme === 'dark'
          record(
            `${name} · 深色标记`,
            'html theme 标记为 dark',
            `data-theme=${metrics?.theme ?? 'null'}, class=${JSON.stringify(metrics?.htmlClass ?? '')}`,
            okDark,
            { id: `${id}-darkmark`, ...(!okDark ? { screenshot: await snap(page, `${name}_darkmark`) } : {}) },
          )
          const contrast = metrics?.contrast
          const okContrast = typeof contrast === 'number' && contrast >= 3
          record(
            `${name} · 深色对比度`,
            'body 前景/背景对比度 >= 3.0 且不是同色',
            `color=${metrics?.color ?? '?'}, backgroundColor=${metrics?.backgroundColor ?? '?'}, ratio=${typeof contrast === 'number' ? contrast.toFixed(2) : '无法计算'}`,
            okContrast,
            { id: `${id}-contrast`, ...(!okContrast ? { screenshot: await snap(page, `${name}_contrast`) } : {}) },
          )
          // A dark theme must actually paint a dark background. The contrast
          // check above also passes on light-on-white, so this catches a theme
          // marker that changes without the palette following it.
          const bgLum = metrics?.bgLuminance
          const okDarkPalette = typeof bgLum === 'number' && bgLum < 0.3
          record(
            `${name} · 深色底色`,
            'data-theme=dark 时 body 背景相对亮度 < 0.3（深色底）',
            `backgroundColor=${metrics?.backgroundColor ?? '?'}, bgLuminance=${typeof bgLum === 'number' ? bgLum.toFixed(3) : '无法计算'}`,
            okDarkPalette,
            { id: `${id}-darkpalette`, ...(!okDarkPalette ? { screenshot: await snap(page, `${name}_darkpalette`) } : {}) },
          )
        }

        await page.close()
      }
      await context.close()
      await publicContext.close()
    }
  }
}

/* ------------------------------------------------------------------ */
/* Main                                                               */
/* ------------------------------------------------------------------ */
await mkdir(OUT, { recursive: true })
const browser = await chromium.launch({ headless: true })
let adminContext = null
let adminPage = null
let adminCsrf = null
let adminState = null
const created = {
  serverId: null,
  serverName: `${PREFIX}server`,
  nodeIds: [],
  nodeNames: [],
  groupId: null,
  groupName: `${PREFIX}group`,
  subscriptionId: null,
  subscriptionName: `${PREFIX}sub`,
  memberId: null,
  memberUsername: `${PREFIX}member`,
  memberPassword: `E2eMember-${PREFIX}pass`.slice(0, 60),
  memberNewPassword: `E2eMemberNew-${PREFIX}pass`.slice(0, 60),
  batchId: null,
}
const preexisting = {}
let settingsOriginal = null

try {
  /* ---------------- Interaction 1: login -> logout ---------------- */
  console.log('\n=== 交互 1：登录 / 登出 ===')
  {
    const ctx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 }, locale: 'zh-CN' })
    const page = await ctx.newPage()
    await page.goto(`${BASE}/admin/members`, { waitUntil: 'networkidle' })
    const redirectedToLogin = page.url().includes('/login') && new URL(page.url()).searchParams.get('next') === '/admin/members'
    record(
      '交互1 未登录访问受保护页',
      '跳转 /login 且 next=/admin/members',
      page.url(),
      redirectedToLogin,
    )
    if (!redirectedToLogin) await snap(page, '交互1_未登录跳转')

    const login = await loginAs(page, ADMIN_USER, ADMIN_PASSWORD, { navigate: false })
    if (!login.ok) {
      await recordFailure(page, '交互1 管理员登录', '登录成功并回到 next=/admin/members', `仍在登录页：${login.alert}`)
    } else {
      const landed = page.url().endsWith('/admin/members')
      const text = await mainTextOf(page)
      record(
        '交互1 登录并回跳 next',
        '登录后 URL=/admin/members 且显示成员页',
        `URL=${page.url()}，主内容 ${text.length} 字符`,
        landed && text.includes('成员'),
      )
    }

    // Real logout through the header button.
    const logoutButton = page.getByRole('button', { name: /退出登录|退出中/ }).first()
    if (await logoutButton.count()) {
      await logoutButton.click()
      await page.waitForTimeout(1400)
    }
    record(
      '交互1 登出',
      '登出后 URL 为 /login',
      page.url(),
      page.url().includes('/login'),
    )

    await page.goto(`${BASE}/admin/members`, { waitUntil: 'networkidle' })
    await page.waitForTimeout(400)
    record(
      '交互1 登出后访问受保护页',
      '再次跳转 /login',
      page.url(),
      page.url().includes('/login'),
    )
    await ctx.close()
  }

  /* ---------------- Admin bootstrap ---------------- */
  console.log('\n=== 管理员会话（后续用例复用）===')
  adminContext = await browser.newContext({
    ignoreHTTPSErrors: true,
    viewport: { width: 1440, height: 900 },
    locale: 'zh-CN',
  })
  adminPage = await adminContext.newPage()
  {
    const login = await loginAs(adminPage, ADMIN_USER, ADMIN_PASSWORD)
    if (!login.ok) throw new Error(`admin login failed: ${login.alert}`)
    if (!adminPage.url().match(/\/admin\//)) {
      await adminPage.goto(`${BASE}/admin/overview`, { waitUntil: 'networkidle' })
    }
    adminState = await adminContext.storageState()
    const session = await apiData(adminContext, '/api/v1/auth/session')
    adminCsrf = session.csrf_token
    record('引导 管理员登录', '管理员会话可用', `user=${session.user?.username} role=${session.user?.role}`, session.user?.role === 'admin')
  }

  // Snapshot real ids for the rendering matrix (before interactions mutate data).
  {
    const [servers, nodes, subs, members, batches] = await Promise.all([
      apiPage(adminContext, '/api/v1/admin/servers?limit=200'),
      apiPage(adminContext, '/api/v1/admin/nodes?limit=200'),
      apiPage(adminContext, '/api/v1/admin/subscriptions?limit=200'),
      apiPage(adminContext, '/api/v1/admin/members?limit=200'),
      apiPage(adminContext, '/api/v1/admin/batches?limit=200'),
    ])
    preexisting.server = servers.find((x) => !x.archived)?.id
    preexisting.node = nodes.find((x) => !x.archived)?.id
    preexisting.subscription = subs.find((x) => !x.archived)?.id
    preexisting.member = members.find((x) => !x.archived && x.username === MEMBER_USER)?.id
      || members.find((x) => !x.archived)?.id
    preexisting.batch = batches[0]?.id
    record(
      '引导 读取真实路由 ID',
      '服务器 / 节点 / 订阅 / 成员 / 批次 id 都存在',
      JSON.stringify(preexisting),
      Boolean(preexisting.server && preexisting.node && preexisting.subscription && preexisting.member && preexisting.batch),
    )
  }

  /* ---------------- Rendering matrix ---------------- */
  console.log('\n=== 渲染矩阵：30 路由 x 4 组合 ===')
  await runRenderingMatrix(browser, adminState, preexisting)

  /* ---------------- State matrix ---------------- */
  console.log('\n=== 状态矩阵 ===')
  {
    // Real empty state: templates are currently empty; if someone populated
    // them, use the members filter's real EmptyState instead.
    await adminPage.goto(`${BASE}/admin/templates`, { waitUntil: 'networkidle' })
    await adminPage.waitForTimeout(300)
    const templates = await apiPage(adminContext, '/api/v1/admin/templates?limit=200')
    let expectedEmptyText = '还没有客户端模板'
    let actualEmptyText = ''
    if (!templates.length) {
      actualEmptyText = (await mainTextOf(adminPage)).slice(0, 300)
      record(
        '状态 空列表',
        '真实空列表显示 EmptyState：还没有客户端模板',
        `templates=${templates.length}；${JSON.stringify(actualEmptyText.slice(0, 160))}`,
        actualEmptyText.includes(expectedEmptyText),
      )
      if (!actualEmptyText.includes(expectedEmptyText)) await snap(adminPage, '状态_空列表')
    } else {
      await adminPage.goto(`${BASE}/admin/members`, { waitUntil: 'networkidle' })
      const filter = adminPage.getByLabel('过滤成员')
      await filter.fill(`no-such-member-${PREFIX}`)
      await adminPage.waitForTimeout(300)
      const text = await mainTextOf(adminPage)
      record(
        '状态 空列表',
        '真实空结果显示 EmptyState：当前页没有匹配的成员',
        `templates=${templates.length}（模板非空，改用成员过滤空态）；${JSON.stringify(text.slice(0, 160))}`,
        text.includes('当前页没有匹配的成员'),
      )
      if (!text.includes('当前页没有匹配的成员')) await snap(adminPage, '状态_空列表')
      await filter.fill('')
    }

    // INJECTED loading state: delay one real API response, pass it through.
    const loadingPage = await adminContext.newPage()
    const loadingBucket = { console: [], pageerror: [], requestfailed: [] }
    attachLogs(loadingPage, loadingBucket)
    await loadingPage.route('**/api/v1/admin/members?*', async (route) => {
      await sleep(1800)
      await route.continue()
    })
    try {
      const nav = loadingPage.goto(`${BASE}/admin/members`, { waitUntil: 'domcontentloaded', timeout: 30000 })
      const skeleton = loadingPage.locator('[role="status"]').filter({ hasText: '正在读取成员' }).first()
      const visible = await skeleton.waitFor({ state: 'visible', timeout: 8000 }).then(() => true).catch(() => false)
      record(
        '状态 加载态（INJECTED）',
        '延迟真实 /admin/members 响应时显示 Skeleton / LoadingBlock',
        visible ? '出现 role=status “正在读取成员”' : '未观察到加载态',
        visible,
        { injected: true },
      )
      if (!visible) await snap(loadingPage, '状态_加载态_INJECTED')
      await nav
      await loadingPage.unroute('**/api/v1/admin/members?*')
      await loadingPage.waitForTimeout(500)
    } finally {
      await loadingPage.close()
    }

    // Real error state: a nonexistent batch id produces the panel's 404 envelope.
    await adminPage.goto(`${BASE}/admin/batches/bat_e2e_nonexistent_${Date.now()}`, { waitUntil: 'networkidle' })
    await adminPage.waitForTimeout(500)
    const errText = await adminPage.locator('[role=alert]').allInnerTexts().catch(() => [])
    const joined = errText.join(' | ')
    record(
      '状态 错误态',
      '真实 404 显示 ErrorState + role="alert"',
      JSON.stringify(joined.slice(0, 260)),
      errText.length > 0 && /对象不存在|batch not found|NOT_FOUND/.test(joined),
      joined ? {} : { screenshot: await snap(adminPage, '状态_错误态') },
    )
    if (!(errText.length > 0 && /对象不存在|batch not found|NOT_FOUND/.test(joined))) {
      await snap(adminPage, '状态_错误态')
    }
  }

  /* ---------------- Interaction 2: create server ---------------- */
  console.log('\n=== 交互 2：新建服务器 ===')
  {
    await adminPage.goto(`${BASE}/admin/servers`, { waitUntil: 'networkidle' })
    await adminPage.getByRole('button', { name: '新建服务器' }).first().click()
    const dialog = adminPage.getByRole('dialog').last()
    await dialog.waitFor({ state: 'visible' })
    await dialog.locator('#server-name').fill(created.serverName)
    await dialog.locator('#server-address').fill('127.0.0.1')
    await dialog.locator('#server-region').fill('e2e')
    await dialog.getByRole('button', { name: '保存', exact: true }).click()
    await adminPage.getByRole('dialog').waitFor({ state: 'hidden', timeout: 10000 }).catch(() => {})
    await adminPage.waitForTimeout(900)
    const link = adminPage.getByRole('link', { name: created.serverName, exact: true }).first()
    const visible = await link.isVisible().catch(() => false)
    record(
      '交互2 新建服务器后出现在列表',
      `列表出现 ${created.serverName}`,
      visible ? '列表可见' : '列表未出现',
      visible,
    )
    if (!visible) await snap(adminPage, '交互2_新建服务器')
    if (visible) {
      const href = await link.getAttribute('href')
      const m = href?.match(/\/admin\/servers\/([^/]+)\/runtime/)
      created.serverId = m?.[1] || null
    }
    if (!created.serverId) {
      const servers = await apiPage(adminContext, '/api/v1/admin/servers?limit=200')
      created.serverId = servers.find((x) => x.name === created.serverName)?.id ?? null
    }
    record('交互2 读取服务器 id', '能从列表/API 取到新服务器 id', created.serverId || 'null', Boolean(created.serverId))
  }

  /* ---------------- Interaction 3: five node templates ---------------- */
  console.log('\n=== 交互 3：五个模板新建节点 ===')
  {
    const templates = [
      {
        key: 'vless-reality',
        label: 'VLESS + REALITY',
        name: `${PREFIX}reality`,
        protocol: 'vless',
        port: '443',
        network: 'tcp',
        security: 'reality',
      },
      {
        key: 'vless-ws-tls',
        label: 'VLESS + WebSocket + TLS',
        name: `${PREFIX}ws-tls`,
        protocol: 'vless',
        port: '443',
        network: 'ws',
        security: 'tls',
      },
      {
        key: 'vmess-ws-tls',
        label: 'VMess + WebSocket + TLS',
        name: `${PREFIX}vmess`,
        protocol: 'vmess',
        port: '443',
        network: 'ws',
        security: 'tls',
      },
      {
        key: 'trojan-tls',
        label: 'Trojan + TLS',
        name: `${PREFIX}trojan`,
        protocol: 'trojan',
        port: '443',
        network: 'tcp',
        security: 'tls',
      },
      {
        key: 'ss2022',
        label: 'Shadowsocks 2022',
        name: `${PREFIX}ss2022`,
        protocol: 'shadowsocks',
        port: '8388',
        network: 'tcp',
        security: 'none',
      },
    ]

    for (const tpl of templates) {
      await adminPage.goto(`${BASE}/admin/nodes/new`, { waitUntil: 'networkidle' })
      await adminPage.waitForTimeout(400)
      const expand = adminPage.getByRole('button', { name: '展开', exact: true }).first()
      if (await expand.isVisible().catch(() => false)) await expand.click()
      const templateButton = adminPage.locator('button').filter({ hasText: tpl.label }).first()
      await templateButton.waitFor({ state: 'visible', timeout: 8000 })
      await templateButton.click()
      await adminPage.waitForTimeout(tpl.key === 'vless-reality' ? 900 : 250)

      await adminPage.locator('#node-name').fill(tpl.name)
      await adminPage.locator('#node-host').fill('127.0.0.1')
      await adminPage.locator('#node-server').selectOption(created.serverId)
      // The TLS templates intentionally require a domain; the template copy says
      // the next step is to fill the certificate domain. Provide it so the save
      // exercises the template's complete path instead of stopping at the
      // server's "TLS requires ... server name" validation.
      if (tpl.security === 'tls') {
        await adminPage.locator('#tls-sni').fill(`${tpl.key}.e2e.test`)
        await adminPage.locator('#client-sni').fill(`${tpl.key}.e2e.test`)
      }
      if (tpl.security === 'reality') {
        // REALITY also needs client-facing parameters; the template fills the
        // server-side realitySettings but not client_settings.
        const pub = await adminPage.locator('#reality-public-key').inputValue()
        const shortId = (await adminPage.locator('#reality-short-ids').inputValue()).split(',')[0]?.trim() || ''
        await adminPage.locator('#client-sni').fill('www.microsoft.com')
        await adminPage.locator('#client-public-key').fill(pub)
        await adminPage.locator('#client-short-id').fill(shortId)
        await adminPage.locator('#client-fingerprint').fill('chrome')
      }

      const actual = {
        protocol: await adminPage.locator('#node-protocol').inputValue(),
        port: await adminPage.locator('#node-port').inputValue(),
        network: await adminPage.locator('#stream-network').inputValue(),
        security: await adminPage.locator('#stream-security').inputValue(),
      }
      let keyDetail = ''
      let keyOk = true
      if (tpl.key === 'vless-reality') {
        const privateKey = await adminPage.locator('#reality-private-key').inputValue()
        const publicKey = await adminPage.locator('#reality-public-key').inputValue()
        keyDetail = `privateKey=${privateKey.length} chars, publicKey=${publicKey.length} chars`
        keyOk = privateKey.length > 0 && publicKey.length > 0
      }
      if (tpl.key === 'ss2022') {
        const password = await adminPage.locator('#ss-password').inputValue()
        const serverKey = await adminPage.locator('#node-server-key').inputValue()
        keyDetail = `password=${password.length} chars, serverKey=${serverKey.length} chars`
        keyOk = password.length === 24 && serverKey.length === 24
      }

      const formOk =
        actual.protocol === tpl.protocol &&
        actual.port === tpl.port &&
        actual.network === tpl.network &&
        actual.security === tpl.security &&
        keyOk
      record(
        `交互3 模板 ${tpl.key} 表单值`,
        `protocol=${tpl.protocol}, port=${tpl.port}, network=${tpl.network}, security=${tpl.security}${tpl.key === 'vless-reality' ? '，REALITY 私钥非空' : ''}${tpl.key === 'ss2022' ? '，SS2022 密码 24 字符' : ''}`,
        `${JSON.stringify(actual)}${keyDetail ? `, ${keyDetail}` : ''}`,
        formOk,
      )
      if (!formOk) await snap(adminPage, `交互3_${tpl.key}_表单`)

      await adminPage.getByRole('button', { name: '保存节点', exact: true }).click()
      await adminPage.waitForTimeout(900)

      if (tpl.security === 'tls') {
        // TLS 模板只填占位域名，没有证书文件。产品在这里的正确行为是
        // **阻断保存并明确说明缺什么**（没有证书的核心起不来，与其让它下发
        // 到 Agent 才失败，不如在表单里就拦住）。所以这个用例验的是阻断，
        // 而不是"保存成功"——以前脚本按后者断言，才把正确行为判成 FAIL。
        const blocked = await adminPage
          .getByText('TLS 证书文件未配置', { exact: false })
          .first()
          .isVisible()
          .catch(() => false)
        const stayed = adminPage.url().includes('/admin/nodes/new')
        record(
          `交互3 模板 ${tpl.key} 缺证书被阻断`,
          'TLS 模板未填证书时阻止保存，并提示需要「证书文件路径」/「私钥文件路径」',
          `停在表单=${stayed}，出现证书提示=${blocked}`,
          blocked && stayed,
        )
        if (!(blocked && stayed)) await snap(adminPage, `交互3_${tpl.key}_缺证书`)

        // 补上证书路径后必须能保存成功，否则阻断就成了死路。
        await adminPage.locator('#tls-cert-file').fill('/etc/proxypanel/tls/e2e-cert.pem')
        await adminPage.locator('#tls-key').fill('/etc/proxypanel/tls/e2e-key.pem')
        await adminPage.getByRole('button', { name: '保存节点', exact: true }).click()
        await adminPage.waitForURL(/\/admin\/nodes$/, { timeout: 15000 }).catch(() => {})
        await adminPage.waitForTimeout(600)
      }

      await adminPage.waitForURL(/\/admin\/nodes$/, { timeout: 5000 }).catch(() => {})
      const inList = await adminPage.getByText(tpl.name, { exact: true }).first().isVisible().catch(() => false)
      record(
        `交互3 模板 ${tpl.key} 保存`,
        `保存后回到节点列表并出现 ${tpl.name}`,
        `URL=${adminPage.url()}，列表可见=${inList}`,
        adminPage.url().endsWith('/admin/nodes') && inList,
      )
      if (!inList) await snap(adminPage, `交互3_${tpl.key}_保存`)

      const nodes = await apiPage(adminContext, '/api/v1/admin/nodes?limit=200')
      const recordNode = nodes.find((x) => x.name === tpl.name)
      if (recordNode) {
        created.nodeIds.push(recordNode.id)
        created.nodeNames.push(recordNode.name)
        const apiOk =
          recordNode.protocol === tpl.protocol &&
          Number(recordNode.port) === Number(tpl.port) &&
          String(recordNode.stream_settings?.network) === tpl.network &&
          String(recordNode.stream_settings?.security || 'none') === tpl.security
        record(
          `交互3 模板 ${tpl.key} 服务端回读`,
          '服务端记录中的协议/端口/传输/加密层与模板一致',
          `protocol=${recordNode.protocol}, port=${recordNode.port}, network=${recordNode.stream_settings?.network}, security=${recordNode.stream_settings?.security || 'none'}`,
          apiOk,
        )
      } else {
        record(
          `交互3 模板 ${tpl.key} 服务端回读`,
          '能在 API 列表中找到刚创建的节点',
          '未找到',
          false,
        )
      }
    }
  }

  /* ---------------- Interaction 4: field validation ---------------- */
  console.log('\n=== 交互 4：节点字段级校验 ===')
  {
    await adminPage.goto(`${BASE}/admin/nodes/new`, { waitUntil: 'networkidle' })
    await adminPage.waitForTimeout(400)
    await adminPage.locator('#node-name').fill('')
    await adminPage.locator('#node-port').fill('70000')
    await adminPage.waitForTimeout(400)
    const nameInvalid = (await adminPage.locator('#node-name').getAttribute('aria-invalid')) === 'true'
    const portInvalid = (await adminPage.locator('#node-port').getAttribute('aria-invalid')) === 'true'
    const inlineName = await adminPage.getByText('节点名称必填', { exact: true }).first().isVisible().catch(() => false)
    const inlinePort = await adminPage.getByText('端口必须是 1–65535 的整数', { exact: true }).first().isVisible().catch(() => false)
    record(
      '交互4 空名称 + 非法端口',
      'aria-invalid=true 且输入框下方显示就地错误文案',
      `name aria-invalid=${nameInvalid}, port aria-invalid=${portInvalid}, 名称文案=${inlineName}, 端口文案=${inlinePort}`,
      nameInvalid && portInvalid && inlineName && inlinePort,
    )
    if (!(nameInvalid && portInvalid && inlineName && inlinePort)) await snap(adminPage, '交互4_字段校验')
  }

  /* ---------------- Interaction 4b: 对外端口 ---------------- */
  console.log('\n=== 交互 4b：节点对外端口 ===')
  {
    await adminPage.goto(`${BASE}/admin/nodes/new`, { waitUntil: 'networkidle' })
    await adminPage.waitForTimeout(400)

    // 两个端口字段的语义必须能从界面读出来：监听端口=本机实际监听，
    // 对外端口=成员客户端连接的端口（可留空表示相同）。
    const listenLabel = await adminPage.getByText('监听端口', { exact: true }).first().isVisible().catch(() => false)
    const externalLabel = await adminPage.getByText('对外端口（可选）', { exact: true }).first().isVisible().catch(() => false)
    // 留空含义写在 input 的 placeholder 里（不是 text node，getByText 抓不到），
    // 同时字段下方有一段说明文案。两者任一存在即可。
    const placeholder = await adminPage.locator('#node-external-port').getAttribute('placeholder')
    const hint =
      Boolean(placeholder && placeholder.includes('留空')) ||
      (await adminPage
        .getByText('成员客户端实际连接的端口', { exact: false })
        .first()
        .isVisible()
        .catch(() => false))
    record(
      '交互4b 字段语义可读',
      '界面区分「监听端口」与「对外端口（可选）」，并说明留空含义',
      `监听端口=${listenLabel}, 对外端口=${externalLabel}, 留空提示=${hint}`,
      listenLabel && externalLabel && hint,
    )

    // 实时校验：对外端口必须 1-65535。
    await adminPage.locator('#node-external-port').fill('70000')
    await adminPage.waitForTimeout(400)
    const badInvalid = (await adminPage.locator('#node-external-port').getAttribute('aria-invalid')) === 'true'
    record(
      '交互4b 对外端口越界校验',
      '填 70000 时 aria-invalid=true 并显示就地错误',
      `aria-invalid=${badInvalid}`,
      badInvalid,
    )

    if (!badInvalid) await snap(adminPage, '交互4b_对外端口越界')

    // 真实保存 + 服务端回读。
    const epName = `e2e-外部端口-${Date.now().toString().slice(-6)}`
    await adminPage.locator('#node-external-port').fill('')
    await adminPage.locator('#node-name').fill(epName)
    await adminPage.locator('#node-host').fill('relay.e2e.test')
    await adminPage.locator('#node-server').selectOption(created.serverId)
    await adminPage.locator('#node-port').fill('20199')
    await adminPage.locator('#node-external-port').fill('443')
    await adminPage.getByRole('button', { name: /保存|创建/ }).first().click()
    await adminPage.waitForTimeout(1500)

    const epNodes = await apiPage(adminContext, '/api/v1/admin/nodes?limit=200')
    const epNode = epNodes.find((x) => x.name === epName)
    const readBack = epNode ? Number(epNode.external_port) : null
    record(
      '交互4b 对外端口保存',
      '界面上填的 443 写入服务端，监听端口仍为 20199',
      `external_port=${readBack}, port=${epNode?.port}`,
      Boolean(epNode) && readBack === 443 && Number(epNode.port) === 20199,
    )
    if (epNode) {
      created.nodeIds.push(epNode.id)
      created.nodeNames.push(epNode.name)
    }
    if (!(epNode && readBack === 443)) await snap(adminPage, '交互4b_对外端口保存')
  }

  /* ---------------- Interactions 5 & 6: group -> subscription -> member ---------------- */
  console.log('\n=== 交互 5/6：节点组 → 订阅 → 成员 ===')
  {
    // Create group with the first template node.
    await adminPage.goto(`${BASE}/admin/node-groups`, { waitUntil: 'networkidle' })
    await adminPage.getByRole('button', { name: '新建节点组' }).first().click()
    let dialog = adminPage.getByRole('dialog').last()
    await dialog.waitFor({ state: 'visible' })
    await dialog.locator('#group-name').fill(created.groupName)
    await dialog.locator('div.cursor-pointer').filter({ hasText: created.nodeNames[0] }).first().click()
    await adminPage.waitForTimeout(300)
    const countOne = (await dialog.innerText()).includes('包含节点（1）')
    await dialog.getByRole('button', { name: '保存', exact: true }).click()
    await adminPage.getByRole('dialog').waitFor({ state: 'hidden', timeout: 10000 }).catch(() => {})
    await adminPage.waitForTimeout(800)
    record(
      '交互5 新建节点组并挂 1 个节点',
      '弹窗计数变为 1，保存后列表出现组名',
      `弹窗计数=${countOne}，列表可见=${await adminPage.getByText(created.groupName, { exact: true }).first().isVisible().catch(() => false)}`,
      countOne && await adminPage.getByText(created.groupName, { exact: true }).first().isVisible().catch(() => false),
    )
    if (!countOne) await snap(adminPage, '交互5_新建节点组')

    const groups = await apiPage(adminContext, '/api/v1/admin/node-groups?limit=200')
    created.groupId = groups.find((x) => x.name === created.groupName)?.id ?? null
    record('交互5 读取节点组 id', 'API 中存在新节点组', created.groupId || 'null', Boolean(created.groupId))

    // Create subscription referencing the group.
    await adminPage.goto(`${BASE}/admin/subscriptions/new`, { waitUntil: 'networkidle' })
    await adminPage.waitForTimeout(500)
    await adminPage.locator('#sub-name').fill(created.subscriptionName)
    await adminPage.locator('div.cursor-pointer').filter({ hasText: created.groupName }).first().click()
    await adminPage.locator('#sub-quota').fill('10')
    await adminPage.locator('#sub-valid-days').fill('30')
    await adminPage.getByRole('button', { name: '保存订阅', exact: true }).click()
    await adminPage.waitForURL(/\/admin\/subscriptions$/, { timeout: 15000 }).catch(() => {})
    await adminPage.waitForTimeout(700)
    const subVisible = await adminPage.getByText(created.subscriptionName, { exact: true }).first().isVisible().catch(() => false)
    record(
      '交互6 新建订阅',
      '保存后订阅列表出现新订阅',
      `URL=${adminPage.url()}，列表可见=${subVisible}`,
      adminPage.url().endsWith('/admin/subscriptions') && subVisible,
    )
    if (!subVisible) await snap(adminPage, '交互6_新建订阅')

    const subs = await apiPage(adminContext, '/api/v1/admin/subscriptions?limit=200')
    const sub = subs.find((x) => x.name === created.subscriptionName)
    created.subscriptionId = sub?.id ?? null
    record(
      '交互6 订阅绑定节点组',
      '订阅的 node_group_ids 含刚创建的节点组',
      JSON.stringify(sub?.node_group_ids ?? []),
      Boolean(sub && Array.isArray(sub.node_group_ids) && sub.node_group_ids.includes(created.groupId)),
    )

    // Create member bound to the subscription through the real form.
    await adminPage.goto(`${BASE}/admin/members`, { waitUntil: 'networkidle' })
    await adminPage.getByRole('button', { name: '新建成员' }).first().click()
    dialog = adminPage.getByRole('dialog').last()
    await dialog.waitFor({ state: 'visible' })
    await dialog.locator('#member-username').fill(created.memberUsername)
    await dialog.locator('#member-password').fill(created.memberPassword)
    await dialog.locator('#member-sub').selectOption(created.subscriptionId)
    await dialog.getByRole('button', { name: '保存', exact: true }).click()
    await adminPage.getByRole('dialog').waitFor({ state: 'hidden', timeout: 10000 }).catch(() => {})
    await adminPage.waitForTimeout(900)
    const memberRow = adminPage.locator('table tbody tr').filter({ hasText: created.memberUsername }).first()
    const rowVisible = await memberRow.isVisible().catch(() => false)
    const rowText = rowVisible ? await memberRow.innerText() : ''
    record(
      '交互6 新建成员并绑定订阅',
      '成员列表出现该成员，且订阅列显示订阅名',
      `行可见=${rowVisible}，行文本=${JSON.stringify(rowText.slice(0, 220))}`,
      rowVisible && rowText.includes(created.subscriptionName),
    )
    if (!(rowVisible && rowText.includes(created.subscriptionName))) await snap(adminPage, '交互6_成员绑定订阅')

    const members = await apiPage(adminContext, '/api/v1/admin/members?limit=200')
    const member = members.find((x) => x.username === created.memberUsername)
    created.memberId = member?.id ?? null
    record(
      '交互6 成员服务端绑定',
      '成员 subscription_id 等于新订阅 id',
      member?.subscription_id || 'null',
      Boolean(member && member.subscription_id === created.subscriptionId),
    )

    // Now edit the group (referenced by subscription) -> real preview flow.
    await adminPage.goto(`${BASE}/admin/node-groups`, { waitUntil: 'networkidle' })
    const groupRow = adminPage.locator('table tbody tr').filter({ hasText: created.groupName }).first()
    await groupRow.getByRole('button', { name: '编辑', exact: true }).click()
    dialog = adminPage.getByRole('dialog').last()
    await dialog.waitFor({ state: 'visible' })
    await dialog.locator('div.cursor-pointer').filter({ hasText: created.nodeNames[1] }).first().click()
    await adminPage.waitForTimeout(300)
    const countTwo = (await dialog.innerText()).includes('包含节点（2）')
    await dialog.getByRole('button', { name: '保存', exact: true }).click()
    const preview = adminPage.getByRole('dialog').filter({ hasText: '节点组成员变更影响预览' }).last()
    const previewVisible = await preview.waitFor({ state: 'visible', timeout: 15000 }).then(() => true).catch(() => false)
    let previewText = ''
    if (previewVisible) {
      await preview.getByText(/预览结果（未提交）/).first().waitFor({ state: 'visible', timeout: 10000 }).catch(() => {})
      previewText = await preview.innerText().catch(() => '')
    }
    record(
      '交互5 被订阅引用的节点组触发预览',
      'PATCH 返回 PREVIEW_REQUIRED 后弹出节点组影响预览',
      `弹窗可见=${previewVisible}，文本=${JSON.stringify(previewText.slice(0, 260))}`,
      previewVisible && /影响预览|预览结果/.test(previewText),
    )
    if (!previewVisible) await snap(adminPage, '交互5_节点组预览')

    if (previewVisible) {
      await preview.getByRole('checkbox').first().check().catch(async () => {
        await preview.getByText(/我已确认以上影响范围/).first().click()
      })
      await preview.getByRole('button', { name: '确认提交', exact: true }).click()
      await adminPage.waitForURL(/\/admin\/batches\//, { timeout: 20000 }).catch(() => {})
      await adminPage.waitForTimeout(800)
      record(
        '交互5 预览后提交',
        '提交成功并进入批量任务详情',
        adminPage.url(),
        /\/admin\/batches\/bat_/.test(adminPage.url()),
      )
      if (!/\/admin\/batches\/bat_/.test(adminPage.url())) await snap(adminPage, '交互5_预览提交')
    }

    const groupAfter = await apiData(adminContext, `/api/v1/admin/node-groups/${created.groupId}`)
    record(
      '交互5 组内节点数',
      '预览提交后节点组包含 2 个节点',
      `node_ids=${JSON.stringify(groupAfter?.node_ids ?? [])}`,
      Array.isArray(groupAfter?.node_ids) && groupAfter.node_ids.length === 2,
    )
    if (countTwo === false) await snap(adminPage, '交互5_组节点计数')
  }

  /* ---------------- Interaction 7: quota override ---------------- */
  console.log('\n=== 交互 7：成员配额覆盖切换 ===')
  {
    await adminPage.goto(`${BASE}/admin/members`, { waitUntil: 'networkidle' })
    const row = adminPage.locator('table tbody tr').filter({ hasText: created.memberUsername }).first()
    await row.getByRole('button', { name: '编辑', exact: true }).click()
    const dialog = adminPage.getByRole('dialog').last()
    await dialog.waitFor({ state: 'visible' })
    await dialog.getByText('单独给这个人设置', { exact: true }).first().click()
    await adminPage.waitForTimeout(300)
    const radioChecked = (await adminPage.locator('#member-quota-override').getAttribute('aria-checked')) === 'true'
    const inputVisible = await adminPage.locator('#member-override').isVisible().catch(() => false)
    const placeholder = await adminPage.locator('#member-override').getAttribute('placeholder').catch(() => '')
    const hintVisible = await dialog.getByText(/留空表示不限量/).first().isVisible().catch(() => false)
    record(
      '交互7 配额覆盖单选生效',
      '选择“单独设置”后 radio 选中，出现额度输入框',
      `aria-checked=${radioChecked}, inputVisible=${inputVisible}`,
      radioChecked && inputVisible,
    )
    record(
      '交互7 留空提示可见',
      '输入框 placeholder=“留空表示不限量”，并有留空说明',
      `placeholder=${JSON.stringify(placeholder)}, hint=${hintVisible}`,
      placeholder.includes('留空表示不限量') && hintVisible,
    )
    if (!(radioChecked && inputVisible && placeholder.includes('留空表示不限量'))) await snap(adminPage, '交互7_配额覆盖')

    // Empty override must be blocked with an inline form error.
    await dialog.getByRole('button', { name: '保存', exact: true }).click()
    await adminPage.waitForTimeout(400)
    const errs = await dialog.locator('[role=alert]').allInnerTexts().catch(() => [])
    const emptyBlocked = errs.join(' | ').includes('配额覆盖必须填写大于 0 的 GiB 数值')
    record(
      '交互7 覆盖留空被就地拦截',
      '保存时提示“配额覆盖必须填写大于 0 的 GiB 数值”',
      JSON.stringify(errs.join(' | ').slice(0, 220)),
      emptyBlocked,
    )
    if (!emptyBlocked) await snap(adminPage, '交互7_留空拦截')

    await adminPage.locator('#member-override').fill('5')
    await dialog.getByRole('button', { name: '保存', exact: true }).click()
    await adminPage.getByRole('dialog').waitFor({ state: 'hidden', timeout: 10000 }).catch(() => {})
    await adminPage.waitForTimeout(900)
    const members = await apiPage(adminContext, '/api/v1/admin/members?limit=200')
    const member = members.find((x) => x.username === created.memberUsername)
    record(
      '交互7 覆盖值持久化',
      '服务端 quota_override_enabled=true 且 quota_override_bytes=5 GiB',
      `enabled=${member?.quota_override_enabled}, bytes=${member?.quota_override_bytes}`,
      member?.quota_override_enabled === true && member?.quota_override_bytes === 5 * 1024 ** 3,
    )
  }

  /* ---------------- Interaction 8: batch preview -> submit ---------------- */
  console.log('\n=== 交互 8：成员批量操作 ===')
  {
    await adminPage.goto(`${BASE}/admin/members`, { waitUntil: 'networkidle' })
    await adminPage.getByRole('checkbox', { name: `选择 ${created.memberUsername}` }).check()
    await adminPage.getByRole('button', { name: /批量操作/ }).first().click()
    let dialog = adminPage.getByRole('dialog').last()
    await dialog.waitFor({ state: 'visible' })
    await dialog.locator('#batch-op').selectOption('clear_override')
    await dialog.getByRole('button', { name: '生成预览', exact: true }).click()
    const preview = adminPage.getByRole('dialog').filter({ hasText: '批量影响预览' }).last()
    const previewVisible = await preview.waitFor({ state: 'visible', timeout: 15000 }).then(() => true).catch(() => false)
    if (previewVisible) {
      // 加载中就已经有「重新预览」「取消」按钮了，用它们当就绪信号会立刻返回、
      // 读到的是 "正在请求影响预览"。必须等 loading 文案消失、表头真正出现。
      await preview
        .getByText('正在请求影响预览', { exact: false })
        .first()
        .waitFor({ state: 'hidden', timeout: 20000 })
        .catch(() => {})
      await preview
        .getByText('逐个成员的影响', { exact: false })
        .first()
        .waitFor({ state: 'visible', timeout: 20000 })
        .catch(() => {})
    }
    const previewText = previewVisible ? await preview.innerText().catch(() => '') : ''
    const hasPerMember = previewText.includes('逐个成员的影响') && previewText.includes('会发生什么')
    const hasMember = previewText.includes(created.memberUsername)
    const hasConsequence = /配额|覆盖|清除|订阅|额度/.test(previewText)
    record(
      '交互8 批量预览弹窗',
      '预览出现「逐个成员的影响」表格，含成员名与“会发生什么”',
      `可见=${previewVisible}, 表头=${hasPerMember}, 成员=${hasMember}, 后果文本=${hasConsequence}；${JSON.stringify(previewText.slice(0, 400))}`,
      previewVisible && hasPerMember && hasMember && hasConsequence,
    )
    if (!(previewVisible && hasPerMember && hasMember && hasConsequence)) {
      await snap(adminPage, '交互8_批量预览')
    }

    if (previewVisible) {
      await preview.getByRole('checkbox').first().check().catch(async () => {
        await preview.getByText(/我已确认以上影响范围/).first().click()
      })
      await preview.getByRole('button', { name: '确认提交', exact: true }).click()
      await adminPage.waitForURL(/\/admin\/batches\//, { timeout: 20000 }).catch(() => {})
      await adminPage.waitForTimeout(800)
      const m = adminPage.url().match(/\/admin\/batches\/([^/?#]+)/)
      created.batchId = m?.[1] ? decodeURIComponent(m[1]) : null
      record(
        '交互8 批量提交',
        '提交后进入批量任务详情，批次状态可读',
        `URL=${adminPage.url()}，batch=${created.batchId}`,
        Boolean(created.batchId),
      )
      if (!created.batchId) await snap(adminPage, '交互8_批量提交')
      if (created.batchId) {
        const batch = await apiData(adminContext, `/api/v1/admin/batches/${encodeURIComponent(created.batchId)}`)
        record(
          '交互8 批次服务端状态',
          '批次存在且 state 非空',
          `state=${batch?.state}, operation=${batch?.operation}`,
          Boolean(batch?.state),
        )
      }
    }
  }

  /* ---------------- Interaction 9: credential rotation dialog ---------------- */
  console.log('\n=== 交互 9：凭据轮换弹窗 ===')
  {
    await adminPage.goto(`${BASE}/admin/members`, { waitUntil: 'networkidle' })
    const row = adminPage.locator('table tbody tr').filter({ hasText: created.memberUsername }).first()
    await row.getByRole('button', { name: '凭据', exact: true }).click()
    const dialog = adminPage.getByRole('dialog').last()
    await dialog.waitFor({ state: 'visible', timeout: 8000 }).catch(() => {})
    const text = await dialog.innerText().catch(() => '')
    const hasTitle = text.includes('代理凭据轮换')
    const hasConfirm = await dialog.getByRole('button', { name: '确认轮换', exact: true }).isVisible().catch(() => false)
    const hasWarning = /旧凭据立即失效|硬撤权/.test(text)
    record(
      '交互9 凭据轮换弹窗打开',
      '弹窗标题、确认按钮与后果说明都在',
      `标题=${hasTitle}, 确认=${hasConfirm}, 后果=${hasWarning}`,
      hasTitle && hasConfirm && hasWarning,
    )
    if (!(hasTitle && hasConfirm && hasWarning)) await snap(adminPage, '交互9_凭据弹窗')

    await adminPage.keyboard.press('Escape')
    await adminPage.waitForTimeout(500)
    const closed = (await adminPage.getByRole('dialog').count()) === 0
    record(
      '交互9 Esc 关闭弹窗',
      '按 Esc 后弹窗从 DOM 移除',
      closed ? '已关闭' : `仍有 ${await adminPage.getByRole('dialog').count()} 个 dialog`,
      closed,
    )
    if (!closed) await snap(adminPage, '交互9_Esc关闭')
  }

  /* ---------------- Interaction 10: settings retention persistence ---------------- */
  console.log('\n=== 交互 10：设置保留天数持久化 ===')
  {
    const current = await apiData(adminContext, '/api/v1/admin/settings')
    settingsOriginal = Number(current.retention_days)
    const next = settingsOriginal === 30 ? 31 : 30
    await adminPage.goto(`${BASE}/admin/settings`, { waitUntil: 'networkidle' })
    await adminPage.waitForTimeout(500)
    await adminPage.locator('#settings-retention').fill(String(next))
    const put = adminPage.waitForResponse(
      (resp) => resp.url().includes('/api/v1/admin/settings') && resp.request().method() === 'PUT',
      { timeout: 15000 },
    ).catch(() => null)
    await adminPage.getByRole('button', { name: '保存设置', exact: true }).click()
    const resp = await put
    await adminPage.waitForTimeout(900)
    const afterSave = await apiData(adminContext, '/api/v1/admin/settings')
    record(
      '交互10 保存保留天数',
      `PUT 成功且服务端 retention_days=${next}`,
      `PUT HTTP ${resp?.status() ?? '无响应'}，服务端=${afterSave.retention_days}`,
      resp?.status() === 200 && Number(afterSave.retention_days) === next,
    )

    await adminPage.reload({ waitUntil: 'networkidle' })
    await adminPage.waitForTimeout(600)
    const afterReload = await adminPage.locator('#settings-retention').inputValue()
    record(
      '交互10 刷新后持久化',
      `刷新页面后输入框仍为 ${next}`,
      `输入框=${afterReload}`,
      String(afterReload) === String(next),
    )
    if (String(afterReload) !== String(next)) await snap(adminPage, '交互10_持久化')

    // Restore the real original value and verify the restore.
    await adminPage.locator('#settings-retention').fill(String(settingsOriginal))
    const restore = adminPage.waitForResponse(
      (resp) => resp.url().includes('/api/v1/admin/settings') && resp.request().method() === 'PUT',
      { timeout: 15000 },
    ).catch(() => null)
    await adminPage.getByRole('button', { name: '保存设置', exact: true }).click()
    await restore
    await adminPage.waitForTimeout(800)
    const restored = await apiData(adminContext, '/api/v1/admin/settings')
    record(
      '交互10 恢复原保留天数',
      `恢复为原值 ${settingsOriginal}`,
      `服务端=${restored.retention_days}`,
      Number(restored.retention_days) === settingsOriginal,
    )
  }

  /* ---------------- Interaction 11: change password ---------------- */
  console.log('\n=== 交互 11：临时成员改密码 ===')
  {
    const memberCtx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 }, locale: 'zh-CN' })
    const memberPage = await memberCtx.newPage()
    let changed = false
    try {
      const login = await loginAs(memberPage, created.memberUsername, created.memberPassword)
      record(
        '交互11 临时成员初始密码可登录',
        '登录成功并进入成员端页面',
        `URL=${memberPage.url()}${login.ok ? '' : `，${login.alert}`}`,
        login.ok,
      )
      if (login.ok) {
        await memberPage.goto(`${BASE}/me/security`, { waitUntil: 'networkidle' })
        await memberPage.locator('#pw-current').fill(created.memberPassword)
        await memberPage.locator('#pw-next').fill(created.memberNewPassword)
        await memberPage.locator('#pw-confirm').fill(created.memberNewPassword)
        const pwResp = memberPage.waitForResponse(
          (resp) => resp.url().includes('/api/v1/me/password') && resp.request().method() === 'POST',
          { timeout: 15000 },
        ).catch(() => null)
        await memberPage.getByRole('button', { name: '更新密码', exact: true }).click()
        const resp = await pwResp
        await memberPage.waitForTimeout(900)
        const alerts = await memberPage.locator('[role=alert]').allInnerTexts().catch(() => [])
        const cleared = (await memberPage.locator('#pw-current').inputValue()) === ''
        changed = resp?.status() === 200 && alerts.length === 0
        record(
          '交互11 成员改密码',
          'POST /me/password 200，无错误提示，表单清空',
          `HTTP ${resp?.status() ?? '无响应'}，alerts=${JSON.stringify(alerts.join(' | ').slice(0, 200))}，清空=${cleared}`,
          changed && cleared,
        )
        if (!(changed && cleared)) await snap(memberPage, '交互11_改密码')
      }
    } finally {
      await memberCtx.close()
    }

    if (changed) {
      // New password must work.
      const newCtx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 }, locale: 'zh-CN' })
      const newPage = await newCtx.newPage()
      try {
        const login = await loginAs(newPage, created.memberUsername, created.memberNewPassword)
        record(
          '交互11 新密码登录',
          '新密码登录成功进入成员端',
          `URL=${newPage.url()}${login.ok ? '' : `，${login.alert}`}`,
          login.ok && newPage.url().includes('/me/'),
        )
        if (!login.ok) await snap(newPage, '交互11_新密码登录')
      } finally {
        await newCtx.close()
      }

      // Old password must be rejected with a real 401.
      const oldCtx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 }, locale: 'zh-CN' })
      const oldPage = await oldCtx.newPage()
      try {
        const res = await apiCall(oldCtx, '/api/v1/auth/login', {
          method: 'POST',
          body: { username: created.memberUsername, password: created.memberPassword },
        })
        record(
          '交互11 旧密码被拒绝（API 401）',
          '旧密码登录返回 HTTP 401',
          `HTTP ${res.status}，${JSON.stringify(res.body).slice(0, 200)}`,
          res.status === 401,
        )

        await fillAndSubmitLogin(oldPage, created.memberUsername, created.memberPassword)
        const oldAlert = (await oldPage.locator('[role=alert]').allInnerTexts().catch(() => [])).join(' | ')
        record(
          '交互11 旧密码被拒绝（UI）',
          'UI 仍停在 /login 并显示错误提示',
          `URL=${oldPage.url()}，提示=${JSON.stringify(oldAlert.slice(0, 200))}`,
          oldPage.url().includes('/login') && oldAlert.length > 0,
        )
      } finally {
        await oldCtx.close()
      }
    }
  }

  /* ---------------- Interaction 12: theme toggle ---------------- */
  console.log('\n=== 交互 12：主题切换 ===')
  {
    await adminPage.goto(`${BASE}/admin/overview`, { waitUntil: 'networkidle' })
    await adminPage.waitForTimeout(300)
    const before = await adminPage.evaluate(() => ({
      theme: document.documentElement.dataset.theme,
      cls: document.documentElement.className,
      stored: localStorage.getItem('pp.theme'),
      bg: getComputedStyle(document.body).backgroundColor,
      fg: getComputedStyle(document.body).color,
    }))
    await adminPage.getByRole('button', { name: '切换主题' }).click()
    await adminPage.waitForTimeout(500)
    const after = await adminPage.evaluate(() => ({
      theme: document.documentElement.dataset.theme,
      cls: document.documentElement.className,
      stored: localStorage.getItem('pp.theme'),
      bg: getComputedStyle(document.body).backgroundColor,
      fg: getComputedStyle(document.body).color,
    }))
    const target = before.theme === 'dark' ? 'light' : 'dark'
    record(
      '交互12 主题切换',
      `点击后 html 主题标记变为 ${target}（实现用 data-theme，不是 class）`,
      `before=${JSON.stringify(before)}，after=${JSON.stringify(after)}`,
      after.theme === target && after.stored === target,
      { note: '任务文字写的是 html class；当前实现使用 html[data-theme]。' },
    )
    const colorChanged = before.bg !== after.bg || before.fg !== after.fg
    const colorShot = colorChanged ? null : await snap(adminPage, '交互12_主题实际配色')
    record(
      '交互12 主题切换的实际配色',
      '切换主题后 body 背景色与前景色发生变化（不只是 data-theme 标记）',
      `before bg=${before.bg}, fg=${before.fg}；after bg=${after.bg}, fg=${after.fg}`,
      colorChanged,
      { note: '这是深色主题是否真正生效的直接检查。', ...(colorShot ? { screenshot: colorShot } : {}) },
    )
    if (after.theme !== target) await snap(adminPage, '交互12_主题切换')

    await adminPage.reload({ waitUntil: 'networkidle' })
    await adminPage.waitForTimeout(500)
    const reloaded = await adminPage.evaluate(() => ({
      theme: document.documentElement.dataset.theme,
      stored: localStorage.getItem('pp.theme'),
    }))
    record(
      '交互12 主题刷新后保持',
      `刷新后仍为 ${target}`,
      JSON.stringify(reloaded),
      reloaded.theme === target && reloaded.stored === target,
    )
    if (reloaded.theme !== target) await snap(adminPage, '交互12_主题保持')

    // Restore the original theme.
    if (reloaded.theme !== before.theme) {
      await adminPage.getByRole('button', { name: '切换主题' }).click()
      await adminPage.waitForTimeout(400)
    }
  }

  /* ---------------- Interaction 13: mobile drawer ---------------- */
  console.log('\n=== 交互 13：移动端汉堡菜单 ===')
  {
    const mobileCtx = await browser.newContext({
      ignoreHTTPSErrors: true,
      viewport: { width: 390, height: 844 },
      locale: 'zh-CN',
      storageState: adminState,
    })
    const mobile = await mobileCtx.newPage()
    try {
      await mobile.goto(`${BASE}/admin/overview`, { waitUntil: 'networkidle' })
      const menu = mobile.locator('button[aria-label="打开导航"]')
      const menuVisible = await menu.isVisible().catch(() => false)
      const sidebarHidden = !(await mobile.locator('#app-sidebar').isVisible().catch(() => true))
      record(
        '交互13 390px 汉堡按钮可见',
        '移动端显示汉堡按钮，桌面侧栏隐藏',
        `汉堡=${menuVisible}, 侧栏隐藏=${sidebarHidden}`,
        menuVisible && sidebarHidden,
      )
      if (!(menuVisible && sidebarHidden)) await snap(mobile, '交互13_汉堡按钮')

      await menu.click()
      await mobile.waitForTimeout(400)
      const sheet = mobile.getByRole('dialog').first()
      const openVisible = await sheet.isVisible().catch(() => false)
      const navLink = await sheet.getByRole('link', { name: '设置', exact: true }).isVisible().catch(() => false)
      const expanded = (await menu.getAttribute('aria-expanded')) === 'true'
      record(
        '交互13 打开抽屉',
        '抽屉可见、包含导航链接、aria-expanded=true',
        `可见=${openVisible}, 设置链接=${navLink}, expanded=${expanded}`,
        openVisible && navLink && expanded,
      )
      if (!(openVisible && navLink)) await snap(mobile, '交互13_打开抽屉')

      await mobile.keyboard.press('Escape')
      await mobile.waitForTimeout(500)
      const closedByEsc = (await mobile.getByRole('dialog').count()) === 0
      record('交互13 Esc 关闭抽屉', '按 Esc 后抽屉关闭', closedByEsc ? '已关闭' : '仍打开', closedByEsc)
      if (!closedByEsc) await snap(mobile, '交互13_Esc关闭抽屉')

      await menu.click()
      await mobile.waitForTimeout(400)
      // The overlay is the full-screen layer; the sheet is 288px wide on the left.
      await mobile.mouse.click(370, 420)
      await mobile.waitForTimeout(500)
      const closedByOverlay = (await mobile.getByRole('dialog').count()) === 0
      record('交互13 点遮罩关闭抽屉', '点击抽屉外遮罩后关闭', closedByOverlay ? '已关闭' : '仍打开', closedByOverlay)
      if (!closedByOverlay) await snap(mobile, '交互13_遮罩关闭')
    } finally {
      await mobileCtx.close()
    }
  }

  /* ---------------- Interaction 14: browser back ---------------- */
  console.log('\n=== 交互 14：浏览器后退 ===')
  {
    await adminPage.goto(`${BASE}/admin/members`, { waitUntil: 'networkidle' })
    await adminPage.waitForTimeout(300)
    await adminPage.getByRole('link', { name: '设置', exact: true }).first().click()
    await adminPage.waitForURL(/\/admin\/settings$/, { timeout: 10000 }).catch(() => {})
    await adminPage.waitForTimeout(500)
    const settingsOk = adminPage.url().endsWith('/admin/settings') && (await mainTextOf(adminPage)).includes('设置')
    await adminPage.goBack()
    await adminPage.waitForTimeout(900)
    const backText = await mainTextOf(adminPage)
    const backOk = adminPage.url().endsWith('/admin/members') && backText.includes('成员')
    record(
      '交互14 前进到设置再后退',
      'URL 回到 /admin/members，内容为成员页',
      `前进=${settingsOk ? 'ok' : 'bad'}，后退 URL=${adminPage.url()}，内容=${JSON.stringify(backText.slice(0, 80))}`,
      settingsOk && backOk,
    )
    if (!(settingsOk && backOk)) await snap(adminPage, '交互14_浏览器后退')
  }

  /* ---------------- Permission matrix ---------------- */
  console.log('\n=== 权限矩阵（UI）===')
  {
    const memberCtx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 }, locale: 'zh-CN' })
    const memberPage = await memberCtx.newPage()
    try {
      const login = await loginAs(memberPage, MEMBER_USER, MEMBER_PASSWORD)
      record(
        '权限 成员登录',
        `${MEMBER_USER} 能登录并进入 /me/subscription`,
        `URL=${memberPage.url()}${login.ok ? '' : `，${login.alert}`}`,
        login.ok && memberPage.url().includes('/me/subscription'),
      )

      const adminPaths = [
        '/admin/overview',
        '/admin/servers',
        '/admin/nodes',
        '/admin/node-groups',
        '/admin/subscriptions',
        '/admin/members',
        '/admin/invitations',
        '/admin/settings',
      ]
      for (const path of adminPaths) {
        await memberPage.goto(`${BASE}${path}`, { waitUntil: 'networkidle' })
        await memberPage.waitForTimeout(350)
        const url = memberPage.url()
        let denied = null
        try {
          denied = new URL(url).searchParams.get('denied')
        } catch {}
        record(
          `权限 成员访问 ${path}`,
          '重定向 /me/subscription?denied=...',
          `URL=${url}，denied=${denied}`,
          url.includes('/me/subscription') && denied === path,
        )
        if (!(url.includes('/me/subscription') && denied === path)) await snap(memberPage, `权限_成员_${path.replace(/\W+/g, '_')}`)
      }

      const mePaths = ['/me/subscription', '/me/usage', '/me/credentials', '/me/status', '/me/security']
      for (const path of mePaths) {
        await memberPage.goto(`${BASE}${path}`, { waitUntil: 'networkidle' })
        await memberPage.waitForTimeout(350)
        const text = await mainTextOf(memberPage)
        const ok = new URL(memberPage.url()).pathname === path && text.length > 30
        record(
          `权限 成员访问 ${path}`,
          '页面正常打开且主内容非空（提示性 Alert 不算错误）',
          `URL=${memberPage.url()}，内容=${text.length} 字符`,
          ok,
        )
        if (!ok) await snap(memberPage, `权限_成员_${path.replace(/\W+/g, '_')}`)
      }
    } finally {
      await memberCtx.close()
    }

    const mePaths = ['/me/subscription', '/me/usage', '/me/credentials', '/me/status', '/me/security']
    for (const path of mePaths) {
      await adminPage.goto(`${BASE}${path}`, { waitUntil: 'networkidle' })
      await adminPage.waitForTimeout(350)
      const text = await mainTextOf(adminPage)
      const ok = new URL(adminPage.url()).pathname === path && text.length > 30
      record(
        `权限 管理员访问 ${path}`,
        '按当前设计允许，页面正常打开且主内容非空（提示性 Alert 不算错误）',
        `URL=${adminPage.url()}，内容=${text.length} 字符`,
        ok,
      )
      if (!ok) await snap(adminPage, `权限_管理员_${path.replace(/\W+/g, '_')}`)
    }
  }
} catch (caught) {
  console.error('FATAL', caught)
  recordFailure(adminPage, 'FATAL 脚本异常', '脚本完整跑完', caught instanceof Error ? caught.stack || caught.message : String(caught))
} finally {
  /* ---------------- Cleanup ---------------- */
  console.log('\n=== 清理测试数据 ===')
  const cleanup = { attempted: [], failed: [], leftovers: {} }
  try {
    if (adminContext) {
      // Restore settings if a test aborted before restoring.
      if (settingsOriginal !== null) {
        try {
          const current = await apiData(adminContext, '/api/v1/admin/settings')
          if (Number(current.retention_days) !== settingsOriginal) {
            await apiData(adminContext, '/api/v1/admin/settings', {
              method: 'PUT',
              csrf: adminCsrf,
              body: { ...current, retention_days: settingsOriginal },
            })
          }
        } catch (e) {
          cleanup.failed.push(`settings restore: ${e}`)
        }
      }

      // Delete in dependency order. DELETE endpoints idempotently archive/delete;
      // 404/409 are treated as cleanup failures with detail.
      if (created.memberId) {
        cleanup.attempted.push(`member ${created.memberId}`)
        const r = await apiCall(adminContext, `/api/v1/admin/members/${created.memberId}`, { method: 'DELETE', csrf: adminCsrf })
        if (r.status >= 300) cleanup.failed.push(`member ${created.memberId}: HTTP ${r.status} ${JSON.stringify(r.body).slice(0, 200)}`)
      }
      if (created.subscriptionId) {
        cleanup.attempted.push(`subscription ${created.subscriptionId}`)
        const r = await apiCall(adminContext, `/api/v1/admin/subscriptions/${created.subscriptionId}`, { method: 'DELETE', csrf: adminCsrf })
        if (r.status >= 300) cleanup.failed.push(`subscription ${created.subscriptionId}: HTTP ${r.status} ${JSON.stringify(r.body).slice(0, 200)}`)
      }
      if (created.groupId) {
        cleanup.attempted.push(`node-group ${created.groupId}`)
        const r = await apiCall(adminContext, `/api/v1/admin/node-groups/${created.groupId}`, { method: 'DELETE', csrf: adminCsrf })
        if (r.status >= 300) cleanup.failed.push(`node-group ${created.groupId}: HTTP ${r.status} ${JSON.stringify(r.body).slice(0, 200)}`)
      }
      for (const nodeId of created.nodeIds) {
        cleanup.attempted.push(`node ${nodeId}`)
        const r = await apiCall(adminContext, `/api/v1/admin/nodes/${nodeId}`, { method: 'DELETE', csrf: adminCsrf })
        if (r.status >= 300) cleanup.failed.push(`node ${nodeId}: HTTP ${r.status} ${JSON.stringify(r.body).slice(0, 200)}`)
      }
      if (created.serverId) {
        cleanup.attempted.push(`server ${created.serverId}`)
        const r = await apiCall(adminContext, `/api/v1/admin/servers/${created.serverId}`, { method: 'DELETE', csrf: adminCsrf })
        if (r.status >= 300) cleanup.failed.push(`server ${created.serverId}: HTTP ${r.status} ${JSON.stringify(r.body).slice(0, 200)}`)
      }

      // Verify nothing with this run's prefix remains visible.
      try {
        const [servers, nodes, groups, subs, members] = await Promise.all([
          apiPage(adminContext, '/api/v1/admin/servers?limit=200'),
          apiPage(adminContext, '/api/v1/admin/nodes?limit=200'),
          apiPage(adminContext, '/api/v1/admin/node-groups?limit=200'),
          apiPage(adminContext, '/api/v1/admin/subscriptions?limit=200'),
          apiPage(adminContext, '/api/v1/admin/members?limit=200'),
        ])
        const leftover = (items, nameKey = 'name') => items.filter((x) => String(x[nameKey] ?? '').startsWith(PREFIX) && x.archived !== true)
        cleanup.leftovers = {
          servers: leftover(servers).map((x) => x.name),
          nodes: leftover(nodes).map((x) => x.name),
          node_groups: leftover(groups).map((x) => x.name),
          subscriptions: leftover(subs).map((x) => x.name),
          members: leftover(members, 'username').map((x) => x.username),
        }
      } catch (e) {
        cleanup.failed.push(`leftover check: ${e}`)
      }
    }
  } catch (e) {
    cleanup.failed.push(`cleanup fatal: ${e}`)
  }

  const leftoverCount = Object.values(cleanup.leftovers).reduce((n, arr) => n + (Array.isArray(arr) ? arr.length : 0), 0)
  record(
    '清理 测试数据',
    '本次前缀的服务器/节点/节点组/订阅/成员均不再可见，且无清理错误',
    `attempted=${cleanup.attempted.length}, failed=${JSON.stringify(cleanup.failed)}, leftovers=${JSON.stringify(cleanup.leftovers)}`,
    cleanup.failed.length === 0 && leftoverCount === 0,
  )

  await adminContext?.close().catch(() => {})
  await browser.close().catch(() => {})

  const passed = results.filter((r) => r.status === 'PASS').length
  const failed = results.filter((r) => r.status === 'FAIL')
  const output = {
    generated_at: new Date().toISOString(),
    base: BASE,
    prefix: PREFIX,
    duration_ms: Date.now() - RUN_STARTED,
    summary: {
      total: results.length,
      pass: passed,
      fail: failed.length,
      console_error_occurrences: rawConsoleErrors.length,
      pageerror_occurrences: rawPageErrors.length,
      injected_cases: results.filter((r) => r.injected).length,
    },
    cleanup,
    failures: failed.map((f) => ({
      id: f.id,
      case: f.case,
      expectation: f.expectation,
      actual: f.actual,
      screenshot: f.screenshot ?? null,
      note: f.note ?? null,
    })),
    results,
    raw_console_errors: rawConsoleErrors,
    raw_page_errors: rawPageErrors,
    screenshots,
  }
  await writeFile(join(OUT, 'ui-full.json'), JSON.stringify(output, null, 2))
  await writeFile(join(OUT, 'ui-full.txt'), results.map((r) => `${r.status}\t${r.id}\t${r.case}\t${r.expectation}\t${r.actual}`).join('\n'))

  console.log('\n================ 汇总 ================')
  console.log(`总计 ${results.length} 项：PASS ${passed} / FAIL ${failed.length}`)
  console.log(`console error 条数=${rawConsoleErrors.length}，pageerror 条数=${rawPageErrors.length}，INJECTED=${output.summary.injected_cases}`)
  if (failed.length) {
    console.log('\nFAIL 清单：')
    for (const f of failed) {
      console.log(`- [${f.id}] ${f.case}`)
      console.log(`  期望: ${f.expectation}`)
      console.log(`  实际: ${f.actual}`)
      if (f.screenshot) console.log(`  截图: ${f.screenshot}`)
    }
  }
  console.log(`\n结果 JSON: ${join(OUT, 'ui-full.json')}`)
  console.log(`截图目录: ${OUT}`)

  process.exitCode = failed.length ? 1 : 0
}
