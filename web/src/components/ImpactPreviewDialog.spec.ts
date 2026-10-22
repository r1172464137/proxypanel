import { afterEach, describe, expect, it, vi } from 'vitest'
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
  expires_at: '2026-10-22T10:00:00Z',
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

  it('勾选确认后带 Idempotency-Key 提交，并 emit submitted/close 后跳转批次页', async () => {
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
    expect(onClose).toHaveBeenCalled()
    expect(mocks.push).toHaveBeenCalledWith('/admin/batches/batch_09')
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
