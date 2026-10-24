/**
 * Owned web-demo page objects / selectors (group A). Kept small on purpose:
 * product-readiness-e2e.mjs is the main entry and the split per-US scripts
 * import from here so selectors are not duplicated.
 *
 * Every selector prefers a stable data-testid or a form label; toast text is
 * never the only assertion.
 */
import { expect } from '@playwright/test'

export const REDACTED_SUB = /\/sub\/[^\s"'?]+/g
export const OWNED_SCHEMA = /wdemo_ui_[a-f0-9]{16}/g

export function redactor(extra = []) {
  return (value) => {
    let out = String(value).replace(REDACTED_SUB, '/sub/[REDACTED]').replace(OWNED_SCHEMA, '[owned-schema]')
    for (const secret of extra) if (secret) out = out.split(secret).join('[REDACTED]')
    return out
  }
}

export class SessionPage {
  constructor(page, base) {
    this.page = page
    this.base = base
  }

  async login(username, password, expectPath) {
    await this.page.goto(this.base + '/login')
    await this.page.getByLabel('用户名', { exact: true }).fill(username)
    await this.page.getByLabel('密码', { exact: true }).fill(password)
    await this.page.getByRole('button', { name: '登录', exact: true }).click()
    await this.page.waitForURL('**' + expectPath)
    return this
  }

  heading() { return this.page.getByRole('heading', { level: 1 }) }
}

export class ServersPage {
  constructor(page, base) {
    this.page = page
    this.base = base
  }

  async open() { await this.page.goto(this.base + '/admin/servers') }

  async create(name, address) {
    await this.open()
    await this.page.getByTestId('server-create-button').click()
    await this.page.locator('#server-name').fill(name)
    await this.page.locator('#server-address').fill(address)
    const created = this.page.waitForResponse(r => r.url().endsWith('/api/v1/admin/servers') && r.request().method() === 'POST')
    await this.page.getByRole('dialog').getByRole('button', { name: '保存', exact: true }).click()
    const response = await created
    expect(response.status()).toBe(201)
    await expect(this.page.getByTestId('server-save-result')).toContainText('下一步：生成一键安装命令')
    return (await response.json()).data
  }

  row(name) { return this.page.locator('tr').filter({ hasText: name }) }

  /**
   * Registration code stays one-time: the secret is copied through the real
   * clipboard and handed straight to the fixture, never written to evidence.
   */
  async registrationCode(name) {
    await this.open()
    await this.row(name).getByRole('button', { name: '一键安装', exact: true }).click()
    const dialog = this.page.getByRole('dialog')
    await expect(dialog).toContainText('15 分钟内有效')
    await dialog.getByText('手工安装 / 查看注册码', { exact: true }).click()
    await expect(this.page.getByTestId('registration-code-value')).toBeVisible()
    await this.page.getByTestId('registration-code-value').getByRole('button', { name: '复制', exact: true }).click()
    const code = (await this.page.evaluate(() => navigator.clipboard.readText())).trim()
    await expect(this.page.getByTestId('registration-code-value')).not.toBeEmpty()
    return code
  }
}

export class NodesPage {
  constructor(page, base) {
    this.page = page
    this.base = base
  }

  async open() { await this.page.goto(this.base + '/admin/nodes/new') }

  async create({ name, serverLabel, host, port, listen }) {
    await this.open()
    await this.page.locator('#node-name').fill(name)
    await this.page.locator('#node-server').selectOption({ label: serverLabel })
    await this.page.locator('#node-host').fill(host)
    await this.page.locator('#node-port').fill(String(port))
    await this.page.locator('#node-listen').fill(listen)
    const created = this.page.waitForResponse(r => r.url().endsWith('/api/v1/admin/nodes') && r.request().method() === 'POST')
    await this.page.getByTestId('node-save-button').click()
    const response = await created
    expect(response.status()).toBe(201)
    await expect(this.page.getByTestId('node-sync-panel')).toContainText('已保存，正在同步')
    await expect(this.page.getByTestId('node-sync-panel')).toContainText('真实连接尚未验证')
    return (await response.json()).data
  }
}

export class NodeGroupsPage {
  constructor(page, base) {
    this.page = page
    this.base = base
  }

  async open() { await this.page.goto(this.base + '/admin/node-groups') }

  async create(name, nodeName) {
    await this.open()
    await this.page.getByRole('button', { name: '新建节点组', exact: true }).first().click()
    await this.page.locator('#group-name').fill(name)
    // Click the node row's own label (role=button-free div) inside the group dialog.
    await this.page.getByRole('dialog').locator('div.space-y-1 > div').filter({ hasText: nodeName }).first().click()
    await expect(this.page.getByRole('dialog')).toContainText('包含节点（1）')
    const created = this.page.waitForResponse(r => r.url().endsWith('/api/v1/admin/node-groups') && r.request().method() === 'POST')
    await this.page.getByRole('dialog').getByRole('button', { name: /保存|创建/ }).last().click()
    const response = await created
    expect(response.status()).toBe(201)
    return (await response.json()).data
  }
}

export class SubscriptionsPage {
  constructor(page, base) {
    this.page = page
    this.base = base
  }

  async create({ name, groupName, quotaGiB }) {
    await this.page.goto(this.base + '/admin/subscriptions/new')
    await this.page.locator('#sub-name').fill(name)
    await this.page.getByText(groupName, { exact: false }).first().click()
    await this.page.locator('#sub-quota').fill(String(quotaGiB))
    await this.page.locator('#sub-tz').fill('UTC')
    await this.page.locator('#sub-anchor').fill('1')
    const created = this.page.waitForResponse(r => r.url().endsWith('/api/v1/admin/subscriptions') && r.request().method() === 'POST')
    await this.page.getByTestId('subscription-impact-preview-button').click()
    const response = await created
    // Creating a plan has exactly one contract: 201. A 200 here would mean the
    // fixture silently accepted a different endpoint/behaviour.
    expect(response.status(), 'plan create must be 201 Created').toBe(201)
    return (await response.json()).data
  }
}

export class InvitationsPage {
  constructor(page, base) {
    this.page = page
    this.base = base
  }

  async create(planLabel) {
    await this.page.goto(this.base + '/admin/invitations')
    await this.page.getByRole('button', { name: '生成邀请码', exact: true }).first().click()
    // The plan list is loaded asynchronously; <option> elements are never
    // "visible", so wait for attachment instead of visibility.
    await this.page.locator('#invite-sub option').filter({ hasText: planLabel }).waitFor({ state: 'attached' })
    await this.page.locator('#invite-sub').selectOption({ label: planLabel })
    const created = this.page.waitForResponse(r => r.url().endsWith('/api/v1/admin/invitations') && r.request().method() === 'POST')
    await this.page.getByRole('button', { name: '生成一次性邀请', exact: true }).click()
    const response = await created
    expect(response.status()).toBe(201)
    const code = (await response.json()).data.code
    await expect(this.page.getByTestId('invitation-code-value')).toBeVisible()
    await this.page.getByRole('dialog').getByRole('button', { name: '关闭', exact: true }).last().click()
    return code
  }
}

export class RedeemPage {
  constructor(page, base) {
    this.page = page
    this.base = base
  }

  async redeem({ code, username, password, expectSuccess = true }) {
    await this.page.goto(this.base + '/redeem')
    await this.page.locator('#redeem-code').fill(code)
    await this.page.locator('#redeem-username').fill(username)
    await this.page.locator('#redeem-password').fill(password)
    await this.page.locator('#redeem-confirm').fill(password)
    const pending = this.page.waitForResponse(r => r.url().endsWith('/api/v1/auth/redeem') && r.request().method() === 'POST')
    await this.page.getByRole('button', { name: '兑换并登录', exact: true }).click()
    const response = await pending
    if (expectSuccess) expect(response.status(), 'usable invitation redeem').toBe(201)
    else {
      // An unusable invitation is a domain validation error, not a rate limit,
      // an auth failure or a broken fixture: exactly 400 VALIDATION_FAILED.
      expect(response.status(), 'unusable invitation status').toBe(400)
      const payload = await response.json().catch(() => ({}))
      expect(payload.error?.code, 'unusable invitation code').toBe('VALIDATION_FAILED')
      await expect(this.page.locator('[role="alert"]').first()).toBeVisible()
      const message = await this.page.getByTestId('redeem-result-message').innerText()
      // The uniform public copy must not distinguish invitation states.
      expect(message).toMatch(/邀请不可用/)
      const body = await this.page.locator('body').innerText()
      expect(body).not.toMatch(/expired|revoked|already used|已过期|已撤销|已达到上限/i)
      expect(body).not.toMatch(/429|过于频繁|认证|登录已过期/)
    }
    return response.status()
  }
}

export class MemberServicePage {
  constructor(page, base) {
    this.page = page
    this.base = base
  }

  async open(path = '/me/subscription') {
    await this.page.goto(this.base + path)
    await this.page.reload()
    await expect(this.heading()).toBeVisible()
  }

  heading() { return this.page.getByRole('heading', { level: 1 }) }
  summary() { return this.page.getByTestId('service-summary-card') }
  usedValue() { return this.summary().locator('dd').first() }
  importCard() { return this.page.getByTestId('client-import-card') }
}

export class MembersPage {
  constructor(page, base) {
    this.page = page
    this.base = base
  }

  async open() { await this.page.goto(this.base + '/admin/members') }
  row(username) { return this.page.locator('tr').filter({ has: this.page.getByRole('link', { name: username, exact: true }) }) }

  async pauseProxy(username) {
    await this.open()
    await this.row(username).getByRole('link', { name: username, exact: true }).click()
    await this.page.getByTestId('member-proxy-button').click()
    await expect(this.page.getByTestId('member-action-result')).toContainText('代理访问已暂停')
    await expect(this.page.getByTestId('member-proxy-button')).toHaveText('恢复代理访问')
  }

  async resumeProxy() {
    await this.page.getByTestId('member-proxy-button').click()
    await expect(this.page.getByTestId('member-action-result')).toContainText('代理访问已恢复')
    await expect(this.page.getByTestId('member-proxy-button')).toHaveText('暂停代理访问')
  }
}
