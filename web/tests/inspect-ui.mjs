// 真实浏览器巡检：登录、逐页遍历、记录每一处可见问题。
//
// 只做观察和记录，不做断言性通过/失败——目的是把界面上真实存在的问题
// 列出来。真实链路（真面板、真 PostgreSQL、真 nginx、真 TLS），不做任何
// 网络拦截或伪造响应。
//
// 用法：node tests/inspect-ui.mjs [baseURL]
import { chromium } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
import { join } from 'node:path'

const BASE = process.argv[2] || 'https://192.168.5.203:8443'
const USER = process.env.PANEL_ADMIN_USER || 'admin'
const PASS = process.env.PANEL_ADMIN_PASSWORD || ''
const OUT = process.env.OUT_DIR || '/home/compile/Work/ui-inspect'

const ADMIN_PAGES = [
  ['/admin/overview', '概览'],
  ['/admin/servers', '服务器'],
  ['/admin/nodes', '节点'],
  ['/admin/node-groups', '节点组'],
  ['/admin/subscriptions', '订阅'],
  ['/admin/members', '成员'],
  ['/admin/templates', '模板'],
  ['/admin/batches', '批量'],
  ['/admin/events', '事件'],
  ['/admin/audit', '审计'],
  ['/admin/settings', '设置'],
  ['/admin/usage', '用量'],
]

const PROBLEMS = []
function problem(page, kind, detail, extra = {}) {
  PROBLEMS.push({ page, kind, detail, ...extra })
  console.log(`  [${kind}] ${detail}`)
}

