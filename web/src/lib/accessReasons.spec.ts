import { describe, expect, it } from 'vitest'
import { accessReasonDetails, accessReasonSummary, describeAccessReason, describeQuotaSource } from './accessReasons'

// 这些测试锁住"用户能看懂"这件事。
//
// 之前有 5 处各自展示访问原因，其中 4 处直接把后端原因码打给用户看
// （"原因：no_binding"）。映射统一到这里之后，任何人再往视图里塞原始码，
// 这些断言就会失败。

const CODES = [
  'no_binding',
  'binding_expired',
  'subscription_access_paused',
  'subscription_archived',
  'proxy_paused',
  'account_suspended',
  'account_archived',
  'over_quota',
  'no_nodes_authorized',
  'node_disabled',
  'node_not_authorized',
  'expired',
]

describe('访问原因映射', () => {
  it('每个已知原因码都有中文说明与下一步建议', () => {
    for (const code of CODES) {
      const text = describeAccessReason(code)
      expect(text.label, `${code} 缺少中文说明`).toBeTruthy()
      expect(text.action, `${code} 缺少下一步建议`).toBeTruthy()
      // 展示给用户的内容里不能再出现原始码
      expect(text.label, `${code} 的文案里残留了原始码`).not.toBe(code)
      expect(text.label).not.toMatch(/^[a-z_]+$/)
    }
  })

  it('no_binding 翻译成用户能懂的话', () => {
    const text = describeAccessReason('no_binding')
    expect(text.label).toBe('还没有绑定订阅')
    expect(text.action).toContain('管理员')
  })

  it('未识别的码会回显原码并提示反馈，而不是静默显示空白', () => {
    const text = describeAccessReason('some_future_code')
    expect(text.label).toContain('some_future_code')
    expect(text.action).toContain('管理员')
  })

  it('summary 用中文短语拼接，详情返回结构化条目', () => {
    expect(accessReasonSummary(['no_binding', 'over_quota'])).toBe(
      '还没有绑定订阅；已超出本期流量额度',
    )
    const details = accessReasonDetails(['no_binding'])
    expect(details).toHaveLength(1)
    expect(details[0]!.label).toBe('还没有绑定订阅')
  })

  it('空输入不产生任何文案（不显示空括号或逗号）', () => {
    expect(accessReasonSummary([])).toBe('')
    expect(accessReasonSummary(undefined)).toBe('')
    expect(accessReasonSummary(null)).toBe('')
    expect(accessReasonDetails(undefined)).toEqual([])
  })
})

describe('额度来源映射', () => {
  // 后端 quota_source 只有 subscription / override / unlimited 三种。
  // 之前 MeStatusView 直接打印原始值（"额度来源：subscription"），
  // MembersView 里还写了一套三元表达式。
  it('后端的三种来源都翻成中文', () => {
    expect(describeQuotaSource('subscription')).toBe('跟随订阅')
    expect(describeQuotaSource('override')).toBe('单独设置')
    expect(describeQuotaSource('unlimited')).toBe('不限量')
  })

  it('空值与未识别的值有合理降级', () => {
    expect(describeQuotaSource('')).toBe('未知')
    expect(describeQuotaSource(undefined)).toBe('未知')
    expect(describeQuotaSource(null)).toBe('未知')
    expect(describeQuotaSource('future_value')).toBe('future_value')
  })
})
