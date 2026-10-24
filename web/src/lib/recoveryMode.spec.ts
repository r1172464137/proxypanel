import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h, nextTick } from 'vue'

vi.mock('@/stores/session', async () => {
  const { reactive } = await import('vue')
  return { sessionState: reactive({ user: null as null | { id: string; username: string; role: string } }) }
})
vi.mock('@/api/endpoints', () => ({ adminApi: { recoveryMode: { get: vi.fn() } } }))
import { sessionState } from '@/stores/session'
import { adminApi } from '@/api/endpoints'
import { clearRecoveryMode, mutateRecoveryMode, useRecoveryMode } from './recoveryMode'
const get = vi.mocked(adminApi.recoveryMode.get)
const wrappers: ReturnType<typeof mount>[] = []
function reader() {
  let resource!: ReturnType<typeof useRecoveryMode>
  const wrapper = mount(defineComponent({ setup() { resource = useRecoveryMode(); return () => h('p', String(resource.data.value?.recovery_mode ?? 'unknown')) } }))
  wrappers.push(wrapper)
  return { wrapper, resource }
}
function admin(id = 'a') { sessionState.user = { id, username: id, role: 'admin' } }
afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  sessionState.user = null
  clearRecoveryMode()
  vi.clearAllMocks()
  vi.useRealTimers()
})
describe('恢复状态共享资源的身份和失效边界', () => {
  it('成员不调用管理员接口；两个读取方共享一次请求和一次轮询', async () => {
    vi.useFakeTimers()
    sessionState.user = { id: 'm', username: 'm', role: 'member' }
    reader(); reader()
    await flushPromises()
    expect(get).not.toHaveBeenCalled()
    get.mockResolvedValue({ recovery_mode: true } as Awaited<ReturnType<typeof adminApi.recoveryMode.get>>)
    admin()
    await flushPromises()
    expect(get).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(30_000)
    expect(get).toHaveBeenCalledTimes(2)
  })
  it('登出清空缓存，迟到的管理员回复不能污染成员状态', async () => {
    let resolve!: (value: Awaited<ReturnType<typeof adminApi.recoveryMode.get>>) => void
    get.mockImplementationOnce(() => new Promise(done => { resolve = done }))
    admin()
    const { resource } = reader()
    sessionState.user = null
    sessionState.user = { id: 'm', username: 'm', role: 'member' }
    resolve({ recovery_mode: true } as Awaited<ReturnType<typeof adminApi.recoveryMode.get>>)
    await flushPromises()
    expect(resource.data.value).toBeNull()
    expect(resource.loaded.value).toBe(false)
    expect(get).toHaveBeenCalledTimes(1)
  })
  it('进入/退出后所有读取方立即重新核对；换管理员不复用旧缓存', async () => {
    get.mockResolvedValue({ recovery_mode: false } as Awaited<ReturnType<typeof adminApi.recoveryMode.get>>)
    admin()
    const first = reader(); const second = reader()
    await flushPromises()
    get.mockResolvedValue({ recovery_mode: true } as Awaited<ReturnType<typeof adminApi.recoveryMode.get>>)
    await mutateRecoveryMode(async () => ({ recovery_mode: true }))
    await nextTick()
    expect(first.wrapper.text()).toBe('true')
    expect(second.wrapper.text()).toBe('true')
    expect(get).toHaveBeenCalledTimes(2)
    admin('other')
    expect(first.resource.data.value).toBeNull()
    await flushPromises()
    expect(get).toHaveBeenCalledTimes(3)
  })
})
