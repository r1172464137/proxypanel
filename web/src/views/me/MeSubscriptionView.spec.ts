import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import type { MeStatus, MeSubscription } from '@/api/types'
import { ApiError } from '@/api/http'
import MeSubscriptionView from './MeSubscriptionView.vue'
import MeStatusView from './MeStatusView.vue'
import { memberAccessState } from '@/lib/memberService'

const state = vi.hoisted(() => ({
  status: {} as MeStatus,
  subscription: {} as MeSubscription,
  subscriptionError: null as Error | null,
  rotate: vi.fn(),
}))
vi.mock('@/api/endpoints', () => ({ meApi: {
  status: vi.fn(async () => state.status),
  subscription: vi.fn(async () => { if (state.subscriptionError) throw state.subscriptionError; return state.subscription }),
  nodes: vi.fn(async () => ({ items: [{ id: 'node-1', name: '我的入口', protocol: 'vless' }], next_cursor: null })),
  rotateSubscriptionToken: state.rotate,
} }))
vi.mock('@/lib/toast', () => ({ pushToast: vi.fn() }))
const stubs = {
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
  QrPanel: { props: ['value'], template: '<figure aria-label="个人二维码" />' },
}
const wrappers: ReturnType<typeof mount>[] = []
async function view() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] })
  await router.push('/me/subscription')
  await router.isReady()
  const wrapper = mount(MeSubscriptionView, { global: { plugins: [router], stubs } })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}
beforeEach(() => {
  state.status = { access: { access: true, used_bytes: 1024, effective_quota_bytes: 4096 } }
  state.subscription = { url: 'https://panel.test/sub/{token}', token: 'test-only-token', formats: ['v2rayn'], format_status: [{ format: 'v2rayn', available: true, reason: 'ok' }] }
  state.subscriptionError = null
  state.rotate.mockClear()
})
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.unstubAllGlobals() })

