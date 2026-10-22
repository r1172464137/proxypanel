import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import AppModal from './AppModal.vue'

function dialogEl(): HTMLElement | null {
  return document.querySelector('[role="dialog"]')
}

function buttonByName(name: string): HTMLButtonElement | undefined {
  return Array.from(document.querySelectorAll('button')).find(
    (button) => button.getAttribute('aria-label') === name || button.textContent?.trim() === name,
  ) as HTMLButtonElement | undefined
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('AppModal', () => {
  it('open 时渲染标题、正文与底部操作', async () => {
    const wrapper = mount(AppModal, {
      props: { open: true, title: '生成订阅链接' },
      slots: { default: '<p>正文内容</p>', footer: '<button type="button">保存</button>' },
    })
    await nextTick()

    const dialog = dialogEl()
    expect(dialog).not.toBeNull()
    expect(dialog!.textContent).toContain('生成订阅链接')
    expect(dialog!.textContent).toContain('正文内容')
    expect(buttonByName('保存')).toBeDefined()
    wrapper.unmount()
  })

  it('open 为 false 时不渲染对话框', () => {
    const wrapper = mount(AppModal, { props: { open: false, title: '隐藏的标题' } })
    expect(dialogEl()).toBeNull()
    wrapper.unmount()
  })

  it('size 映射到不同的最大宽度', async () => {
    const sm = mount(AppModal, { props: { open: true, title: '小', size: 'sm' } })
    await nextTick()
    expect(dialogEl()!.className).toContain('max-w-md')
    sm.unmount()
    document.body.innerHTML = ''

    const md = mount(AppModal, { props: { open: true, title: '中' } })
    await nextTick()
    expect(dialogEl()!.className).toContain('max-w-2xl')
    md.unmount()
    document.body.innerHTML = ''

    const lg = mount(AppModal, { props: { open: true, title: '大', size: 'lg' } })
    await nextTick()
    expect(dialogEl()!.className).toContain('max-w-4xl')
    lg.unmount()
  })

  it('Esc 与关闭按钮都 emit close', async () => {
    const onClose = vi.fn()
    const wrapper = mount(AppModal, { props: { open: true, title: '可关闭' }, attrs: { onClose } })
    await nextTick()

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await nextTick()
    await nextTick()
    expect(onClose).toHaveBeenCalledTimes(1)

    buttonByName('关闭')!.click()
    await nextTick()
    expect(onClose).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('打开时焦点进入对话框', async () => {
    const wrapper = mount(AppModal, { props: { open: false, title: '焦点' } })
    await wrapper.setProps({ open: true })
    await nextTick()
    await nextTick()

    const dialog = dialogEl()
    expect(dialog).not.toBeNull()
    expect(dialog!.contains(document.activeElement)).toBe(true)
    wrapper.unmount()
  })

  it('关闭后把焦点还给打开前的元素', async () => {
    const opener = document.createElement('button')
    opener.textContent = '打开弹窗'
    document.body.appendChild(opener)
    opener.focus()
    expect(document.activeElement).toBe(opener)

    const wrapper = mount(AppModal, { props: { open: false, title: '焦点' } })
    await wrapper.setProps({ open: true })
    await nextTick()
    await nextTick()

    await wrapper.setProps({ open: false })
    await nextTick()
    await nextTick()
    expect(document.activeElement).toBe(opener)
    wrapper.unmount()
  })
})
