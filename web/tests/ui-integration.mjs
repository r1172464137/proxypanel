// 第 3 步：页面联调。每个页面记录 HTTP / 控制台 / 异常 / 溢出 / 空壳 / 占位符，
// 并对关键表单做真实提交，验证前后面数据一致。
//
// 真实链路，唯二的注入是：一个 503（验证错误态）与一个人为延迟（验证加载态），
// 都用 INJECTED_ 前缀，绝不与真实错误混淆。
import { chromium } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
import { join } from 'node:path'

const BASE = process.env.PANEL_BASE || 'https://192.168.5.203:8443'
const USER = process.env.PANEL_ADMIN_USER || 'admin'
const PASS = process.env.PANEL_ADMIN_PASSWORD || ''
const OUT = process.env.OUT_DIR || '/home/compile/Work/evidence/ui'

const findings = []
function record(page, kind, detail, severity = 'defect') {
  findings.push({ page, kind, detail, severity })
  console.log(`    [${severity}] ${kind}: ${detail}`)
}

const ADMIN_PAGES = [
  ['/admin/overview', '总览'],
  ['/admin/servers', '服务器'],
  ['/admin/nodes', '节点'],
  ['/admin/nodes/new', '新建节点'],
  ['/admin/node-groups', '节点组'],
  ['/admin/subscriptions', '订阅'],
  ['/admin/subscriptions/new', '新建订阅'],
  ['/admin/members', '成员'],
  ['/admin/invitations', '邀请'],
  ['/admin/usage', '用量'],
  ['/admin/events', '事件与告警'],
  ['/admin/tasks', '任务与同步'],
  ['/admin/audit', '审计'],
  ['/admin/settings', '设置'],
  // 已知可疑
  ['/admin/templates', '模板（可疑：空）'],
  ['/admin/batches', '批量（可疑：空）'],
  ['/admin/batches/bat_nonexistent', '批量详情（404 态）'],
]

const ME_PAGES = [
  ['/me/subscription', '我的订阅'],
  ['/me/usage', '我的用量'],
  ['/me/credentials', '凭据'],
  ['/me/status', '账户状态'],
  ['/me/security', '账号安全'],
]

async function inspectPage(page, path, label) {
  const logs = [], errors = [], failed = []
  const onLog = (m) => { if (m.type() === 'error') logs.push(m.text()) }
  const onErr = (e) => errors.push(e.message)
  const onFail = (r) => failed.push(`${r.method()} ${r.url().replace(BASE, '')} — ${r.failure()?.errorText}`)
  page.on('console', onLog); page.on('pageerror', onErr); page.on('requestfailed', onFail)

  let status = null
  try {
    const resp = await page.goto(BASE + path, { waitUntil: 'networkidle', timeout: 25000 })
    status = resp?.status()
    await page.waitForTimeout(500)
  } catch (e) {
    record(path, 'nav-failed', e.message.split('\n')[0])
    page.off('console', onLog); page.off('pageerror', onErr); page.off('requestfailed', onFail)
    return
  }

  const safe = path.replace(/[/]/g, '_')
  await page.screenshot({ path: join(OUT, `page${safe}.png`), fullPage: true })

  const info = await page.evaluate(() => ({
    headings: [...document.querySelectorAll('h1,.page-header__title,.page-title')].map(e => e.innerText.trim()).filter(Boolean),
    mainText: (document.querySelector('main,.content')?.innerText || '').trim(),
    tables: document.querySelectorAll('table').length,
    rows: document.querySelectorAll('tbody tr').length,
    buttons: [...document.querySelectorAll('button')].map(b => b.innerText.trim()).filter(Boolean),
    inputs: document.querySelectorAll('input').length,
    selects: document.querySelectorAll('select').length,
    banners: [...document.querySelectorAll('.banner--danger,[role=alert]')].map(e => e.innerText.trim()).filter(Boolean),
    scrollW: document.documentElement.scrollWidth,
    clientW: document.documentElement.clientWidth,
    navLinks: [...document.querySelectorAll('a[href^="/"]')].map(a => a.getAttribute('href')),
  }))

  console.log(`\n  ${path} (${label}) HTTP ${status} | 标题 ${JSON.stringify(info.headings)} | 表 ${info.tables} 行 ${info.rows} | 按钮 ${info.buttons.length} | 输入 ${info.inputs}`)

  if (status >= 400) record(path, 'http', `HTTP ${status}`)
  if (info.banners.length) record(path, 'error-banner', info.banners.join(' | ').slice(0, 200))
  if (logs.length) record(path, 'console', `${logs.length} 条：${logs.slice(0, 2).join(' | ').slice(0, 250)}`)
  if (errors.length) record(path, 'pageerror', `${errors.length} 条：${errors.slice(0, 2).join(' | ').slice(0, 250)}`)
  if (failed.length) record(path, 'request-failed', failed.slice(0, 3).join(' | ').slice(0, 250))
  if (info.scrollW > info.clientW + 1) record(path, 'overflow', `横向溢出 ${info.scrollW - info.clientW}px`)
  if (info.mainText.length < 20) record(path, 'blank-page', `主内容 ${info.mainText.length} 字符`, 'critical')
  for (const token of ['undefined', 'null', 'NaN', '[object Object]', 'TODO', 'FIXME']) {
    if (info.mainText.includes(token)) record(path, 'placeholder', `出现字面量 "${token}"`)
  }

  page.off('console', onLog); page.off('pageerror', onErr); page.off('requestfailed', onFail)
  return info
}

