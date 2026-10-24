import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ErrorState from './ErrorState.vue'
import { ApiError } from '@/api/http'

function alertOf(wrapper: ReturnType<typeof mount>) {
  const alert = wrapper.find('[role="alert"]')
  expect(alert.exists()).toBe(true)
  return alert
}

describe('ErrorState', () => {
  it('渲染 role=alert，并把服务端消息按纯文本展示', () => {
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

    const alert = alertOf(wrapper)
    expect(alert.text()).toContain('<img src=x')
    expect(alert.text()).toContain('字段 name 非法')
    expect(alert.text()).toContain('VALIDATION_FAILED')
    expect(alert.text()).toContain('req_1')
    expect(wrapper.html()).not.toContain('<img')
  })

  it('展示中文错误提示与兜底标题', () => {
    const wrapper = mount(ErrorState, {
      props: {
        error: new ApiError(409, { code: 'PREVIEW_REQUIRED', message: '需要预览' }),
      },
    })

    const alert = alertOf(wrapper)
    expect(alert.text()).toContain('需要预览')
    expect(alert.text()).toContain('必须先生成预览并确认')
  })

  it('默认显示“重试”按钮，点击后 emit retry', async () => {
    const onRetry = vi.fn()
    const wrapper = mount(ErrorState, {
      props: { error: new ApiError(503, { code: 'DB_UNAVAILABLE', message: '数据库不可用' }), onRetry },
    })

    const button = wrapper.get('button')
    expect(button.text()).toContain('重试')
    await button.trigger('click')
    // Verify delivery to the parent, independent of devtools-based event recording.
    expect(onRetry).toHaveBeenCalledTimes(1)
    expect(onRetry).toHaveBeenCalledWith()
  })

  it('showRetry=false 时不显示重试按钮', () => {
    const wrapper = mount(ErrorState, {
      props: {
        error: new ApiError(403, { code: 'FORBIDDEN', message: '无权限' }),
        showRetry: false,
      },
    })

    alertOf(wrapper)
    expect(wrapper.findAll('button')).toHaveLength(0)
  })

  it('retryLabel 可自定义', () => {
    const wrapper = mount(ErrorState, {
      props: {
        error: new ApiError(503, { code: 'DB_UNAVAILABLE', message: '数据库不可用' }),
        retryLabel: '重新加载',
      },
    })

    expect(wrapper.get('button').text()).toContain('重新加载')
  })
})
