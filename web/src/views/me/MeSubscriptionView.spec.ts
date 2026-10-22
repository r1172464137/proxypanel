import { afterEach, describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { nextTick } from 'vue'
import MeSubscriptionView from './MeSubscriptionView.vue'

// P1-2：后端在无代理权限时对 /sub/{token} 正确返回
// 403 {"code":"FORBIDDEN","message":"no proxy access: no_binding"}，
// 但页面照样展示订阅链接、复制按钮和二维码，用户复制到一个必然失败的地址。
//
// 这里锁定：access.access === false 时隐藏可复制的链接与二维码、给出可读的
// 原因说明；有权限时行为完全不变；/me/status 拿不到时不臆测为"无权限"。
//
// 断言全部走语义查询（可访问名 / 可见文案 / href），不断言自造 CSS 类名。

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
    rotateSubscriptionToken: vi.fn(async () => ({ token: 'tok_new', url: 'https://panel.test/sub/tok_new' })),
  },
}))

vi.mock('@/stores/session', () => ({
  sessionState: { user: { username: 'alice', role: 'member' } },
  pushToast: vi.fn(),
}))

vi.mock('@/lib/toast', () => ({ pushToast: vi.fn() }))

vi.mock('vue-router', () => ({
  useRoute: () => ({ fullPath: '/me/subscription', meta: {} }),
  useRouter: () => ({ push: vi.fn(), back: vi.fn() }),
}))

const stubs = {
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
  // 二维码组件需要 canvas：用带可访问名的占位符来断言"扫码入口是否存在"
  QrPanel: { props: ['value'], template: '<figure aria-label="订阅二维码" />' },
  LoadingBlock: { template: '<div role="status" aria-label="加载中" />' },
  ErrorState: { template: '<div role="alert" aria-label="加载失败" />' },
  EmptyState: { template: '<p aria-label="空状态" />' },
}

async function mountView() {
  const wrapper = mount(MeSubscriptionView, { global: { stubs } })
  await flushPromises()
  return wrapper
}

function hasQrCode(wrapper: ReturnType<typeof mount>): boolean {
  return wrapper.find('[aria-label="订阅二维码"]').exists()
}

/** 可复制的订阅链接入口（CopyField 的复制按钮带 aria-label="复制"）。 */
function hasCopyButton(wrapper: ReturnType<typeof mount>): boolean {
  return wrapper.findAll('button').some((button) => button.attributes('aria-label') === '复制')
}

function linkTexts(wrapper: ReturnType<typeof mount>): string {
  return wrapper.findAll('a').map((link) => link.text()).join(' ')
}

function links(wrapper: ReturnType<typeof mount>): string[] {
  return wrapper.findAll('a').map((link) => link.attributes('href') ?? '')
}

function buttonByText(wrapper: ReturnType<typeof mount>, text: string) {
  return wrapper.findAll('button').find((button) => button.text().includes(text))
}

afterEach(() => {
  document.body.innerHTML = ''
})

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
    // 二维码与复制入口都必须消失，避免用户拿到必然 403 的地址。
    expect(hasQrCode(wrapper)).toBe(false)
    expect(hasCopyButton(wrapper)).toBe(false)
    expect(wrapper.findAll('input')).toHaveLength(0)
    // 也不应出现"打开 / 下载"这个可点击入口
    expect(linkTexts(wrapper)).not.toContain('打开 / 下载')
    wrapper.unmount()
  })

  it('仍指向账户状态与用量页，便于用户自查', async () => {
    const wrapper = await mountView()
    expect(links(wrapper)).toContain('/me/status')
    expect(links(wrapper)).toContain('/me/usage')
    wrapper.unmount()
  })
})

describe('MeSubscriptionView 有代理权限', () => {
  beforeEach(() => {
    state.status = { access: { access: true, proxy_access: 'active', access_reasons: [] } }
  })

  it('正常渲染订阅链接、二维码与格式选择', async () => {
    const wrapper = await mountView()
    expect(hasQrCode(wrapper)).toBe(true)
    expect(hasCopyButton(wrapper)).toBe(true)
    expect(wrapper.text()).not.toContain('当前无代理权限')
    // 格式选择器由 Label 关联，标签文案对用户可见
    expect(wrapper.text()).toContain('客户端格式')
    expect(buttonByText(wrapper, '校验该格式')).toBeDefined()
    expect(linkTexts(wrapper)).toContain('打开 / 下载')
    wrapper.unmount()
  })

  it('点「轮换订阅链接」打开确认弹窗，弹窗里有确认操作', async () => {
    const wrapper = await mountView()
    const rotate = wrapper.findAll('button').find((button) => button.text() === '轮换订阅链接')
    expect(rotate).toBeDefined()
    await rotate!.trigger('click')
    await nextTick()

    const dialog = document.querySelector('[role="dialog"]')
    expect(dialog).not.toBeNull()
    expect(dialog!.textContent).toContain('轮换订阅链接')
    expect(dialog!.textContent).toContain('轮换后旧订阅链接立即失效')
    expect(dialog!.textContent).toContain('确认轮换')
    wrapper.unmount()
  })
})

describe('MeSubscriptionView 状态未知', () => {
  it('/me/status 拿不到访问判定时不臆测为无权限', async () => {
    state.status = {}
    const wrapper = await mountView()
    // 保留原有链接展示，不能把"请求失败"误报成"没有权限"。
    expect(wrapper.text()).not.toContain('当前无代理权限')
    expect(hasQrCode(wrapper)).toBe(true)
    expect(hasCopyButton(wrapper)).toBe(true)
    wrapper.unmount()
  })
})
