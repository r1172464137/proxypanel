import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import SetupChecklist from './SetupChecklist.vue'
function render(setup?: { steps: { key: string; state: string; reason?: string }[]; completed_count: number }) {
  return mount(SetupChecklist, { props: { setup }, global: { plugins: [createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] })] } })
}
const allKeys = ['servers', 'nodes', 'node_groups', 'subscriptions', 'members', 'traffic_observed']
describe('真实首次设置路径', () => {
  it('缺少服务端进度不伪装成零步或已完成', () => {
    const view = render()
    expect(view.text()).toContain('设置进度暂不可用')
    expect(view.findAll('li')).toHaveLength(6)
    expect(view.text()).not.toContain('已完成')
    expect(view.find('a[href="/admin/invitations"]').exists()).toBe(true)
  })
  it('阻断进度不伪装成 0/6，完整六个已知完成才收起为摘要', () => {
    const blocked = render({ steps: [{ key: 'servers', state: 'blocked', reason: '无法核对接入' }], completed_count: 0 })
    expect(blocked.get('[data-testid="setup-progress-blocked"]').text()).toContain('设置被阻断')
    // 0 is a legal aggregate here, so it is shown as the confirmed count — but
    // blocked/unknown steps are listed separately and the summary stays open.
    // (The previous assertion hid every count, which also hid real confirmed
    // totals such as 3/6; the API's completed_count is now shown when legal.)
    expect(blocked.get('[data-testid="setup-progress-label"]').text()).toBe('已确认完成 0/6 步')
    expect(blocked.text()).not.toContain('设置已完成')
    expect(blocked.find('ol').exists()).toBe(true)
    const done = render({ steps: allKeys.map(key => ({ key, state: 'completed' })), completed_count: 6 })
    expect(done.get('[data-testid="setup-progress-label"]').text()).toContain('6/6')
    expect(done.find('ol').exists()).toBe(false)
    expect(done.find('[data-testid="setup-progress-unknown"]').exists()).toBe(false)
    expect(done.find('a[href="/admin/setup"]').exists()).toBe(true)
  })
  it('已知 completed_count 与 unknown 步骤分开呈现，不隐藏已知数量也不当作待完成', () => {
    const setup = {
      steps: allKeys.map((key, index) => ({ key, state: index === 5 ? 'unknown' : 'completed' })),
      completed_count: 5,
    }
    const view = render(setup)
    expect(view.get('[data-testid="setup-progress-label"]').text()).toBe('已确认完成 5/6 步')
    expect(view.get('[data-testid="setup-progress-unknown"]').text()).toBe('1 步状态未知，未计入已完成')
    expect(view.text()).not.toContain('进度未知：等待完整核对结果')
    expect(view.findAll('li')).toHaveLength(6)
    const unknownCell = view.get('[data-testid="setup-step-traffic_observed"]')
    expect(unknownCell.text()).toContain('状态未知')
    expect(unknownCell.text()).not.toContain('待完成')
    expect(view.find('ol').exists()).toBe(true)
  })
  it('aggregate 缺失或不合法时才显示数量未知', () => {
    const cases = [undefined, -1, 7, 2.5, Number.NaN]
    for (const completed_count of cases) {
      const view = render({ steps: allKeys.map(key => ({ key, state: 'completed' })), completed_count: completed_count as number })
      expect(view.get('[data-testid="setup-progress-label"]').text()).toBe('完成数量未知：等待完整核对结果')
      expect(view.get('[data-testid="setup-progress-label"]').text()).not.toMatch(/\d\/6/)
      expect(view.find('ol').exists()).toBe(true)
    }
  })
  it('只使用服务端状态，并保留阻断原因和处理入口', () => {
    const view = render({ steps: [{ key: 'servers', state: 'completed' }, { key: 'nodes', state: 'blocked', reason: '管理程序未接入' }], completed_count: 1 })
    expect(view.findAll('li')[0]!.text()).toContain('已完成')
    expect(view.findAll('li')[1]!.text()).toContain('被阻断')
    expect(view.text()).toContain('管理程序未接入')
    expect(view.find('a[href="/admin/tasks"]').exists()).toBe(true)
  })
})