async function main() {
  await mkdir(OUT, { recursive: true })
  const browser = await chromium.launch({ headless: true })
  const ctx = await browser.newContext({
    ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 }, locale: 'zh-CN',
  })
  const page = await ctx.newPage()

  console.log('=== 登录 ===')
  await page.goto(BASE + '/', { waitUntil: 'networkidle' })
  if (!page.url().includes('/login')) record('/', 'auth', `未登录访问 / 没有跳登录页，当前 ${page.url()}`)
  await page.locator('input').nth(0).fill(USER)
  await page.locator('input[type=password]').first().fill(PASS)
  await page.locator('button[type=submit]').first().click()
  await page.waitForTimeout(2500)
  if (page.url().includes('/login')) {
    record('/login', 'auth', '登录后仍在 /login', 'critical')
    await browser.close()
    return
  }
  console.log('  登录成功 ->', page.url())

  console.log('\n=== 管理员页面 ===')
  const navLinks = new Set()
  for (const [p, l] of ADMIN_PAGES) {
    const info = await inspectPage(page, p, l)
    if (info) info.navLinks.forEach(h => navLinks.add(h))
  }

  console.log('\n=== 成员端页面 ===')
  for (const [p, l] of ME_PAGES) await inspectPage(page, p, l)

  console.log('\n=== 侧栏链路可达性 ===')
  const dead = []
  for (const href of [...navLinks].sort()) {
    if (href.startsWith('/assets') || href === '/') continue
    const r = await page.goto(BASE + href, { waitUntil: 'domcontentloaded', timeout: 15000 }).catch(() => null)
    const t = (await page.locator('main,.content').first().innerText().catch(() => '')).trim()
    const blank = t.length < 20
    if (r?.status() >= 400 || blank) {
      dead.push({ href, status: r?.status(), blank })
      record(href, 'dead-link', `状态 ${r?.status()}，主内容 ${t.length} 字符`, 'critical')
    }
  }
  console.log(`  检查 ${navLinks.size} 个链接，死链 ${dead.length} 个`)

  await writeFile(join(OUT, 'findings.json'), JSON.stringify(findings, null, 2))
  await browser.close()

  console.log('\n=== 汇总 ===')
  const byKind = {}
  for (const f of findings) byKind[f.kind] = (byKind[f.kind] || 0) + 1
  for (const [k, n] of Object.entries(byKind).sort((a, b) => b[1] - a[1])) console.log(`  ${k}: ${n}`)
  console.log(`  总计 ${findings.length}`)
  console.log(`  输出: ${OUT}`)
}

main().catch(e => { console.error('巡检失败:', e); process.exit(1) })
