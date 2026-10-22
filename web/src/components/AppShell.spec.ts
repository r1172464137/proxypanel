import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import AppShell from './AppShell.vue'
import { sessionState } from '@/stores/session'

vi.mock('@/stores/session', () => ({
  sessionState: { user: { username: 'alice', role: 'admin' } },
  logout: vi.fn(async () => undefined),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ meta: { title: '总览' }, fullPath: '/admin/overview' }),
  useRouter: () => ({ push: vi.fn(async () => undefined) }),
}))

const stubs = {
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
}

function mountShell() {
  return mount(AppShell, {
    attachTo: document.body,
    global: { stubs },
    slots: { default: '<div>页面内容</div>' },
  })
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('AppShell 导航与抽屉', () => {
  it('侧栏每个入口都有 AppIcon 图标和文字，且不再使用 Unicode 图标', () => {
    const wrapper = mountShell()
    const links = wrapper.findAll('.sidebar__link')
    expect(links.length).toBeGreaterThan(0)
    for (const link of links) {
      expect(link.find('svg.app-icon').exists()).toBe(true)
      expect(link.text().length).toBeGreaterThan(0)
    }
    expect(wrapper.text()).not.toContain('☰')
    expect(wrapper.text()).not.toContain('☾')
    wrapper.unmount()
  })

  it('品牌保留「代理面板」名称并使用直角品牌标记', () => {
    const wrapper = mountShell()
    expect(wrapper.find('.sidebar__brand-text strong').text()).toBe('代理面板')
    expect(wrapper.find('.sidebar__seal').text()).toBe('代')
    wrapper.unmount()
  })

  it('抽屉可由菜单按钮打开，Escape 关闭并把焦点还给触发按钮', async () => {
    const wrapper = mountShell()
    const menu = wrapper.find('button[aria-label="打开导航"]')
    expect(menu.attributes('aria-expanded')).toBe('false')

    await menu.trigger('click')
    expect(wrapper.find('.sidebar').classes()).toContain('sidebar--open')
    expect(menu.attributes('aria-expanded')).toBe('true')
    expect(wrapper.find('.sidebar-backdrop').exists()).toBe(true)

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await nextTick()

    expect(wrapper.find('.sidebar').classes()).not.toContain('sidebar--open')
    expect(menu.attributes('aria-expanded')).toBe('false')
    expect(wrapper.find('.sidebar-backdrop').exists()).toBe(false)
    expect(document.activeElement).toBe(menu.element)
    wrapper.unmount()
  })

  it('未打开抽屉时按 Escape 不改变状态', async () => {
    const wrapper = mountShell()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await nextTick()
    expect(wrapper.find('.sidebar').classes()).not.toContain('sidebar--open')
    wrapper.unmount()
  })
})

// P1-3：成员端 5 个页面都必须在侧栏可达。
//
// 修复前管理员侧栏只给了"我的订阅视图"一项，其余 4 个页面（用量、凭据、
// 账户状态、账号安全）只能手输 URL 到达。这里锁定"5 个入口都在、都有图标、
// 区块标题不再叫容易与'成员管理'混淆的'成员端'"。
describe('AppShell 我的账户入口', () => {
  it('管理员侧栏给出全部 5 个账户视图入口', async () => {
    sessionState.user = {
      id: 'mbr_admin', username: 'admin', role: 'admin', active: true, proxy_enabled: true,
    } as never
    const wrapper = mountShell()
    await nextTick()

    const hrefs = wrapper.findAll('.sidebar__link').map((a) => a.attributes('href'))
    for (const path of [
      '/me/subscription', '/me/usage', '/me/credentials', '/me/status', '/me/security',
    ]) {
      expect(hrefs, `侧栏缺少 ${path}`).toContain(path)
    }
    // 区块标题不再与上方的"成员"（管理他人）混淆。
    expect(wrapper.text()).toContain('我的账户')
    wrapper.unmount()
  })

  it('成员侧栏同样给出 5 个入口且都有图标与文字', async () => {
    sessionState.user = {
      id: 'mbr_member', username: 'alice', role: 'member', active: true, proxy_enabled: true,
    } as never
    const wrapper = mountShell()
    await nextTick()

    const links = wrapper.findAll('.sidebar__link')
    const meLinks = links.filter((a) => (a.attributes('href') ?? '').startsWith('/me/'))
    expect(meLinks.length).toBe(5)
    for (const link of meLinks) {
      expect(link.find('svg.app-icon').exists()).toBe(true)
      expect(link.text().trim().length).toBeGreaterThan(0)
    }
    wrapper.unmount()
  })
})
