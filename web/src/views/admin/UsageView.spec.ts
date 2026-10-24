import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { UsageItem } from '@/api/types'
import UsageView from './UsageView.vue'

const mocks = vi.hoisted(() => ({ query: vi.fn() }))
vi.mock('@/api/endpoints', () => ({ adminApi: {
  usage: { query: mocks.query },
  members: { list: async () => ({ items: [], next_cursor: null }) },
  nodes: { list: async () => ({ items: [], next_cursor: null }) },
} }))
beforeEach(() => mocks.query.mockReset())
afterEach(() => { document.body.innerHTML = '' })
describe('usage cursor/query boundaries', () => {
  it('consumes real items + next_cursor only and appends with original filters', async () => {
    mocks.query.mockResolvedValueOnce({ items: [{ member_id: 'm1', node_id: 'n1', up_bytes: 1, down_bytes: 2 }], next_cursor: 'usage-page-2' })
      .mockResolvedValueOnce({ items: [{ member_id: 'm2', node_id: 'n2', up_bytes: 3, down_bytes: 4 }], next_cursor: null })
    const wrapper = mount(UsageView)
    await flushPromises()
    await wrapper.find('[data-testid="usage-next-button"]').trigger('click')
    await flushPromises()
    expect(mocks.query).toHaveBeenLastCalledWith({ member_id: undefined, node_id: undefined, from: undefined, to: undefined, limit: 100, cursor: 'usage-page-2' })
    expect(wrapper.text()).toContain('m1')
    expect(wrapper.text()).toContain('m2')
    expect(wrapper.text()).toContain('已加载 2 条')
    expect(wrapper.find('[data-testid="usage-next-button"]').exists()).toBe(false)
    wrapper.unmount()
  })
  it('editing filters retires a pending query; late results cannot repopulate the new scope', async () => {
    let resolve!: (value: { items: UsageItem[]; next_cursor: string | null }) => void
    mocks.query.mockImplementationOnce(() => new Promise(done => { resolve = done }))
      .mockResolvedValueOnce({ items: [{ member_id: 'new-member', up_bytes: 0, down_bytes: 0 }], next_cursor: null })
    const wrapper = mount(UsageView)
    await flushPromises()
    await wrapper.find('#usage-from').setValue('2026-01-01T00:00')
    await wrapper.find('[data-testid="usage-query-button"]').trigger('click')
    await flushPromises()
    resolve({ items: [{ member_id: 'stale-member' }], next_cursor: 'stale-cursor' })
    await flushPromises()
    expect(wrapper.text()).toContain('new-member')
    expect(wrapper.text()).not.toContain('stale-member')
    expect(wrapper.find('[data-testid="usage-next-button"]').exists()).toBe(false)
    expect(mocks.query).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })
})
