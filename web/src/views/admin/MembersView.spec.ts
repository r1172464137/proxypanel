import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import MembersView from './MembersView.vue'
import type { MemberRecord } from '@/api/types'
import { adminApi } from '@/api/endpoints'

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
  ] satisfies MemberRecord[],
}))

vi.mock('@/api/endpoints', () => ({
  adminApi: {
    members: {
      list: vi.fn(async (query: { q?: string }) => ({ items: fixture.items.filter(item => !query.q || item.username.includes(query.q)), next_cursor: null })),
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
      plugins: [createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] })],
      stubs: { ImpactPreviewDialog: true },
    },
  })
}

describe('MembersView 过滤空状态', () => {
  it('cross-page selection previews all explicitly selected IDs, then refresh clears the scope', async () => {
    vi.mocked(adminApi.members.list)
      .mockResolvedValueOnce({ items: [fixture.items[0]!], next_cursor: 'page-2' })
      .mockResolvedValueOnce({ items: [fixture.items[1]!], next_cursor: null })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.find('[aria-label="选择 alice"]').trigger('click')
    await wrapper.findAll('button').find(button => button.text() === '下一页')!.trigger('click')
    await flushPromises()
    await wrapper.find('[aria-label="选择 bob"]').trigger('click')
    expect(wrapper.find('[data-testid="member-selection-scope"]').text()).toContain('已选 2 名（含其它页 1 名）')
    const dialog = wrapper.findAllComponents({ name: 'ImpactPreviewDialog' }).at(-1)!
    expect(dialog.props('memberIds')).toEqual(['mem_01', 'mem_02'])
    await wrapper.find('[data-testid="member-refresh-button"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="member-selection-scope"]').text()).toContain('已选 0 名')
    expect(dialog.props('memberIds')).toEqual([])
    wrapper.unmount()
  })

  it('passes actual plan/archive filters and drops previous selections', async () => {
    vi.mocked(adminApi.subscriptions.list).mockResolvedValueOnce({ items: [{ id: 'plan-2', name: 'Plan Two' } as Awaited<ReturnType<typeof adminApi.subscriptions.list>>['items'][number]], next_cursor: null })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.find('[aria-label="选择 alice"]').trigger('click')
    await wrapper.find('[data-testid="member-subscription-select"]').setValue('plan-2')
    await flushPromises()
    await wrapper.find('[data-testid="member-include-archived-checkbox"]').setValue(true)
    await flushPromises()
    expect(adminApi.members.list).toHaveBeenLastCalledWith({ limit: 50, cursor: null, q: '', subscription_id: 'plan-2', include_archived: true })
    expect(wrapper.find('[data-testid="member-selection-scope"]').text()).toContain('已选 0 名')
    wrapper.unmount()
  })
  it('过滤无匹配时显示显式空状态并可一键清空', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.findAll('table tbody tr')).toHaveLength(2)
    expect(wrapper.text()).not.toContain('当前页没有匹配的成员')

    await wrapper.find('input[aria-label="搜索成员"]').setValue('zzz-no-such-member')
    await wrapper.findAll('button').find(b => b.text() === '搜索全部成员')!.trigger('click')
    await flushPromises()
    expect(adminApi.members.list).toHaveBeenLastCalledWith({ limit: 50, cursor: null, q: 'zzz-no-such-member' })
    expect(wrapper.text()).toContain('没有匹配的成员')
    // 不能再是“只剩表头”的空表体。
    expect(wrapper.find('table').exists()).toBe(false)

    const clear = wrapper.findAll('button').find((button) => button.text() === '清空搜索')
    expect(clear).toBeTruthy()
    await clear!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).not.toContain('当前页没有匹配的成员')
    expect(wrapper.findAll('table tbody tr')).toHaveLength(2)
  })

  it('过滤命中时只显示匹配行', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.find('input[aria-label="搜索成员"]').setValue('alice')
    await wrapper.findAll('button').find(b => b.text() === '搜索全部成员')!.trigger('click')
    await flushPromises()
    expect(adminApi.members.list).toHaveBeenLastCalledWith({ limit: 50, cursor: null, q: 'alice' })
    const rows = wrapper.findAll('table tbody tr')
    expect(rows).toHaveLength(1)
    expect(rows[0]!.text()).toContain('alice')
  })
})
