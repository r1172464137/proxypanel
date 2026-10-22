import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import MeSubscriptionView from './MeSubscriptionView.vue'

// P1-2：后端在无代理权限时对 /sub/{token} 正确返回
// 403 {"code":"FORBIDDEN","message":"no proxy access: no_binding"}，
// 但页面照样展示订阅链接、复制按钮和二维码，用户复制到一个必然失败的地址。
//
// 这里锁定：access.access === false 时隐藏可复制的链接与二维码、给出可读的
// 原因说明；有权限时行为完全不变；/me/status 拿不到时不臆测为"无权限"。

const state = vi.hoisted(() => ({
  subscription: { url: 'https://panel.test/sub/{token}', token: 'tok_secret', formats: ['xray', 'singbox'] },
  status: { access: { access: true, proxy_access: 'active', access_reasons: [] as string[] } } as Record<string, unknown>,
}))

vi.mock('@/api/endpoints', () => ({
  meApi: {
    subscription: vi.fn(async () => state.subscription),
    status: vi.fn(async () => state.status),
    nodes: vi.fn(async () => []),
    rotateToken: vi.fn(),
  },
}))

vi.mock('@/stores/session', () => ({
  sessionState: { user: { username: 'alice', role: 'member' } },
  pushToast: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ fullPath: '/me/subscription', meta: {} }),
  useRouter: () => ({ push: vi.fn(), back: vi.fn() }),
}))

const stubs = {
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
  AppIcon: { template: '<svg class="app-icon" />' },
  // The QR component renders a canvas; stub it so we can assert presence/absence.
  QrPanel: { props: ['value'], template: '<div class="qr-stub" />' },
  LoadingBlock: { template: '<div class="loading-stub" />' },
  ErrorState: { template: '<div class="error-stub" />' },
  EmptyState: { template: '<div class="empty-stub" />' },
}

async function mountView() {
  const wrapper = mount(MeSubscriptionView, { global: { stubs } })
  await flushPromises()
  return wrapper
}

describe('MeSubscriptionView 无代理权限', () => {
  beforeEach(() => {
    state.status = { access: { access: false, proxy_access: 'no_binding', access_reasons: ['no_binding'] } }
  })

  it('提示无代理权限，并把原因码翻成可读中文', async () => {
    const wrapper = await mountView()
    const text = wrapper.text()
    expect(text).toContain('当前无代理权限')
    expect(text).toContain('还没有绑定订阅')
    wrapper.unmount()
  })

  it('不再渲染可复制的订阅链接与二维码', async () => {
    const wrapper = await mountView()
    // 链接输入框与二维码都必须消失，避免用户拿到必然 403 的地址。
    expect(wrapper.find('.qr-stub').exists()).toBe(false)
    expect(wrapper.findAll('input').length).toBe(0)
    // 也不应出现"打开 / 下载"这个可点击入口
    expect(wrapper.findAll('a').map((a) => a.text()).join(' ')).not.toContain('打开 / 下载')
    wrapper.unmount()
  })

  it('仍指向账户状态与用量页，便于用户自查', async () => {
    const wrapper = await mountView()
    const hrefs = wrapper.findAll('a').map((a) => a.attributes('href'))
    expect(hrefs).toContain('/me/status')
    expect(hrefs).toContain('/me/usage')
    wrapper.unmount()
  })
})

describe('MeSubscriptionView 有代理权限', () => {
  beforeEach(() => {
    state.status = { access: { access: true, proxy_access: 'active', access_reasons: [] } }
  })

  it('正常渲染订阅链接与二维码', async () => {
    const wrapper = await mountView()
    expect(wrapper.find('.qr-stub').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('当前无代理权限')
    wrapper.unmount()
  })
})

describe('MeSubscriptionView 状态未知', () => {
  it('/me/status 拿不到访问判定时不臆测为无权限', async () => {
    state.status = {}
    const wrapper = await mountView()
    // 保留原有链接展示，不能把"请求失败"误报成"没有权限"。
    expect(wrapper.text()).not.toContain('当前无代理权限')
    expect(wrapper.find('.qr-stub').exists()).toBe(true)
    wrapper.unmount()
  })
})
