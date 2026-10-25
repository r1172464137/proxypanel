import { describe, expect, it } from 'vitest'
import type { MeSubscription } from '@/api/types'
import { MEMBER_CLIENTS, credentialRotationRequest, formatAvailability, memberSubscriptionUrl, safeLoginNext } from './clientImport'
const fixture = (reason: string, available = false): MeSubscription => ({ url: '/sub/{token}', token: 'synthetic', formats: ['v2rayn'], format_status: [{ format: 'v2rayn', reason, available }] })
describe('真实格式状态边界', () => {
  it.each([['nodes_empty', '暂无可导入'], ['protocol_unsupported:flow', '无法表达'], ['node_incomplete:host', '不完整'], ['future_reason', '未知检查项']])('不可用 %s 不产链接', (reason, message) => {
    const data = fixture(reason)
    expect(formatAvailability(data, 'v2rayn').message).toContain(message)
    expect(memberSubscriptionUrl(data, 'v2rayn', 'https://panel.test')).toBe('')
  })
  it('缺状态不乐观放行，formats 列表不能代替 format_status', () => {
    const data = fixture('ok', true)
    delete data.format_status
    expect(memberSubscriptionUrl(data, 'v2rayn', 'https://panel.test')).toBe('')
  })
  it('只允许 http(s)，不构造客户端唤起 URI，也不接受内嵌账号', () => {
    expect(new URL(memberSubscriptionUrl(fixture('ok', true), 'v2rayn', 'https://panel.test')).searchParams.get('format')).toBe('v2rayn')
    for (const url of ['javascript:alert(1)', 'https://user:password@panel.test/sub/a']) {
      expect(memberSubscriptionUrl({ ...fixture('ok', true), url }, 'v2rayn', 'https://panel.test')).toBe('')
    }
  })
})
describe('重置请求范围', () => {
  it('节点必选，全量必须额外确认，且不带遗留 node_id', () => {
    expect(() => credentialRotationRequest('single_node', '', false)).toThrow('请选择节点')
    expect(credentialRotationRequest('single_node', 'node-a', false)).toEqual({ scope: 'single_node', node_id: 'node-a' })
    expect(() => credentialRotationRequest('all_nodes', 'node-a', false)).toThrow('确认')
    expect(credentialRotationRequest('all_nodes', 'node-a', true)).toEqual({ scope: 'all_nodes' })
  })
})
describe('登录 next', () => {
  it.each(['//outside.test', '/\\outside.test', '/\nnext', 'https://outside.test'])('拒绝非站内目标', value => expect(safeLoginNext(value, '/me/subscription')).toBe('/me/subscription'))
  it('保留站内路径、查询与锚点', () => expect(safeLoginNext('/me/usage?page=2#detail', '/')).toBe('/me/usage?page=2#detail'))
})

// The member UI advertises only the formats members actually use. The server
// keeps rendering the others, so hiding them here must not remove API support.
it('只向成员暴露 Xray 与 Mihomo，其余格式仍由服务端支持', () => {
  expect(MEMBER_CLIENTS.map(item => item.format)).toEqual(['mihomo', 'xray'])
  // Hiding is a UI decision only: the unlisted formats must still resolve to a
  // working URL when the server reports them as available.
  for (const format of ['v2rayn', 'singbox'] as const) {
    const data: MeSubscription = { url: '/sub/{token}', token: 'synthetic', formats: [format], format_status: [{ format, reason: 'ok', available: true }] }
    expect(memberSubscriptionUrl(data, format, 'https://panel.test')).toContain(`format=${format}`)
  }
})
