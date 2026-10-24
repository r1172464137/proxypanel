import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { sessionState } from '@/stores/session'
import { readPending, savePending, type PendingBatch } from '@/lib/batchPending'
import { ApiError } from '@/api/http'
import { flushPromises, mount } from '@vue/test-utils'
import type { BatchPreview } from '@/api/types'

const mocks = vi.hoisted(() => ({
  preview: vi.fn(),
  submit: vi.fn(),
  push: vi.fn(async () => undefined),
  pushToast: vi.fn(),
}))

vi.mock('@/api/endpoints', () => ({
  adminApi: { batches: { preview: mocks.preview, submit: mocks.submit } },
}))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: mocks.push }) }))
vi.mock('@/lib/toast', () => ({ pushToast: mocks.pushToast }))

import ImpactPreviewDialog from './ImpactPreviewDialog.vue'

const preview: BatchPreview = {
  id: 'bpv_01',
  version: 4,
  affected_count: 2,
  affected_nodes: 7,
  exceptions: [{ member_id: 'm_02', reason: '已过期' }],
  warnings: ['会中断现有连接'],
  expires_at: '2099-10-22T10:00:00Z',
}

function mountDialog() {
  return mount(ImpactPreviewDialog, {
    props: {
      open: false,
      operation: 'set_expiry',
      memberIds: ['m_01', 'm_02'],
      values: { days: 30 },
    },
  })
}

function buttonByText(text: string): HTMLButtonElement {
  const button = Array.from(document.querySelectorAll('button')).find(
    (item) => item.textContent?.trim() === text,
  )
  if (!button) throw new Error(`按钮「${text}」未找到`)
  return button as HTMLButtonElement
}

beforeEach(() => {
  localStorage.clear()
  sessionState.status = 'ready'
  sessionState.user = { id: 'admin-a', username: 'a', role: 'admin' }
})

afterEach(() => {
  document.body.innerHTML = ''
  vi.clearAllMocks()
})

