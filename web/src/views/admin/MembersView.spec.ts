import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import MembersView from './MembersView.vue'

// 真实端点形状：列表行是服务端 decorateMember 派生后的扁平成员记录。
const fixture = vi.hoisted(() => ({
  items: [
    {
      id: 'mem_01',
      version: 1,
      username: 'alice',
      role: 'member',
      active: true,
      proxy_enabled: true,
      subscription_id: 'sub_01',
      expires_at: null,
      quota_override_bytes: null,
      quota_override_enabled: false,
      used_bytes: 1024,
      effective_quota_bytes: 1073741824,
      access_reasons: [],
      proxy_access: 'enabled',
      over_quota: false,
    },
    {
      id: 'mem_02',
      version: 2,
      username: 'bob',
      role: 'member',
      active: true,
      proxy_enabled: true,
      subscription_id: null,
      expires_at: null,
      quota_override_bytes: null,
      quota_override_enabled: false,
      used_bytes: 0,
      effective_quota_bytes: null,
      access_reasons: [],
      proxy_access: 'no_subscription',
      over_quota: false,
    },
  ],
}))

vi.mock('@/api/endpoints', () => ({
  adminApi: {
    members: {
      list: vi.fn(async () => ({ items: fixture.items, next_cursor: null })),
      update: vi.fn(),
      create: vi.fn(),
      remove: vi.fn(),
      rotateSubscriptionToken: vi.fn(),
      rotateCredentials: vi.fn(),
    },
    subscriptions: { list: vi.fn(async () => ({ items: [], next_cursor: null })) },
    nodes: { list: vi.fn(async () => ({ items: [], next_cursor: null })) },
  },
}))

function mountView() {
  return mount(MembersView, {
    global: {
      // 关闭态的预览弹窗不参与断言，但它会 useRouter()；用桩避免无路由注入告警。
      stubs: { RouterLink: { props: ['to'], template: '<a><slot /></a>' }, ImpactPreviewDialog: true },
    },
  })
}

describe('MembersView 过滤空状态', () => {
  it('过滤无匹配时显示显式空状态并可一键清空', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.findAll('table tbody tr')).toHaveLength(2)
    expect(wrapper.text()).not.toContain('当前页没有匹配的成员')

    await wrapper.find('input[aria-label="过滤成员"]').setValue('zzz-no-such-member')
    expect(wrapper.text()).toContain('当前页没有匹配的成员')
    // 不能再是“只剩表头”的空表体。
    expect(wrapper.find('table').exists()).toBe(false)

    const clear = wrapper.findAll('button').find((button) => button.text() === '清空过滤')
    expect(clear).toBeTruthy()
    await clear!.trigger('click')
    expect(wrapper.text()).not.toContain('当前页没有匹配的成员')
    expect(wrapper.findAll('table tbody tr')).toHaveLength(2)
  })

  it('过滤命中时只显示匹配行', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.find('input[aria-label="过滤成员"]').setValue('alice')
    const rows = wrapper.findAll('table tbody tr')
    expect(rows).toHaveLength(1)
    expect(rows[0]!.text()).toContain('alice')
  })
})
