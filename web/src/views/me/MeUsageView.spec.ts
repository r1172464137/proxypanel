import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { api, ApiError } from '@/api/http'
import { unpackMeUsage } from '@/lib/meUsage'
import { memberSummary } from '@/lib/memberService'
import { useResource } from '@/lib/async'
import MeUsageView from './MeUsageView.vue'
import ErrorState from '@/components/ErrorState.vue'

vi.mock('@/api/endpoints', () => ({ meApi: { status: vi.fn(async () => ({ access: { access: true, effective_quota_bytes: 1000 } })) } }))
vi.mock('@/api/http', async (importOriginal) => ({ ...await importOriginal<typeof import('@/api/http')>(), api: { get: vi.fn() } }))
const get = vi.mocked(api.get)
const wrappers: ReturnType<typeof mount>[] = []
// Returning mockReset() returns the mock itself, which Vitest treats as hook cleanup.
beforeEach(() => { get.mockReset() })
afterEach(() => wrappers.splice(0).forEach(wrapper => wrapper.unmount()))
async function view() {
  const wrapper = mount(MeUsageView)
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

describe('成员用量最小闭环', () => {
  it('资源加载捕获拒绝并保留原始 ApiError', async () => {
    const error = new ApiError(503, { code: 'NETWORK_ERROR', message: '资源读取失败' })
    const resource = useResource(async () => { throw error })
    await expect(resource.reload()).resolves.toBeUndefined()
    expect(resource.error.value).toBe(error)
    expect(resource.loading.value).toBe(false)
    expect(resource.data.value).toBeNull()
  })
  it('对象解包保留用量与调整，不接受畸形数据作为零记录', () => {
    const page = unpackMeUsage({ items: { usage: [{ up_bytes: 42 }], adjustments: [{ bytes: -7 }] }, next_cursor: 'next' })
    expect(page.items[0]?.up_bytes).toBe(42)
    expect(page.adjustments[0]?.bytes).toBe(-7)
    expect(page.next_cursor).toBe('next')
    expect(unpackMeUsage({ items: [] })).toEqual({ items: [], adjustments: [], next_cursor: null })
    expect(() => unpackMeUsage({ items: { usage: [], adjustments: null } })).toThrow()
  })
  it('未知已用量不推导满额剩余；不完整资料保留已知值', () => {
    expect(memberSummary({ access: { effective_quota_bytes: 1000 } }).remaining).toBeUndefined()
    expect(memberSummary({ data_complete: false, access: { effective_quota_bytes: 1000, used_bytes: 25 } })).toMatchObject({ remaining: 975, incomplete: true })
    expect(memberSummary({ access: { effective_quota_bytes: 0, used_bytes: 0 } }).remaining).toBe(0)
  })
  it('展示对象中的记录、负调整、分页游标，调整只在第一页', async () => {
    get.mockResolvedValueOnce({ items: { usage: [{ node_id: 'entry-a', up_bytes: 10, down_bytes: 20, estimated: true, boundary_split: true, correction: true }], adjustments: [{ bytes: -7, reason: '人工核对' }] }, next_cursor: 'cursor-2' })
      .mockResolvedValueOnce({ items: { usage: [{ node_id: 'entry-b', up_bytes: 30, down_bytes: 40 }], adjustments: [] }, next_cursor: null })
    const wrapper = await view()
    expect(wrapper.text()).toContain('entry-a')
    expect(wrapper.text()).toContain('-7 B')
    expect(wrapper.text()).toContain('跨周期分摊')
    const summary = wrapper.get('[data-testid="usage-summary-card"]').text()
    expect(summary).toContain('剩余—')
    await wrapper.get('[data-testid="usage-next-button"]').trigger('click')
    await flushPromises()
    expect(get.mock.calls[1]?.[1]?.query).toEqual({ cursor: 'cursor-2' })
    expect(wrapper.text()).toContain('entry-b')
    expect(wrapper.text()).not.toContain('人工核对')
    expect(wrapper.text()).toContain('额度调整记录仅在第一页展示')
    expect(wrapper.get('[data-testid="usage-next-button"]').attributes('disabled')).toBeDefined()
  })
  it('读取失败显示重试而不是空表或明细零合计', async () => {
    get.mockRejectedValueOnce(new ApiError(503, { code: 'NETWORK_ERROR', message: '暂时不可达' }))
      .mockResolvedValueOnce({ items: { usage: [{ node_id: '恢复后的节点', up_bytes: 12, down_bytes: 34 }], adjustments: [] }, next_cursor: null })
    const wrapper = await view()
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('无法连接面板')
    expect(wrapper.find('[data-testid="usage-empty-message"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('本页暂无用量记录')
    expect(wrapper.text()).not.toContain('上 0 B')
    expect(wrapper.text()).toContain('重试')
    expect(get).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[data-testid="usage-summary-card"]').text()).toContain('1000 B')
    await wrapper.getComponent(ErrorState).get('button').trigger('click')
    await flushPromises()
    expect(get).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('恢复后的节点')
    expect(wrapper.text()).toContain('上 12 B · 下 34 B')
  })
})
