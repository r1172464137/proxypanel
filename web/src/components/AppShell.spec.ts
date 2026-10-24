import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import { createRouter, createMemoryHistory } from 'vue-router'
import AppShell from './AppShell.vue'
import { sessionState } from '@/stores/session'

vi.mock('@/stores/session', () => ({
  sessionState: { user: { username: 'alice', role: 'admin' } },
  logout: vi.fn(async () => undefined),
}))

vi.mock('@/api/endpoints', () => ({ adminApi: { recoveryMode: { get: vi.fn(async () => ({ recovery_mode: false })) } } }))

const MY_ACCOUNT_PATHS = [
  '/me/subscription',
  '/me/usage',
  '/me/credentials',
  '/me/status',
  '/me/security',
]

/*
 * 语义查询：等价于 testing-library 的 getByRole('link', { name })，但不引入额外依赖。
 * role 映射到隐式语义标签（a[href]→link、button→button、nav→navigation…），
 * 可访问名称优先 aria-label，其次可见文字。
 */
type Role = 'link' | 'button' | 'navigation' | 'dialog' | 'complementary' | 'heading'

const ROLE_SELECTORS: Record<Role, string> = {
  link: 'a[href]',
  button: 'button',
  navigation: 'nav',
  dialog: '[role="dialog"]',
  complementary: 'aside',
  heading: 'h1, h2, h3, h4, h5, h6',
}

function accessibleName(el: Element): string {
  const labelled = el.getAttribute('aria-label')?.trim()
  const text = (el.textContent ?? '').replace(/\s+/g, ' ').trim()
  return labelled || text
}

function queryAllByRole(root: ParentNode, role: Role, name?: string | RegExp): HTMLElement[] {
  const all = Array.from(root.querySelectorAll<HTMLElement>(ROLE_SELECTORS[role]))
  if (name === undefined) return all
  return all.filter((el) => {
    const actual = accessibleName(el)
    return typeof name === 'string' ? actual === name : name.test(actual)
  })
}

function getByRole(root: ParentNode, role: Role, name?: string | RegExp): HTMLElement {
  const found = queryAllByRole(root, role, name)
  if (found.length !== 1) {
    throw new Error(
      `期望恰好 1 个 role=${role} name=${String(name)}，实际 ${found.length} 个：` +
        JSON.stringify(found.map((el) => accessibleName(el))),
    )
  }
  return found[0]
}

function meLinks(root: ParentNode): HTMLElement[] {
  return queryAllByRole(root, 'link').filter((link) =>
    (link.getAttribute('href') ?? '').startsWith('/me/'),
  )
}

function mountShell(): VueWrapper {
  return mount(AppShell, {
    attachTo: document.body,
    global: { plugins: [createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] })] },
    slots: { default: '<div>页面内容</div>' },
  })
}

async function openDrawer(): Promise<{ wrapper: VueWrapper; menu: HTMLElement; dialog: HTMLElement }> {
  const wrapper = mountShell()
  const menu = getByRole(document, 'button', '打开导航')
  expect(menu.getAttribute('aria-expanded')).toBe('false')

  menu.click()
  await nextTick()
  await flushPromises()

  const dialog = getByRole(document, 'dialog')
  return { wrapper, menu, dialog }
}

