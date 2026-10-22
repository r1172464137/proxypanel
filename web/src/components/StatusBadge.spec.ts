import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import StatusBadge from './StatusBadge.vue'
import { Badge } from '@/components/ui/badge'

function badgeOf(wrapper: ReturnType<typeof mount>) {
  const badge = wrapper.findComponent(Badge)
  expect(badge.exists()).toBe(true)
  return badge
}

describe('StatusBadge', () => {
  it('区分失败与已生效的文案', () => {
    const failed = mount(StatusBadge, { props: { status: 'failed' } })
    const applied = mount(StatusBadge, { props: { status: 'applied' } })

    expect(failed.text()).toContain('失败')
    expect(applied.text()).toContain('已生效')
    expect(failed.text()).not.toBe(applied.text())
  })

  it('布尔值映射为是/否，并分别使用 ok / danger 色调', () => {
    const yes = mount(StatusBadge, { props: { status: true } })
    const no = mount(StatusBadge, { props: { status: false } })

    expect(yes.text()).toContain('是')
    expect(no.text()).toContain('否')
    expect(badgeOf(yes).props('variant')).toBe('outline')
    expect(badgeOf(no).props('variant')).toBe('destructive')
  })

  it('tone=danger 覆盖默认色调并渲染危险语义', () => {
    const wrapper = mount(StatusBadge, { props: { status: 'enabled', tone: 'danger' } })

    expect(wrapper.text()).toContain('已启用')
    expect(badgeOf(wrapper).props('variant')).toBe('destructive')
  })

  it('tone=accent 使用品牌强调色变体', () => {
    const wrapper = mount(StatusBadge, { props: { status: 'used', tone: 'accent' } })

    expect(wrapper.text()).toContain('已使用')
    expect(badgeOf(wrapper).props('variant')).toBe('outline')
  })

  it('label 覆盖默认文案，未知状态回落为 muted', () => {
    const labelled = mount(StatusBadge, { props: { status: 'failed', label: '同步失败' } })
    const unknown = mount(StatusBadge, { props: { status: null } })

    expect(labelled.text()).toContain('同步失败')
    expect(unknown.text()).toContain('未知')
    expect(badgeOf(unknown).props('variant')).toBe('secondary')
  })
})
