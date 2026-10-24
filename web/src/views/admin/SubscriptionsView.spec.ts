import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import SubscriptionsView from './SubscriptionsView.vue'

const mocks = vi.hoisted(() => ({ list: vi.fn(), remove: vi.fn(), toast: vi.fn() }))
vi.mock('@/api/endpoints', () => ({ adminApi: { subscriptions: { list: mocks.list, remove: mocks.remove } } }))
vi.mock('@/lib/toast', () => ({ pushToast: mocks.toast }))

beforeEach(() => {
  vi.clearAllMocks()
  mocks.list.mockResolvedValue({ items: [{ id: 'sub', name: '方案', version: 1, node_group_ids: [], quota_bytes: null, archived: false, bound_members: 0 }], next_cursor: null })
})
afterEach(() => { document.body.innerHTML = '' })

describe('SubscriptionsView DELETE feedback', () => {
  it.each([true, false])('retired=%s distinguishes preserved history from physical deletion', async (retired) => {
    mocks.remove.mockResolvedValue({ retired })
    const wrapper = mount(SubscriptionsView, { attachTo: document.body, global: { stubs: { RouterLink: true } } })
    await flushPromises()
    await wrapper.find('[data-testid="subscription-retire-button-sub"]').trigger('click')
    await flushPromises()
    const confirm = [...document.querySelectorAll('.modal__footer button')].find((b) => !b.textContent?.includes('取消')) as HTMLButtonElement
    expect(confirm).toBeTruthy()
    confirm.click()
    await flushPromises()
    expect(mocks.remove).toHaveBeenCalledWith('sub', 1)
    expect(mocks.toast).toHaveBeenCalledWith('success', retired ? '方案已退役，历史保留' : '方案已删除')
    expect(mocks.list).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })
})
