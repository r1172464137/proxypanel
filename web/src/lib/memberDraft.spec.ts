import { describe, expect, it } from 'vitest'
import type { MemberRecord } from '@/api/types'
import { buildMemberWrite, draftFromMember, emptyMemberDraft, quotaSource } from './memberDraft'

function member(overrides: Partial<MemberRecord> = {}): MemberRecord {
  return {
    id: 'mbr_1',
    version: 2,
    username: 'alice',
    role: 'member',
    active: true,
    proxy_enabled: true,
    subscription_id: 'sub_1',
    expires_at: '2026-12-01T00:00:00Z',
    quota_override_bytes: 5368709120,
    quota_override_enabled: true,
    used_bytes: 1024,
    effective_quota_bytes: 5368709120,
    period_start: '2026-10-15T16:00:00Z',
    period_end: '2026-11-15T16:00:00Z',
    access_reasons: [],
    ...overrides,
  }
}

describe('成员表单', () => {
  it('创建时密码必填且至少 10 位', () => {
    const draft = emptyMemberDraft()
    draft.username = 'alice'
    draft.password = 'short'
    expect(buildMemberWrite(draft, 'create').errors.some((error) => error.includes('10 位'))).toBe(true)
    draft.password = 'long-enough-password'
    expect(buildMemberWrite(draft, 'create').errors).toEqual([])
  })

  it('编辑时密码留空表示不修改（不提交 password 键）', () => {
    const draft = draftFromMember(member())
    const built = buildMemberWrite(draft, 'update')
    expect(built.errors).toEqual([])
    expect(built.payload).not.toBeNull()
    expect('password' in built.payload!).toBe(false)
  })

  it('关闭配额覆盖时提交 null，启用时必须填写 GiB', () => {
    const draft = draftFromMember(member())
    draft.quota_override_enabled = false
    expect(buildMemberWrite(draft, 'update').payload?.quota_override_bytes).toBeNull()

    draft.quota_override_enabled = true
    draft.quota_override_gib = ''
    expect(buildMemberWrite(draft, 'update').errors.some((error) => error.includes('配额覆盖'))).toBe(true)
  })

  it('用户名规则与角色校验', () => {
    const draft = emptyMemberDraft()
    draft.username = 'a'
    draft.password = 'long-enough-password'
    expect(buildMemberWrite(draft, 'create').errors.some((error) => error.includes('用户名'))).toBe(true)
    draft.username = 'ok-name'
    draft.role = 'member'
    expect(buildMemberWrite(draft, 'create').errors).toEqual([])
  })

  it('有效期由本地时间转 UTC ISO，空值提交 null', () => {
    const draft = draftFromMember(member())
    draft.expires_at_local = '2026-11-30T12:30'
    const built = buildMemberWrite(draft, 'update')
    expect(built.payload?.expires_at).toBe(new Date('2026-11-30T12:30').toISOString().replace('.000Z', 'Z'))

    draft.expires_at_local = ''
    expect(buildMemberWrite(draft, 'update').payload?.expires_at).toBeNull()
  })

  it('未绑定订阅时给出无代理权限提示', () => {
    const draft = emptyMemberDraft()
    draft.username = 'bob'
    draft.password = 'long-enough-password'
    draft.subscription_id = ''
    expect(buildMemberWrite(draft, 'create').warnings.some((warning) => warning.includes('没有代理权限'))).toBe(true)
  })

  it('额度来源判定', () => {
    expect(quotaSource(member())).toBe('override')
    expect(quotaSource(member({ quota_override_enabled: false }))).toBe('subscription')
    expect(quotaSource(member({ quota_override_enabled: false, effective_quota_bytes: null }))).toBe('unlimited')
    expect(quotaSource(member({ quota_override_enabled: false, effective_quota_bytes: undefined }))).toBe('unknown')
  })
})