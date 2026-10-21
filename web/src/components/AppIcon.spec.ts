import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import AppIcon from './AppIcon.vue'
import { ICON_NAMES, ICON_PATHS } from '@/lib/icons'

describe('AppIcon', () => {
  it('每个图标都遵守统一约定：24 viewBox、1.6 描边、currentColor、圆头圆角', () => {
    for (const name of ICON_NAMES) {
      const wrapper = mount(AppIcon, { props: { name } })
      const svg = wrapper.find('svg')
      expect(svg.attributes('viewBox'), name).toBe('0 0 24 24')
      expect(svg.attributes('stroke'), name).toBe('currentColor')
      expect(svg.attributes('stroke-width'), name).toBe('1.6')
      expect(svg.attributes('stroke-linecap'), name).toBe('round')
      expect(svg.attributes('stroke-linejoin'), name).toBe('round')
      expect(svg.attributes('fill'), name).toBe('none')

      const paths = ICON_PATHS[name]
      expect(paths.length, name).toBeGreaterThan(0)
      expect(wrapper.findAll('path')).toHaveLength(paths.length)
      for (const d of paths) {
        expect(d.trim().length, name).toBeGreaterThan(0)
      }
    }
  })

  it('纯装饰图标对辅助技术隐藏', () => {
    const wrapper = mount(AppIcon, { props: { name: 'menu' } })
    expect(wrapper.attributes('aria-hidden')).toBe('true')
    expect(wrapper.attributes('role')).toBeUndefined()
    expect(wrapper.attributes('aria-label')).toBeUndefined()
  })

  it('传 label 时暴露 img 语义，便于仅图标按钮使用', () => {
    const wrapper = mount(AppIcon, { props: { name: 'menu', label: '打开导航' } })
    expect(wrapper.attributes('role')).toBe('img')
    expect(wrapper.attributes('aria-label')).toBe('打开导航')
    expect(wrapper.attributes('aria-hidden')).toBeUndefined()
  })

  it('尺寸与描边可覆盖', () => {
    const wrapper = mount(AppIcon, { props: { name: 'refresh', size: 14, strokeWidth: 1.75 } })
    expect(wrapper.attributes('width')).toBe('14')
    expect(wrapper.attributes('height')).toBe('14')
    expect(wrapper.attributes('stroke-width')).toBe('1.75')
  })
})
