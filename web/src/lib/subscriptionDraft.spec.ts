import { describe, expect, it } from 'vitest'
import type { SubscriptionRecord } from '@/api/types'
import { buildSubscriptionWrite, draftFromSubscription, emptySubscriptionDraft } from './subscriptionDraft'

function sub(overrides: Partial<SubscriptionRecord> = {}): SubscriptionRecord {
  return {
    id: 'sub_1',
    version: 4,
    name: '合租基础',
    description: null,
    node_group_ids: ['ng_1'],
    quota_bytes: null,
    timezone: 'Asia/Shanghai',
    anchor_day: 1,
    default_valid_days: 90,
    allow_new_bindings: true,
    access_enabled: true,
    archived: false,
    template_id: null,
    ...overrides,
  }
}

describe('订阅表单', () => {
  it('不限量映射为 quota_bytes=null', () => {
    const draft = emptySubscriptionDraft()
    draft.name = 'A'
    draft.unlimited = true
    const built = buildSubscriptionWrite(draft)
    expect(built.errors).toEqual([])
    expect(built.payload?.quota_bytes).toBeNull()
  })

  it('有限配额从 GiB 换算为字节', () => {
    const draft = emptySubscriptionDraft()
    draft.name = 'A'
    draft.quota_gib = '1.5'
    expect(buildSubscriptionWrite(draft).payload?.quota_bytes).toBe(1610612736)
  })

  it('周期起始日必须是 1–31', () => {
    const draft = emptySubscriptionDraft()
    draft.name = 'A'
    draft.anchor_day = '32'
    expect(buildSubscriptionWrite(draft).errors.some((error) => error.includes('1–31'))).toBe(true)
    draft.anchor_day = '0'
    expect(buildSubscriptionWrite(draft).errors.some((error) => error.includes('1–31'))).toBe(true)
  })

  it('无效时区被阻止', () => {
    const draft = emptySubscriptionDraft()
    draft.name = 'A'
    draft.timezone = 'Asia/NotAZone'
    expect(buildSubscriptionWrite(draft).errors.some((error) => error.includes('时区无效'))).toBe(true)
  })

  it('默认有效期留空提交 null，非法值被阻止', () => {
    const draft = emptySubscriptionDraft()
    draft.name = 'A'
    draft.default_valid_days = ''
    expect(buildSubscriptionWrite(draft).payload?.default_valid_days).toBeNull()
    draft.default_valid_days = '0'
    expect(buildSubscriptionWrite(draft).errors.some((error) => error.includes('默认有效期'))).toBe(true)
  })

  it('允许新绑定与允许访问是两个独立字段', () => {
    const draft = emptySubscriptionDraft()
    draft.name = 'A'
    draft.allow_new_bindings = false
    draft.access_enabled = true
    const built = buildSubscriptionWrite(draft)
    expect(built.payload?.allow_new_bindings).toBe(false)
    expect(built.payload?.access_enabled).toBe(true)
    expect(built.warnings.some((warning) => warning.includes('关闭新绑定'))).toBe(true)
  })

  it('暂停访问给出硬撤权警告', () => {
    const draft = emptySubscriptionDraft()
    draft.name = 'A'
    draft.access_enabled = false
    const built = buildSubscriptionWrite(draft)
    expect(built.warnings.some((warning) => warning.includes('硬撤权'))).toBe(true)
  })

  it('回填订阅值（不限量与有限配额）', () => {
    const unlimited = draftFromSubscription(sub())
    expect(unlimited.unlimited).toBe(true)
    expect(unlimited.quota_gib).toBe('')

    const limited = draftFromSubscription(sub({ quota_bytes: 10737418240 }))
    expect(limited.unlimited).toBe(false)
    expect(limited.quota_gib).toBe('10')
  })
})