import { describe, expect, it } from 'vitest'
import type { MeStatus } from '@/api/types'
import { memberAccessState, memberSummary } from '@/lib/memberService'

describe('成员逐字段状态规则', () => {
  it('不完整不抹掉已知数值、零额度与时间，不把判定包装为成功', () => {
    const summary = memberSummary({ data_complete: false, access: { access: true, used_bytes: 0, effective_quota_bytes: 0, expires_at: null }, member: { period_end: '2026-12-01T00:00:00Z' } } as MeStatus)
    expect(summary).toMatchObject({ used: 0, total: 0, remaining: 0, unlimited: false, incomplete: true, state: 'unknown', expires: null, end: '2026-12-01T00:00:00Z' })
  })
  it('明确 null 为不限量，不退回成员默认额度；缺字段才未知', () => {
    expect(memberSummary({ access: { effective_quota_bytes: null }, member: { effective_quota_bytes: 100 } } as MeStatus).unlimited).toBe(true)
    expect(memberSummary({}).total).toBeUndefined()
    expect(memberSummary({}).unlimited).toBe(false)
    expect(memberSummary({}).remaining).toBeUndefined()
  })
  it.each([
    [{ access: { access: true, proxy_access: 'future_state' } }, 'unknown'],
    [{ access: { proxy_access: 'enabled' } }, 'unknown'],
    [{ access: { access: false, proxy_access: 'enabled' } }, 'unknown'],
    [{ access: { access: true, proxy_access: 'enabled', access_reasons: ['ok'] } }, 'enabled'],
    [{ access: { access: false, proxy_access: 'over_quota' } }, 'denied'],
  ])('只信任明确一致的访问判定', (data, expected) => expect(memberAccessState(data as MeStatus)).toBe(expected))
  it.each([
    ['no_binding', '尚未分配服务方案', '管理员'],
    ['over_quota', '本周期额度已用完', '重置'],
    ['binding_expired', '服务已到期', '续期'],
    ['proxy_paused', '服务已暂停', '管理员'],
  ])('不同原因与下一步：%s', (code, label, action) => {
    expect(memberSummary({ access: { access: false, access_reasons: [code] } }).reasons[0]).toMatchObject({ label, action: expect.stringContaining(action) })
  })
})
