import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import AppIcon from './AppIcon.vue'
import { ICON_NAMES } from '@/lib/icons'

/** 每个 lucide 图标至少会渲染一个形状节点（path/line/circle/...）。 */
function shapeCount(root: Element): number {
  return root.querySelectorAll('path, line, circle, rect, ellipse, polyline, polygon').length
}

describe('AppIcon', () => {
  it('每个语义名都能渲染出 svg 图形', () => {
    for (const name of ICON_NAMES) {
      const wrapper = mount(AppIcon, { props: { name } })
      const svg = wrapper.find('svg')
      expect(svg.exists(), `${name} 未渲染 svg`).toBe(true)
      expect(svg.attributes('viewBox'), name).toBe('0 0 24 24')
      expect(svg.attributes('stroke'), name).toBe('currentColor')
      expect(svg.attributes('fill'), name).toBe('none')
      expect(shapeCount(svg.element), `${name} 没有图形节点`).toBeGreaterThan(0)
      wrapper.unmount()
    }
  })

  it('纯装饰图标对辅助技术隐藏', () => {
    const wrapper = mount(AppIcon, { props: { name: 'menu' } })
    const svg = wrapper.find('svg')
    expect(svg.attributes('aria-hidden')).toBe('true')
    expect(svg.attributes('role')).toBeUndefined()
    expect(svg.attributes('aria-label')).toBeUndefined()
    wrapper.unmount()
  })

  it('传 label 时暴露 img 语义，便于仅图标按钮使用', () => {
    const wrapper = mount(AppIcon, { props: { name: 'menu', label: '打开导航' } })
    const svg = wrapper.find('svg')
    expect(svg.attributes('role')).toBe('img')
    expect(svg.attributes('aria-label')).toBe('打开导航')
    expect(svg.attributes('aria-hidden')).toBeUndefined()
    wrapper.unmount()
  })

  it('尺寸与描边可覆盖', () => {
    const wrapper = mount(AppIcon, { props: { name: 'refresh', size: 14, strokeWidth: 1.75 } })
    const svg = wrapper.find('svg')
    expect(svg.attributes('width')).toBe('14')
    expect(svg.attributes('height')).toBe('14')
    expect(svg.attributes('stroke-width')).toBe('1.75')
    wrapper.unmount()
  })
})