async function closeDrawer(): Promise<void> {
  document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
  await nextTick()
  await flushPromises()
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('AppShell 侧栏导航', () => {
  it('管理员侧栏给出全部 5 个「我的账户」入口，每个都有图标和文字', () => {
    sessionState.user = { username: 'alice', role: 'admin' } as never
    const wrapper = mountShell()

    const sidebar = getByRole(document, 'complementary', '主导航')
    const nav = getByRole(sidebar, 'navigation', '页面导航')
    const links = meLinks(nav)

    expect(links.map((link) => link.getAttribute('href'))).toEqual(MY_ACCOUNT_PATHS)
    for (const link of links) {
      expect(link.querySelector('svg'), `${link.getAttribute('href')} 缺少图标`).not.toBeNull()
      expect(accessibleName(link).length, `${link.getAttribute('href')} 缺少文字`).toBeGreaterThan(0)
    }

    // 区块标题必须是「我的账户」，不能与上方的「成员」（管理他人）混淆。
    const headings = queryAllByRole(nav, 'heading').map((heading) => accessibleName(heading))
    expect(headings).toContain('我的账户')

    wrapper.unmount()
  })

  it('侧栏每个导航项都有图标和文字', () => {
    sessionState.user = { username: 'alice', role: 'admin' } as never
    const wrapper = mountShell()

    const sidebar = getByRole(document, 'complementary', '主导航')
    const links = queryAllByRole(sidebar, 'link')
    expect(links.length).toBeGreaterThan(0)
    for (const link of links) {
      expect(link.querySelector('svg'), `${link.getAttribute('href')} 缺少图标`).not.toBeNull()
      expect(accessibleName(link).length, `${link.getAttribute('href')} 缺少文字`).toBeGreaterThan(0)
    }

    wrapper.unmount()
  })

  it('成员侧栏同样给出 5 个入口，且不出现管理员入口', () => {
    sessionState.user = { username: 'bob', role: 'member' } as never
    const wrapper = mountShell()

    const sidebar = getByRole(document, 'complementary', '主导航')
    expect(meLinks(sidebar).map((link) => link.getAttribute('href'))).toEqual(MY_ACCOUNT_PATHS)
    const adminLinks = queryAllByRole(sidebar, 'link').filter((link) =>
      (link.getAttribute('href') ?? '').startsWith('/admin/'),
    )
    expect(adminLinks).toHaveLength(0)

    wrapper.unmount()
  })

  it('顶部栏保留主题切换与退出登录，且页面内容渲染在 main 内', () => {
    sessionState.user = { username: 'alice', role: 'admin' } as never
    const wrapper = mountShell()

    expect(getByRole(document, 'button', '切换主题')).toBeTruthy()
    const signOut = getByRole(document, 'button', '退出登录')
    expect(signOut).toBeTruthy()
    expect(document.querySelector('main')?.textContent).toContain('页面内容')

    wrapper.unmount()
  })
})

describe('AppShell 移动端抽屉', () => {
  it('汉堡按钮打开抽屉，焦点进入抽屉，抽屉里同样有完整导航', async () => {
    sessionState.user = { username: 'alice', role: 'admin' } as never
    const { wrapper, menu, dialog } = await openDrawer()

    expect(menu.getAttribute('aria-expanded')).toBe('true')
    expect(dialog.contains(document.activeElement)).toBe(true)

    expect(getByRole(dialog, 'navigation', '页面导航')).toBeTruthy()
    const links = meLinks(dialog)
    expect(links.map((link) => link.getAttribute('href'))).toEqual(MY_ACCOUNT_PATHS)
    for (const link of links) {
      expect(link.querySelector('svg')).not.toBeNull()
    }

    wrapper.unmount()
  })

  it('Escape 关闭抽屉并把焦点还给汉堡按钮', async () => {
    sessionState.user = { username: 'alice', role: 'admin' } as never
    const { wrapper, menu } = await openDrawer()

    await closeDrawer()

    expect(queryAllByRole(document, 'dialog')).toHaveLength(0)
    expect(menu.getAttribute('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(menu)

    wrapper.unmount()
  })

  it('点击遮罩关闭抽屉', async () => {
    sessionState.user = { username: 'alice', role: 'admin' } as never
    const { wrapper, menu, dialog } = await openDrawer()

    // 遮罩是抽屉内容的兄弟节点（reka 的 DialogOverlay）。
    const backdrop = dialog.previousElementSibling as HTMLElement | null
    expect(backdrop).not.toBeNull()

    // 抽屉打开后 reka 会在下一个宏任务注册 outside-pointerdown 监听。
    await new Promise((resolve) => setTimeout(resolve, 0))
    backdrop!.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await nextTick()
    await flushPromises()

    expect(queryAllByRole(document, 'dialog')).toHaveLength(0)
    expect(menu.getAttribute('aria-expanded')).toBe('false')

    wrapper.unmount()
  })

  it('未打开抽屉时按 Escape 不改变状态', async () => {
    sessionState.user = { username: 'alice', role: 'admin' } as never
    const wrapper = mountShell()
    const menu = getByRole(document, 'button', '打开导航')

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await nextTick()

    expect(menu.getAttribute('aria-expanded')).toBe('false')
    expect(queryAllByRole(document, 'dialog')).toHaveLength(0)

    wrapper.unmount()
  })
})
