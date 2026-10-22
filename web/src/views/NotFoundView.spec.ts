import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import NotFoundView from './NotFoundView.vue'

// P0-4：「页面不存在」页原先只有一句说明和"返回首页"。用户从前端某个入口点进来
// 时，无法区分"功能还没实现"和"页面坏了"；而"返回首页"指向 "/" 会再重定向一次，
// 观感像点了没反应。这里锁定：显示实际请求路径、按路径前缀区分两种文案、
// 首页链接直接给目标路由、无站内历史时"返回上一页"退化成回首页。

const routerPush = vi.fn(async () => undefined)
const routerBack = vi.fn()
const state = vi.hoisted(() => ({
  fullPath: '/admin/templates',
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ fullPath: state.fullPath }),
  useRouter: () => ({ push: routerPush, back: routerBack }),
}))

vi.mock('@/stores/session', () => ({
  sessionState: { user: { username: 'admin', role: 'admin' } },
}))

const stubs = {
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
  AppIcon: { template: '<svg class="app-icon" />' },
}

function mountView() {
  return mount(NotFoundView, { global: { stubs } })
}

describe('NotFoundView', () => {
  beforeEach(() => {
    routerPush.mockClear()
    routerBack.mockClear()
    window.history.replaceState({ back: null }, '')
  })

  it('展示用户实际请求的路径', () => {
    state.fullPath = '/admin/templates'
    const wrapper = mountView()
    expect(wrapper.text()).toContain('/admin/templates')
    wrapper.unmount()
  })

  it('/admin 与 /me 下的未知路径提示"功能可能尚未开放"', () => {
    state.fullPath = '/admin/templates'
    let wrapper = mountView()
    expect(wrapper.text()).toContain('该功能可能尚未开放')
    wrapper.unmount()

    state.fullPath = '/me/whatever'
    wrapper = mountView()
    expect(wrapper.text()).toContain('该功能可能尚未开放')
    wrapper.unmount()
  })

  it('其它路径提示"该地址不存在"', () => {
    state.fullPath = '/typo-path'
    const wrapper = mountView()
    expect(wrapper.text()).toContain('该地址不存在')
    wrapper.unmount()
  })

  it('返回首页直接给目标路由而不是 /', () => {
    state.fullPath = '/admin/templates'
    const wrapper = mountView()
    const hrefs = wrapper.findAll('a').map((a) => a.attributes('href'))
    expect(hrefs).toContain('/admin/overview')
    // "/" 会再重定向一次，正是要避免的那个死循环观感
    expect(hrefs).not.toContain('/')
    wrapper.unmount()
  })

  it('无站内历史时"返回上一页"退化成回首页，不会把用户带出面板', async () => {
    state.fullPath = '/admin/templates'
    window.history.replaceState({ back: null }, '')
    const wrapper = mountView()
    const backBtn = wrapper.findAll('button').find((b) => b.text().includes('返回上一页'))
    expect(backBtn).toBeTruthy()
    await backBtn!.trigger('click')
    await nextTick()
    expect(routerBack).not.toHaveBeenCalled()
    expect(routerPush).toHaveBeenCalledWith('/admin/overview')
    wrapper.unmount()
  })

  it('有站内历史时"返回上一页"走 router.back()', async () => {
    state.fullPath = '/admin/templates'
    window.history.replaceState({ back: '/admin/settings' }, '')
    const wrapper = mountView()
    const backBtn = wrapper.findAll('button').find((b) => b.text().includes('返回上一页'))
    await backBtn!.trigger('click')
    await nextTick()
    expect(routerBack).toHaveBeenCalled()
    expect(routerPush).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
