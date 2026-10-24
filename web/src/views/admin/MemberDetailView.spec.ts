import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { ApiError } from '@/api/http'
import type { MemberRecord } from '@/api/types'
import MemberDetailView from './MemberDetailView.vue'
const mocks = vi.hoisted(() => ({ get: vi.fn(), update: vi.fn() }))
vi.mock('@/api/endpoints', () => ({ adminApi: {
  members: { get: mocks.get, update: mocks.update }, nodes: { list: async () => ({ items: [], next_cursor: null }) },
} }))
const member: MemberRecord = { id: 'm1', version: 4, username: 'partial', role: 'member', active: true, proxy_enabled: true, subscription_id: 'plan1', expires_at: null, quota_override_bytes: null, quota_override_enabled: false, data_complete: false, data_issues: ['cycle_unproven'], used_bytes: 0, effective_quota_bytes: 0 }
function mountView() {
  return mount(MemberDetailView, { global: { plugins: [createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] })] } })
}
beforeEach(() => { mocks.get.mockReset().mockResolvedValue(member); mocks.update.mockReset() })
describe('member authoritative fields and maintenance refusal', () => {
  it('incomplete data preserves known zero fields and never invents access from the account switch', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.text()).toContain('部分资料待核对')
    expect(wrapper.text()).toContain('0 B / 0 B')
    expect(wrapper.text()).toContain('访问原因未提供')
    expect(wrapper.text()).not.toContain('没有限制，权限正常')
    expect(wrapper.find('[data-testid="member-quota-fields"]').text()).not.toContain('不限量')
    wrapper.unmount()
  })
  it('explicit unlimited and unknown usage are distinct; dynamic write refusal remains visible', async () => {
    mocks.get.mockResolvedValue({ ...member, used_bytes: undefined, effective_quota_bytes: null })
    mocks.update.mockRejectedValue(new ApiError(409, { code: 'RECOVERY_MODE', message: 'maintenance' }))
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.text()).toContain('—（用量未知） / 不限量')
    await wrapper.findAll('button').find(button => button.text() === '暂停代理访问')!.trigger('click')
    await flushPromises()
    expect(mocks.update).toHaveBeenCalledWith('m1', { proxy_enabled: false, version: 4 })
    expect(wrapper.text()).toContain('恢复核对中暂不可用')
    expect(mocks.update).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })
})