describe('ImpactPreviewDialog', () => {
  it('打开时请求影响预览并展示结果（请求参数保持原样）', async () => {
    mocks.preview.mockResolvedValue(preview)
    const wrapper = mountDialog()
    await wrapper.setProps({ open: true })
    await flushPromises()

    expect(mocks.preview).toHaveBeenCalledTimes(1)
    expect(mocks.preview).toHaveBeenCalledWith({
      operation: 'set_expiry',
      member_ids: ['m_01', 'm_02'],
      values: { days: 30 },
    })

    const text = document.body.textContent ?? ''
    expect(text).toContain('影响成员 2 名')
    expect(text).toContain('受影响节点 7 个')
    expect(text).toContain('会中断现有连接')
    expect(text).toContain('m_02：已过期')
    expect(text).toContain('预览版本 4')

    // 未勾选确认前不允许提交
    expect(buttonByText('确认提交').disabled).toBe(true)
    await buttonByText('确认提交').click()
    await flushPromises()
    expect(mocks.submit).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('勾选确认后带 Idempotency-Key 提交，保留本次记录链接供持续核对', async () => {
    mocks.preview.mockResolvedValue(preview)
    mocks.submit.mockResolvedValue({ id: 'batch_09' })
    const onSubmit = vi.fn()
    const onClose = vi.fn()
    const wrapper = mount(ImpactPreviewDialog, {
      props: {
        open: false,
        operation: 'set_expiry',
        memberIds: ['m_01', 'm_02'],
        values: { days: 30 },
      },
      attrs: { onSubmitted: onSubmit, onClose },
    })
    await wrapper.setProps({ open: true })
    await flushPromises()

    const checkbox = document.querySelector('input[type="checkbox"]') as HTMLInputElement
    expect(checkbox).not.toBeNull()
    checkbox.click()
    await flushPromises()

    const submitButton = buttonByText('确认提交')
    expect(submitButton.disabled).toBe(false)
    submitButton.click()
    await flushPromises()

    expect(mocks.submit).toHaveBeenCalledTimes(1)
    const [request, idempotencyKey] = mocks.submit.mock.calls[0] as [unknown, string]
    expect(request).toEqual({ preview_id: 'bpv_01', confirm: true })
    expect(typeof idempotencyKey).toBe('string')
    expect(idempotencyKey.length).toBeGreaterThan(8)

    expect(onSubmit).toHaveBeenCalledWith('batch_09')
    expect(onClose).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('查看本次变更记录')
    ;(document.querySelector('a[href="/admin/batches/batch_09"]') as HTMLAnchorElement).click()
    await flushPromises()
    expect(mocks.push).toHaveBeenCalledWith('/admin/batches/batch_09')
    expect(readPending('admin-a')).toEqual([])
    wrapper.unmount()
  })

  it('提交响应丢失后保留同一幂等键，只核对同一次提交', async () => {
    mocks.preview.mockResolvedValue(preview)
    mocks.submit.mockRejectedValueOnce(new Error('网络中断')).mockResolvedValueOnce({ id: 'batch-replay', idempotent_replay: true })
    const wrapper = mountDialog()
    await wrapper.setProps({ open: true })
    await flushPromises()
    ;(document.querySelector('input[type="checkbox"]') as HTMLInputElement).click()
    await flushPromises()
    buttonByText('确认提交').click()
    await flushPromises()
    expect(document.body.textContent).toContain('结果未确认')
    expect(buttonByText('重新预览').disabled).toBe(true)
    buttonByText('核对同一次提交').click()
    await flushPromises()
    expect(mocks.submit).toHaveBeenCalledTimes(2)
    expect(mocks.submit.mock.calls[0]).toEqual(mocks.submit.mock.calls[1])
    expect(document.body.textContent).toContain('这是之前那次提交的结果')
    expect(document.querySelector('a')?.getAttribute('href')).toBe('/admin/batches/batch-replay')
    expect(readPending('admin-a')).toEqual([])
    wrapper.unmount()
  })

  it('刷新后从独立入口找到原提交，不重新预览并使用相同请求和 key', async () => {
    mocks.preview.mockResolvedValue(preview)
    mocks.submit.mockImplementationOnce(async () => {
      expect(readPending('admin-a')).toHaveLength(1) // 请求前已持久化
      throw new Error('响应丢失')
    }).mockResolvedValueOnce({ id: 'recovered', idempotent_replay: true })
    const original = mountDialog()
    await original.setProps({ open: true })
    await flushPromises()
    ;(document.querySelector('input[type="checkbox"]') as HTMLInputElement).click()
    await flushPromises()
    buttonByText('确认提交').click()
    await flushPromises()
    const call = mocks.submit.mock.calls[0]
    original.unmount()
    const restored = mount(ImpactPreviewDialog, { props: { open: true, recoverPending: true, operation: 'assign_subscription', memberIds: [], values: {} } })
    await flushPromises()
    expect(document.body.textContent).toContain('当前账号未确认提交（1 项）')
    const choose = Array.from(document.querySelectorAll('button')).find((b) => b.textContent?.includes('核对 调整成员有效期'))!
    choose.click()
    await flushPromises()
    expect(document.body.textContent).toContain('原提交预览 bpv_01')
    buttonByText('核对同一次提交').click()
    await flushPromises()
    expect(mocks.preview).toHaveBeenCalledTimes(1)
    expect(mocks.submit.mock.calls[1]).toEqual(call)
    expect(document.body.textContent).toContain('这是之前那次提交的结果')
    expect(readPending('admin-a')).toEqual([])
    restored.unmount()
  })

  it('账号切换与退出使旧弹窗失效，不向新账号暴露或提交旧 pending', async () => {
    mocks.preview.mockResolvedValue(preview)
    mocks.submit.mockRejectedValue(new Error('网络中断'))
    const wrapper = mountDialog()
    await wrapper.setProps({ open: true })
    await flushPromises()
    ;(document.querySelector('input[type="checkbox"]') as HTMLInputElement).click()
    await flushPromises()
    buttonByText('确认提交').click()
    await flushPromises()
    sessionState.user = { id: 'admin-b', username: 'b', role: 'admin' }
    await flushPromises()
    expect(document.body.textContent).not.toContain('结果未确认')
    expect(buttonByText('确认提交').disabled).toBe(true)
    expect(readPending('admin-a')).toHaveLength(1)
    expect(readPending('admin-b')).toEqual([])
    sessionState.user = null
    sessionState.status = 'anonymous'
    await flushPromises()
    expect(document.body.textContent).toContain('当前账号未确认')
    expect(mocks.submit).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('写入失败停止发送，明确不能安全恢复，不假成功或自动重试', async () => {
    mocks.preview.mockResolvedValue(preview)
    const wrapper = mountDialog()
    await wrapper.setProps({ open: true })
    await flushPromises()
    ;(document.querySelector('input[type="checkbox"]') as HTMLInputElement).click()
    await flushPromises()
    const spy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('quota') })
    buttonByText('确认提交').click()
    await flushPromises()
    expect(mocks.submit).not.toHaveBeenCalled()
    expect(mocks.pushToast).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('无法安全恢复')
    expect(buttonByText('确认提交').disabled).toBe(true)
    spy.mockRestore()
    wrapper.unmount()
  })

  it('恢复时即使原预览已过期仍只回放同 key；服务端明确拒绝后清理且不放宽确认', async () => {
    const record: PendingBatch = {
      accountId: 'admin-a', operation: 'set_expiry', scope: 'old-scope', targetDigest: 'old-targets', targetCount: 2,
      key: 'original-key', request: { preview_id: 'expired-preview', rules_version: 3, member_set_digest: 'original-digest', confirm: true },
    }
    savePending(record)
    mocks.submit.mockRejectedValueOnce(new ApiError(409, { code: 'PREVIEW_CONCLUSION_CHANGED', message: '结论已变化' }))
    const wrapper = mount(ImpactPreviewDialog, { props: { open: true, recoverPending: true, operation: 'assign_subscription', memberIds: [], values: {} } })
    await flushPromises()
    const choose = Array.from(document.querySelectorAll('button')).find((b) => b.textContent?.includes('核对 调整成员有效期'))!
    choose.click()
    await flushPromises()
    buttonByText('核对同一次提交').click()
    await flushPromises()
    expect(mocks.submit).toHaveBeenCalledWith(record.request, 'original-key')
    expect(mocks.preview).not.toHaveBeenCalled()
    expect(readPending('admin-a')).toEqual([])
    expect(document.body.textContent).toContain('当前账号未确认提交（0 项）')
    expect(document.body.textContent).toContain('结论已变化')
    expect(mocks.pushToast).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('旧账号在途响应不向新账号展示成功，不清除旧账号记录', async () => {
    mocks.preview.mockResolvedValue(preview)
    let resolve!: (value: { id: string }) => void
    mocks.submit.mockImplementationOnce(() => new Promise((done) => { resolve = done }))
    const wrapper = mountDialog()
    await wrapper.setProps({ open: true })
    await flushPromises()
    ;(document.querySelector('input[type="checkbox"]') as HTMLInputElement).click()
    await flushPromises()
    buttonByText('确认提交').click()
    await flushPromises()
    sessionState.user = { id: 'admin-b', username: 'b', role: 'admin' }
    await flushPromises()
    resolve({ id: 'old-account-result' })
    await flushPromises()
    expect(mocks.pushToast).not.toHaveBeenCalled()
    expect(mocks.push).not.toHaveBeenCalled()
    expect(document.body.textContent).not.toContain('old-account-result')
    expect(readPending('admin-a')).toHaveLength(1)
    wrapper.unmount()
  })

  it('预览失败时展示错误并允许重新预览', async () => {
    mocks.preview.mockRejectedValueOnce(new Error('网络中断'))
    mocks.preview.mockResolvedValueOnce(preview)
    const wrapper = mountDialog()
    await wrapper.setProps({ open: true })
    await flushPromises()

    expect(document.querySelector('[role="alert"]')).not.toBeNull()
    await buttonByText('重新预览').click()
    await flushPromises()
    expect(mocks.preview).toHaveBeenCalledTimes(2)
    expect(document.body.textContent).toContain('预览版本 4')
    wrapper.unmount()
  })
})
