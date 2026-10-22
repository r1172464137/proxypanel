import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import ConfirmDialog from './ConfirmDialog.vue'

function buttons(): HTMLButtonElement[] {
  return Array.from(document.querySelectorAll('button'))
}

function buttonByText(text: string): HTMLButtonElement | undefined {
  return buttons().find((button) => button.textContent?.trim() === text)
}

function alertText(): string {
  return Array.from(document.querySelectorAll('[role="alert"]'))
    .map((node) => node.textContent ?? '')
    .join(' ')
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('ConfirmDialog', () => {
  it('渲染标题与说明，确认/取消分别 emit', async () => {
    const onConfirm = vi.fn()
    const onCancel = vi.fn()
    const wrapper = mount(ConfirmDialog, {
      props: { open: true, title: '删除订阅' },
      attrs: { onConfirm, onCancel },
      slots: { default: '<p>删除后无法恢复。</p>' },
    })
    await nextTick()

    const dialog = document.querySelector('[role="alertdialog"]')
    expect(dialog).not.toBeNull()
    expect(dialog!.textContent).toContain('删除订阅')
    expect(dialog!.textContent).toContain('删除后无法恢复。')

    buttonByText('确认')!.click()
    await nextTick()
    expect(onConfirm).toHaveBeenCalledTimes(1)

    buttonByText('取消')!.click()
    await nextTick()
    expect(onCancel).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('pending 时确认按钮禁用并显示处理中', async () => {
    const wrapper = mount(ConfirmDialog, { props: { open: true, title: '处理中', pending: true } })
    await nextTick()

    const busy = buttonByText('处理中…')
    expect(busy).toBeDefined()
    expect(busy!.disabled).toBe(true)
    wrapper.unmount()
  })

  it('danger 时确认按钮用 destructive 样式', async () => {
    const wrapper = mount(ConfirmDialog, {
      props: { open: true, title: '危险', danger: true, confirmLabel: '删除' },
    })
    await nextTick()

    const confirm = buttonByText('删除')!
    expect(confirm.className).toContain('bg-destructive')
    wrapper.unmount()
  })

  it('errorText 渲染为 destructive 提示', async () => {
    const wrapper = mount(ConfirmDialog, {
      props: { open: true, title: '失败', errorText: '权限不足，无法删除' },
    })
    await nextTick()

    expect(alertText()).toContain('权限不足，无法删除')
    wrapper.unmount()
  })
})
