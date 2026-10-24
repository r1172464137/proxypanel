import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import SetupChecklist from './SetupChecklist.vue'
function render(setup?: { steps: { key: string; state: string; reason?: string }[]; completed_count: number }) {
  return mount(SetupChecklist, { props: { setup }, global: { plugins: [createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] })] } })
}
describe('真实首次设置路径', () => {
  it('缺少服务端进度不伪装成零步或已完成', () => {
    const view = render()
    expect(view.text()).toContain('设置进度暂不可用')
    expect(view.findAll('li')).toHaveLength(6)
    expect(view.text()).not.toContain('已完成')
    expect(view.find('a[href="/admin/invitations"]').exists()).toBe(true)
  })
  it('阻断进度不伪装成 0/6，完整交付收起为摘要', () => {
    const blocked = render({ steps: [{ key: 'servers', state: 'blocked', reason: '无法核对接入' }], completed_count: 0 })
    expect(blocked.text()).toContain('设置被阻断')
    expect(blocked.text()).not.toContain('0/6')
    const done = render({ steps: ['servers', 'nodes', 'node_groups', 'subscriptions', 'members', 'traffic_observed'].map(key => ({ key, state: 'completed' })), completed_count: 6 })
    expect(done.text()).toContain('6/6')
    expect(done.find('ol').exists()).toBe(false)
    expect(done.find('a[href="/admin/setup"]').exists()).toBe(true)
  })
  it('只使用服务端状态，并保留阻断原因和处理入口', () => {
    const view = render({ steps: [{ key: 'servers', state: 'completed' }, { key: 'nodes', state: 'blocked', reason: '管理程序未接入' }], completed_count: 1 })
    expect(view.findAll('li')[0]!.text()).toContain('已完成')
    expect(view.findAll('li')[1]!.text()).toContain('被阻断')
    expect(view.text()).toContain('管理程序未接入')
    expect(view.find('a[href="/admin/tasks"]').exists()).toBe(true)
  })
})
