import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { defineComponent } from 'vue'
import type { BatchDetail } from '@/api/types'
import BatchListView from './BatchListView.vue'

const fixture = vi.hoisted(() => ({ items: [] as BatchDetail[] }))
const page = vi.hoisted(() => vi.fn())

vi.mock('@/api/http', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/http')>()
  return { ...actual, api: { ...actual.api, page } }
})

// 直接用内存路由挂载：router-link 需要真实路由注入（桩在 production 构建下不生效）。
const Blank = defineComponent({ render: () => null })

function mountView() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/admin/batches', component: BatchListView },
      { path: '/admin/batches/:id', component: Blank },
      { path: '/admin/members', component: Blank },
      { path: '/admin/tasks', component: Blank },
    ],
  })
  return mount(BatchListView, { global: { plugins: [router] } })
}

beforeEach(() => {
  fixture.items = []
  page.mockReset().mockImplementation(async () => ({ items: fixture.items, next_cursor: null }))
})

afterEach(() => {
  document.body.innerHTML = ''
})

describe('BatchListView', () => {
  it('无批量任务时说明来源，不渲染空表', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(page).toHaveBeenCalledWith('/api/v1/admin/batches', { query: { limit: 50 } })
    expect(wrapper.find('table').exists()).toBe(false)
    expect(wrapper.text()).toContain('还没有批量任务')
    expect(wrapper.text()).toContain('批量操作提交后会出现在这里')
    wrapper.unmount()
  })

  it('列表展示 id / 操作 / 状态 / 成员数，并链接到详情', async () => {
    fixture.items = [
      {
        id: 'bat_01',
        state: 'running',
        operation: 'assign_subscription',
        created_at: '2026-10-20T06:00:00Z',
        affected_count: 2,
        results: [
          { member_id: 'm1', state: 'succeeded' },
          { member_id: 'm2', state: 'pending' },
        ],
      },
    ]
    const wrapper = mountView()
    await flushPromises()

    const row = wrapper.find('table tbody tr')
    expect(row.exists()).toBe(true)
    expect(row.text()).toContain('bat_01')
    expect(row.text()).toContain('分配 / 切换订阅')
    expect(row.text()).toContain('运行中')
    expect(row.text()).toContain('2')
    expect(row.text()).toContain('1 / 2')
    expect(row.find('a[href="/admin/batches/bat_01"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('未知 operation 原样展示，未知 state 降级为未知', async () => {
    fixture.items = [{ id: 'bat_02', operation: 'custom_op', state: undefined }]
    const wrapper = mountView()
    await flushPromises()

    const row = wrapper.find('table tbody tr')
    expect(row.text()).toContain('custom_op')
    expect(row.text()).toContain('未知')
    wrapper.unmount()
  })
})
