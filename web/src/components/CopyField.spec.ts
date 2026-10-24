import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'

vi.mock('@/lib/clipboard', () => ({ copyText: vi.fn(async () => true) }))
vi.mock('@/lib/toast', () => ({ pushToast: vi.fn() }))

import { copyText } from '@/lib/clipboard'
import CopyField from './CopyField.vue'

const VALUE = 'https://panel.test/sub/abcdef0123456789'

function inputValue(wrapper: ReturnType<typeof mount>): string {
  return (wrapper.find('input').element as HTMLInputElement).value
}

function buttonByLabel(wrapper: ReturnType<typeof mount>, label: string) {
  return wrapper.findAll('button').find((button) => button.attributes('aria-label') === label)
}

afterEach(() => {
  vi.clearAllMocks()
})

describe('CopyField', () => {
  it('默认脱敏显示，不渲染原值', () => {
    const wrapper = mount(CopyField, { props: { value: VALUE, label: '订阅链接' } })
    const shown = inputValue(wrapper)
    expect(shown).not.toBe(VALUE)
    expect(shown).toContain('…')
    expect(shown.startsWith(VALUE.slice(0, 6))).toBe(true)
    expect(shown.endsWith(VALUE.slice(-4))).toBe(true)
    wrapper.unmount()
  })

  it('revealable 时可切换显示原值', async () => {
    const wrapper = mount(CopyField, { props: { value: VALUE } })

    await buttonByLabel(wrapper, '显示数值')!.trigger('click')
    await nextTick()
    expect(inputValue(wrapper)).toBe(VALUE)

    await buttonByLabel(wrapper, '隐藏数值')!.trigger('click')
    await nextTick()
    expect(inputValue(wrapper)).not.toBe(VALUE)
    wrapper.unmount()
  })

  it('mask 为 false 时直接显示原值', () => {
    const wrapper = mount(CopyField, { props: { value: VALUE, mask: false } })
    expect(inputValue(wrapper)).toBe(VALUE)
    wrapper.unmount()
  })

  it('点复制调用剪贴板函数，并在 2 秒后恢复图标状态', async () => {
    vi.useFakeTimers()
    const wrapper = mount(CopyField, { props: { value: VALUE } })

    await buttonByLabel(wrapper, '复制')!.trigger('click')
    await nextTick()
    expect(copyText).toHaveBeenCalledWith(VALUE)
    expect(buttonByLabel(wrapper, '已复制')).toBeDefined()

    vi.advanceTimersByTime(2000)
    await nextTick()
    expect(buttonByLabel(wrapper, '复制')).toBeDefined()
    wrapper.unmount()
    vi.useRealTimers()
  })

  it('复制被拒绝时不报成功，显示原文并选择，允许手动复制', async () => {
    vi.mocked(copyText).mockResolvedValueOnce(false)
    const wrapper = mount(CopyField, { props: { value: VALUE } })
    await buttonByLabel(wrapper, '复制')!.trigger('click')
    await nextTick()
    expect(buttonByLabel(wrapper, '已复制')).toBeUndefined()
    expect(wrapper.text()).toContain('复制失败')
    const input = wrapper.get('input').element as HTMLInputElement
    expect(input.value).toBe(VALUE)
    expect(input.selectionStart).toBe(0)
    expect(input.selectionEnd).toBe(VALUE.length)
    wrapper.unmount()
  })

  it('值改变后不继承旧复制成功提示和明文显示', async () => {
    const wrapper = mount(CopyField, { props: { value: VALUE } })
    await buttonByLabel(wrapper, '复制')!.trigger('click')
    await wrapper.setProps({ value: 'new-private-value' })
    expect(buttonByLabel(wrapper, '已复制')).toBeUndefined()
    expect(inputValue(wrapper)).not.toBe('new-private-value')
    wrapper.unmount()
  })

  it('warning 显示为次要文字，自定义 label 走 slot', () => {
    const wrapper = mount(CopyField, {
      props: { value: VALUE, warning: '此链接等同于密码' },
      slots: { label: '<span>我的标签</span>' },
    })
    expect(wrapper.text()).toContain('此链接等同于密码')
    expect(wrapper.text()).toContain('我的标签')
    wrapper.unmount()
  })
})
