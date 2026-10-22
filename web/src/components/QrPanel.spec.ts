import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

// jsdom 没有 canvas，qrcode 的真实实现会失败；这里只验证组件接线。
vi.mock('qrcode', () => ({
  default: {
    toDataURL: vi.fn(async (value: string) => `data:image/png;base64,${value}`),
  },
}))

import QRCode from 'qrcode'
import QrPanel from './QrPanel.vue'

afterEach(() => {
  vi.clearAllMocks()
})

describe('QrPanel', () => {
  it('按 value 渲染二维码图片、无障碍名称与默认说明', async () => {
    const wrapper = mount(QrPanel, { props: { value: 'https://panel.test/sub/tok' } })
    await flushPromises()

    expect(QRCode.toDataURL).toHaveBeenCalledWith('https://panel.test/sub/tok', expect.objectContaining({ width: 240 }))
    const img = wrapper.find('img')
    expect(img.exists()).toBe(true)
    expect(img.attributes('src')).toBe('data:image/png;base64,https://panel.test/sub/tok')
    expect(img.attributes('alt')).toBe('订阅二维码')
    expect(wrapper.find('figure').attributes('aria-label')).toBe('订阅二维码')
    expect(wrapper.text()).toContain('二维码包含完整订阅链接')
    wrapper.unmount()
  })

  it('空值时不渲染图片，给出提示', async () => {
    const wrapper = mount(QrPanel, { props: { value: '' } })
    await flushPromises()
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.text()).toContain('没有可生成二维码的内容')
    wrapper.unmount()
  })

  it('生成失败时渲染可读错误', async () => {
    vi.mocked(QRCode.toDataURL).mockRejectedValueOnce(new Error('boom'))
    const wrapper = mount(QrPanel, { props: { value: 'x' } })
    await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toContain('二维码生成失败')
    wrapper.unmount()
  })

  it('自定义 caption 覆盖默认说明', async () => {
    const wrapper = mount(QrPanel, { props: { value: 'x', caption: '自定义说明' } })
    await flushPromises()
    expect(wrapper.text()).toContain('自定义说明')
    expect(wrapper.find('figure').attributes('aria-label')).toBe('自定义说明')
    wrapper.unmount()
  })
})
