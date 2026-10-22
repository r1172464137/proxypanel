import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import JsonEditor from './JsonEditor.vue'

describe('JsonEditor', () => {
  it('合法 JSON 时 emit update:modelValue', async () => {
    const onModelValue = vi.fn()
    const onError = vi.fn()
    const wrapper = mount(JsonEditor, {
      props: { modelValue: {}, label: '节点配置' },
      attrs: { 'onUpdate:modelValue': onModelValue, 'onUpdate:error': onError },
    })

    await wrapper.find('textarea').setValue('{"protocol":"vless","port":443}')
    await nextTick()

    expect(onModelValue).toHaveBeenCalledWith({ protocol: 'vless', port: 443 })
    expect(onError).toHaveBeenLastCalledWith('')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('非法 JSON 时 emit update:error 并显示错误提示', async () => {
    const onModelValue = vi.fn()
    const onError = vi.fn()
    const wrapper = mount(JsonEditor, {
      props: { modelValue: {}, label: '节点配置' },
      attrs: { 'onUpdate:modelValue': onModelValue, 'onUpdate:error': onError },
    })

    await wrapper.find('textarea').setValue('{"protocol":')
    await nextTick()

    expect(onModelValue).not.toHaveBeenCalled()
    expect(onError).toHaveBeenCalledTimes(1)
    const [message] = onError.mock.calls[0] as [string]
    expect(message).toContain('节点配置')
    const alert = wrapper.find('[role="alert"]')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toBe(message)
    expect(wrapper.find('textarea').attributes('aria-invalid')).toBe('true')
    wrapper.unmount()
  })

  it('非对象 JSON 也被拒绝', async () => {
    const onError = vi.fn()
    const wrapper = mount(JsonEditor, {
      props: { modelValue: {}, label: '节点配置' },
      attrs: { 'onUpdate:error': onError },
    })

    await wrapper.find('textarea').setValue('[1,2,3]')
    await nextTick()
    expect(onError).toHaveBeenCalledTimes(1)
    expect((onError.mock.calls[0] as [string])[0]).toContain('必须是 JSON 对象')
    wrapper.unmount()
  })

  it('label 与 hint 走 Label 与次要说明文字', () => {
    const wrapper = mount(JsonEditor, {
      props: { modelValue: { a: 1 }, label: '附加参数', hint: '键会原样提交' },
    })
    expect(wrapper.find('label').text()).toBe('附加参数')
    expect(wrapper.text()).toContain('键会原样提交')
    expect(wrapper.find('textarea').attributes('spellcheck')).toBe('false')
    wrapper.unmount()
  })
})
