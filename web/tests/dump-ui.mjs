// 逐页抓取界面真实文本与结构，用于人工核对功能是否完整。
// 不截图（当前环境无法读图），只输出可供判断的文本证据。
import { chromium } from '@playwright/test'

const BASE = process.argv[2] || 'https://192.168.5.203:8443'
const USER = process.env.PANEL_ADMIN_USER || 'admin'
const PASS = process.env.PANEL_ADMIN_PASSWORD || ''

const PAGES = [
  '/admin/overview', '/admin/servers', '/admin/nodes', '/admin/node-groups',
  '/admin/subscriptions', '/admin/members', '/admin/templates', '/admin/batches',
  '/admin/events', '/admin/audit', '/admin/settings', '/admin/usage',
]

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 }, locale: 'zh-CN' })
const page = await ctx.newPage()

await page.goto(`${BASE}/`, { waitUntil: 'networkidle' })
if (page.url().includes('/login')) {
  const inputs = page.locator('input')
  await inputs.nth(0).fill(USER)
  await page.locator('input[type=password]').first().fill(PASS)
  await page.locator('button[type=submit]').first().click()
  await page.waitForTimeout(2500)
}

for (const path of PAGES) {
  console.log('\n' + '='.repeat(70))
  console.log('PAGE', path)
  console.log('='.repeat(70))
  try {
    const resp = await page.goto(`${BASE}${path}`, { waitUntil: 'networkidle', timeout: 25000 })
    await page.waitForTimeout(500)
    console.log('HTTP', resp?.status(), '| final url:', page.url())

    // 页面标题 / 页头
    const h = await page.locator('h1, .page-header__title, .page-title').allInnerTexts().catch(() => [])
    console.log('HEADINGS:', JSON.stringify(h))

    // 主要内容文本（截断）
    const main = (await page.locator('main, .content').first().innerText().catch(() => '')).trim()
    console.log('MAIN TEXT (前 700 字符):')
    console.log(main.slice(0, 700) || '(空)')

    // 结构性元素计数
    const counts = await page.evaluate(() => ({
      tables: document.querySelectorAll('table').length,
      rows: document.querySelectorAll('tbody tr').length,
      buttons: document.querySelectorAll('button').length,
      inputs: document.querySelectorAll('input').length,
      selects: document.querySelectorAll('select').length,
      forms: document.querySelectorAll('form').length,
      cards: document.querySelectorAll('.card').length,
      emptyStates: document.querySelectorAll('.empty, .empty-state, [data-empty]').length,
      banners: document.querySelectorAll('.banner--danger, [role=alert]').length,
    }))
    console.log('COUNTS:', JSON.stringify(counts))

    // 按钮文字，能看出功能入口
    const btnText = await page.locator('button').allInnerTexts().catch(() => [])
    console.log('BUTTONS:', JSON.stringify(btnText.map(s => s.trim()).filter(Boolean).slice(0, 20)))

    // 链接（导航）
    const nav = await page.locator('a[href]').evaluateAll(els =>
      els.map(e => `${e.innerText.trim().slice(0, 20)} -> ${e.getAttribute('href')}`).filter(s => s.length > 4))
    console.log('LINKS:', JSON.stringify(nav.slice(0, 25)))
  } catch (e) {
    console.log('ERROR:', e.message.split('\n')[0])
  }
}

await browser.close()
