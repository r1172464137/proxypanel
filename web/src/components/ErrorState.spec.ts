import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import ErrorState from './ErrorState.vue'
import { ApiError } from '@/api/http'

describe('ErrorState', () => {
  it('服务端错误消息按纯文本渲染，不执行 HTML', () => {
    const wrapper = mount(ErrorState, {
      props: {
        error: new ApiError(400, {
          code: 'VALIDATION_FAILED',
          message: '<img src=x onerror="window.__pwned = true">字段 name 非法',
          details: { field_errors: { name: 'required' } },
          request_id: 'req_1',
        }),
      },
    })
    expect(wrapper.html()).not.toContain('<img')
    expect(wrapper.text()).toContain('<img src=x')
    expect(wrapper.text()).toContain('字段 name 非法')
    expect(wrapper.text()).toContain('VALIDATION_FAILED')
    expect(wrapper.text()).toContain('req_1')
  })

  it('不可重试错误不显示重试按钮；可重试错误显示', async () => {
    const nonRetryable = mount(ErrorState, {
      props: { error: new ApiError(403, { code: 'FORBIDDEN', message: '无权限', retryable: false }) },
    })
    expect(nonRetryable.find('button').exists()).toBe(false)

    const retryable = mount(ErrorState, {
      props: { error: new ApiError(503, { code: 'DB_UNAVAILABLE', message: '数据库不可用', retryable: true }) },
    })
    const button = retryable.find('button')
    expect(button.exists()).toBe(true)
    await button.trigger('click')
    expect(retryable.emitted('retry')).toHaveLength(1)
  })

  it('展示中文错误提示（PREVIEW_REQUIRED 等）', () => {
    const wrapper = mount(ErrorState, {
      props: { error: new ApiError(409, { code: 'PREVIEW_REQUIRED', message: '需要预览' }) },
    })
    expect(wrapper.text()).toContain('必须先生成预览并确认')
  })
})