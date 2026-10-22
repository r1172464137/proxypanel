import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import AppShell from './AppShell.vue'

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