async function main() {
  await mkdir(OUT, { recursive: true })

  const browser = await chromium.launch({ headless: true })
  const context = await browser.newContext({
    ignoreHTTPSErrors: true,
    viewport: { width: 1440, height: 900 },
    locale: 'zh-CN',
  })
  const page = await context.newPage()

  const consoleErrors = []
  const pageErrors = []
  const failedRequests = []
  page.on('console', (m) => {
    if (m.type() === 'error') consoleErrors.push(m.text())
  })
  page.on('pageerror', (e) => pageErrors.push(e.message))
  page.on('requestfailed', (r) => {
    // 证书/连接类失败单独归类，避免和后端 4xx/5xx 混在一起
    failedRequests.push(`${r.method()} ${r.url()} — ${r.failure()?.errorText}`)
  })

  console.log('=== 1. 打开首页 ===')
  await page.goto(`${BASE}/`, { waitUntil: 'networkidle', timeout: 30000 })
  console.log('  url:', page.url())
  console.log('  title:', await page.title())
  await page.screenshot({ path: join(OUT, '01-landing.png'), fullPage: true })

  // 未登录时应被导到登录页
  const onLogin = page.url().includes('/login')
  console.log('  未登录被重定向到登录页:', onLogin)
  if (!onLogin) problem('landing', 'auth', `未登录访问 / 没有跳转到 /login，当前 ${page.url()}`)

  console.log('=== 2. 登录 ===')
  const pwdInput = page.locator('input[type=password]')
  if ((await pwdInput.count()) === 0) {
    problem('login', 'ui', '登录页找不到密码输入框')
  } else {
    const userInput = page.locator('input').first()
    await userInput.fill(USER)
    await pwdInput.first().fill(PASS)
    await page.screenshot({ path: join(OUT, '02-login-filled.png'), fullPage: true })
    const submit = page.locator('button[type=submit], button:has-text("登录")').first()
    await submit.click()
    await page.waitForTimeout(2500)
    console.log('  登录后 url:', page.url())
    await page.screenshot({ path: join(OUT, '03-after-login.png'), fullPage: true })
    if (page.url().includes('/login')) {
      const banner = await page.locator('[role=alert]').allInnerTexts().catch(() => [])
      problem('login', 'auth', `登录后仍停留在 /login；提示：${banner.join(' | ') || '(无)'}`)
    }
  }

  console.log('=== 3. 逐页遍历 ===')
  for (const [path, label] of ADMIN_PAGES) {
    consoleErrors.length = 0
    pageErrors.length = 0
    failedRequests.length = 0
    const before = PROBLEMS.length

    let resp
    try {
      resp = await page.goto(`${BASE}${path}`, { waitUntil: 'networkidle', timeout: 30000 })
      await page.waitForTimeout(600)
    } catch (e) {
      problem(path, 'nav', `打开失败: ${e.message.split('\n')[0]}`)
      continue
    }

    const status = resp?.status()
    const safe = path.replace(/[/]/g, '_')
    await page.screenshot({ path: join(OUT, `page${safe}.png`), fullPage: true })

    // 1) HTTP 状态
    if (status && status >= 400) problem(path, 'http', `HTTP ${status}`)

    // 2) 页面级错误横幅
    const banners = await page.locator('.banner--danger, [role=alert]').allInnerTexts().catch(() => [])
    const realBanners = banners.map((s) => s.trim()).filter(Boolean)
    if (realBanners.length) problem(path, 'banner', `错误横幅: ${realBanners.join(' | ').slice(0, 200)}`)

    // 3) 控制台错误
    if (consoleErrors.length) {
      problem(path, 'console', `${consoleErrors.length} 条控制台错误: ${consoleErrors.slice(0, 3).join(' | ').slice(0, 300)}`)
    }
    if (pageErrors.length) {
      problem(path, 'pageerror', `${pageErrors.length} 条未捕获异常: ${pageErrors.slice(0, 2).join(' | ').slice(0, 300)}`)
    }

    // 4) 请求失败
    if (failedRequests.length) {
      problem(path, 'request', `${failedRequests.length} 个请求失败: ${failedRequests.slice(0, 3).join(' | ').slice(0, 300)}`)
    }

    // 5) 横向溢出（页面级）
    const overflow = await page.evaluate(() => ({
      scrollW: document.documentElement.scrollWidth,
      clientW: document.documentElement.clientWidth,
    }))
    if (overflow.scrollW > overflow.clientW + 1) {
      problem(path, 'overflow', `页面横向溢出 ${overflow.scrollW} > ${overflow.clientW}（${overflow.scrollW - overflow.clientW}px）`)
    }

    // 6) 内容是否为空壳
    const mainText = (await page.locator('main, .content').first().innerText().catch(() => '')).trim()
    if (mainText.length < 20) {
      problem(path, 'empty', `主内容区几乎为空（${mainText.length} 字符）`)
    }

    // 7) 可见的占位/未完成痕迹
    const bodyText = await page.locator('body').innerText().catch(() => '')
    for (const token of ['undefined', 'null', 'NaN', 'TODO', 'FIXME', 'Lorem', '[object Object]']) {
      if (bodyText.includes(token)) {
        problem(path, 'placeholder', `页面出现字面量 "${token}"`)
      }
    }

    console.log(`  ${path} (${label}): HTTP ${status}, 问题 ${PROBLEMS.length - before}`)
  }

  console.log('=== 4. 移动端 390px 抽查 ===')
  await page.setViewportSize({ width: 390, height: 844 })
  for (const [path] of ADMIN_PAGES.slice(0, 6)) {
    await page.goto(`${BASE}${path}`, { waitUntil: 'networkidle', timeout: 30000 }).catch(() => {})
    await page.waitForTimeout(400)
    const o = await page.evaluate(() => ({
      s: document.documentElement.scrollWidth,
      c: document.documentElement.clientWidth,
    }))
    if (o.s > o.c + 1) {
      problem(path, 'overflow-390', `390px 横向溢出 ${o.s} > ${o.c}（${o.s - o.c}px）`)
    }
    await page.screenshot({ path: join(OUT, `m390${path.replace(/[/]/g, '_')}.png`), fullPage: true })
  }

  await writeFile(join(OUT, 'problems.json'), JSON.stringify(PROBLEMS, null, 2))
  await browser.close()

  console.log('\n=== 汇总 ===')
  console.log(`共 ${PROBLEMS.length} 个问题`)
  const byKind = {}
  for (const p of PROBLEMS) byKind[p.kind] = (byKind[p.kind] || 0) + 1
  for (const [k, n] of Object.entries(byKind).sort((a, b) => b[1] - a[1])) {
    console.log(`  ${k}: ${n}`)
  }
  console.log(`截图与问题清单: ${OUT}`)
}

main().catch((e) => {
  console.error('巡检本身失败:', e)
  process.exit(1)
})
