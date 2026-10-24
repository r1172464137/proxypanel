import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { ApiError } from '@/api/http'
import MeSecurityView from './MeSecurityView.vue'
const mocks = vi.hoisted(() => ({ password: vi.fn(), session: vi.fn(), push: vi.fn(), clear: vi.fn() }))
vi.mock('@/api/endpoints', () => ({ authApi: { session: mocks.session }, meApi: { changePassword: mocks.password, sessions: vi.fn(async () => ({ items: [], next_cursor: null })) } }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: mocks.push }) }))
vi.mock('@/stores/session', () => ({ clearSession: mocks.clear }))
vi.mock('@/lib/toast', () => ({ pushToast: vi.fn() }))
afterEach(() => vi.clearAllMocks())
async function submit() {
  const wrapper = mount(MeSecurityView)
  await flushPromises()
  await wrapper.get('#pw-current').setValue('wrong-current')
  await wrapper.get('#pw-next').setValue('new-password-123')
  await wrapper.get('#pw-confirm').setValue('new-password-123')
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  return wrapper
}
describe('改密认证核对', () => {
  it('错误当前密码不踢掉仍有效的会话', async () => {
    mocks.password.mockRejectedValue(new ApiError(401, { code: 'UNAUTHENTICATED', message: 'unauthenticated' }))
    mocks.session.mockResolvedValue({ user: { id: 'self' } })
    const wrapper = await submit()
    expect(wrapper.text()).toContain('当前密码不正确')
    expect((wrapper.get('#pw-current').element as HTMLInputElement).value).toBe('wrong-current')
    expect(mocks.clear).not.toHaveBeenCalled()
    expect(mocks.push).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('只读核对确认会话失效后才转到登录', async () => {
    const failure = new ApiError(401, { code: 'UNAUTHENTICATED', message: 'unauthenticated' })
    mocks.password.mockRejectedValue(failure)
    mocks.session.mockRejectedValue(failure)
    const wrapper = await submit()
    expect(mocks.clear).toHaveBeenCalledOnce()
    expect(mocks.push).toHaveBeenCalledWith({ name: 'login', query: { next: '/me/security' } })
    wrapper.unmount()
  })
  it('成功持续展示真实撤销数量', async () => {
    mocks.password.mockResolvedValue({ updated: true, sessions_revoked: 3 })
    const wrapper = await submit()
    expect(wrapper.get('[role="status"]').text()).toContain('已退出其他 3 个会话')
    expect((wrapper.get('#pw-current').element as HTMLInputElement).value).toBe('')
    wrapper.unmount()
  })
})