describe('成员首页', () => {
  it('真实成功响应 fixture：ok 不是限制，首页不能同时声称不可用', async () => {
    state.status = { data_complete: true, access: { algorithm_version: 'access/v1', access: true, proxy_access: 'enabled', access_reasons: ['ok'], used_bytes: 0, effective_quota_bytes: 1073741824 } }
    const wrapper = await view()
    const summary = wrapper.get('[data-testid="service-summary-card"]').text()
    expect(summary).toContain('服务可用')
    expect(summary).not.toContain('服务暂不可用')
    expect(wrapper.find('[data-testid="import-download-button"]').exists()).toBe(true)
  })
  it('不完整周期响应 fixture：unknown 判定不能包装为已确认拒绝或成功', async () => {
    state.status = { data_complete: false, data_issues: ['cycle_unproven'], access: { access: false, proxy_access: 'unknown', access_reasons: ['ok', 'cycle_unproven'], used_bytes: 1024, effective_quota_bytes: 4096 } }
    const wrapper = await view()
    expect(wrapper.get('[data-testid="service-summary-card"]').text()).toContain('服务状态尚未确认')
    expect(wrapper.find('[data-testid="import-download-button"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('当前授权允许使用')
  })
  it.each([
    [{ access: { access: true, proxy_access: 'future_state' } }, 'unknown'],
    [{ access: { proxy_access: 'enabled', access_reasons: ['ok'] } }, 'unknown'],
    [{ access: { access: false, proxy_access: 'enabled' } }, 'unknown'],
    [{ access: { access: false, proxy_access: 'over_quota', access_reasons: ['over_quota'] } }, 'denied'],
    [{ access: { access: false, proxy_access: 'expired', access_reasons: ['binding_expired'] } }, 'denied'],
  ] as const)('只有明确一致的访问判定才可信：%j', (response, expected) => {
    expect(memberAccessState(response as MeStatus)).toBe(expected)
  })
  it.each([true, false])('状态页也使用同一判定，ok 不产生限制、空判定不产生成功（access=%s）', async allowed => {
    state.status = allowed ? { access: { access: true, proxy_access: 'enabled', access_reasons: ['ok'] } } : { access: {} }
    const wrapper = mount(MeStatusView, { global: { stubs } })
    wrappers.push(wrapper)
    await flushPromises()
    expect(wrapper.text()).not.toContain('现在还不能使用代理')
    expect(wrapper.text()).not.toContain('限制原因：ok')
    expect(wrapper.text().includes('权限正常，没有限制。')).toBe(allowed)
    if (!allowed) expect(wrapper.text()).toContain('服务状态尚未确认')
  })
  it('先展示状态、已用与剩余，重置入口下沉，复制不触发重置', async () => {
    const wrapper = await view()
    expect(wrapper.text()).toContain('我的服务')
    expect(wrapper.text()).toContain('3.00 KiB')
    expect(wrapper.text()).toContain('数据更新时间：暂未提供')
    expect(wrapper.get('[data-testid="service-summary-card"]').element.compareDocumentPosition(wrapper.get('[data-testid="client-import-card"]').element) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    const copy = wrapper.findAll('button').find(button => button.attributes('aria-label') === '复制')!
    await copy.trigger('click')
    await flushPromises()
    expect(state.rotate).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="nav-member-credentials"]').attributes('href')).toBe('/me/credentials')
  })
  it.each(['no_binding', 'over_quota', 'binding_expired', 'proxy_paused'])('不可用原因 %s 不提供导入链接', async code => {
    state.status = { access: { access: false, access_reasons: [code] } }
    const wrapper = await view()
    expect(wrapper.findComponent({ name: 'CopyField' }).exists()).toBe(false)
    expect(wrapper.text()).toContain('请联系')
    expect(wrapper.text()).not.toContain(code)
    expect(wrapper.find('[data-testid="nav-service-usage"]').exists()).toBe(true)
  })
  it('状态未知不显示零用量、不限量或服务可用', async () => {
    state.status = {}
    const wrapper = await view()
    expect(wrapper.text()).toContain('服务状态尚未确认')
    expect(wrapper.text()).not.toContain('0 B')
    expect(wrapper.find('input').exists()).toBe(false)
  })
  it('明确的不限量保留 null，不退回成员额度', async () => {
    state.status = { access: { access: true, effective_quota_bytes: null }, member: { effective_quota_bytes: 100 } as MeStatus['member'] }
    const wrapper = await view()
    expect(wrapper.get('[data-testid="service-summary-card"]').text()).toContain('不限量')
  })
  it('维护只降级导入区，已知额度保留', async () => {
    state.subscriptionError = new ApiError(409, { code: 'RECOVERY_MODE', message: 'recovery' })
    const wrapper = await view()
    expect(wrapper.get('[data-testid="import-maintenance-message"]').text()).toContain('恢复核对')
    expect(wrapper.get('[data-testid="service-summary-card"]').text()).toContain('3.00 KiB')
    expect(wrapper.text()).toContain('我的入口')
  })
  it('不兼容格式无复制、二维码或下载', async () => {
    state.subscription.format_status![0] = { format: 'v2rayn', available: false, reason: 'protocol_unsupported:flow' }
    const wrapper = await view()
    expect(wrapper.text()).toContain('无法表达')
    expect(wrapper.find('input').exists()).toBe(false)
    expect(wrapper.find('[data-testid="import-download-button"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="import-check-button"]').attributes('disabled')).toBeDefined()
  })
  it('旧后端无格式状态时先检查，成功仅说明配置取回', async () => {
    delete state.subscription.format_status
    const fetcher = vi.fn(async () => new Response('vless://test-node', { status: 200 }))
    vi.stubGlobal('fetch', fetcher)
    const wrapper = await view()
    expect(wrapper.find('input').exists()).toBe(false)
    await wrapper.get('[data-testid="import-check-button"]').trigger('click')
    await flushPromises()
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(wrapper.find('input').exists()).toBe(true)
    expect(wrapper.get('[data-testid="import-result-message"]').text()).toContain('尚未验证真实连接')
  })
  it('检查失败不泄露响应原文或提供链接', async () => {
    delete state.subscription.format_status
    vi.stubGlobal('fetch', vi.fn(async () => new Response('sensitive-debug-output', { status: 422 })))
    const wrapper = await view()
    await wrapper.get('[data-testid="import-check-button"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('input').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('sensitive-debug-output')
    expect(wrapper.text()).toContain('无法表达')
  })
})
