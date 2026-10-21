import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { adminApi, authApi, meApi, memberRecordFromDetail } from './endpoints'
import { setCsrfToken } from './http'

interface Captured {
  url: string
  init: RequestInit
}

function stubFetch(body: unknown, status = 200): Captured[] {
  const calls: Captured[] = []
  vi.stubGlobal('fetch', async (url: string, init: RequestInit) => {
    calls.push({ url, init })
    return {
      ok: status >= 200 && status < 300,
      status,
      text: async () => JSON.stringify(body),
    } as unknown as Response
  })
  return calls
}

beforeEach(() => setCsrfToken('csrf-1'))
afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

describe('端点封装', () => {
  it('me/usage 归一化服务端 {usage, adjustments} 信封', async () => {
    stubFetch({
      items: [
        {
          usage: [
            { member_id: 'm1', node_id: 'n1', period_start: '2026-10-01T00:00:00Z', period_end: '2026-11-01T00:00:00Z', up_bytes: 10, down_bytes: 20, estimated: true },
          ],
          adjustments: [{ id: 'adj1', bytes: 100, reason: '补量' }],
        },
      ],
      next_cursor: null,
    })
    const result = await meApi.usage()
    expect(result.items).toHaveLength(1)
    expect(result.items[0]!.up_bytes).toBe(10)
    expect(result.items[0]!.period_start).toBe('2026-10-01T00:00:00Z')
    expect(result.adjustments[0]!.reason).toBe('补量')
    expect(result.next_cursor).toBeNull()
  })

  it('恢复模式退出必须带 confirm=verified 与 CSRF', async () => {
    const calls = stubFetch({ data: { recovery_mode: false } })
    await adminApi.recoveryMode.exit({ agent_revisions_checked: true, ledger_checked: true })
    expect(calls[0]!.url).toBe('/api/v1/admin/recovery-mode/exit')
    expect(calls[0]!.init.method).toBe('POST')
    expect(JSON.parse(String(calls[0]!.init.body))).toEqual({ confirm: 'verified', agent_revisions_checked: true, ledger_checked: true })
    const headers = calls[0]!.init.headers as Record<string, string>
    expect(headers['X-CSRF-Token']).toBe('csrf-1')
  })

  it('settings PUT 携带 version 与 template_defaults', async () => {
    const calls = stubFetch({ data: { version: 5 } })
    await adminApi.settings.update({ version: 4, timezone: 'Asia/Shanghai', template_defaults: { xray: { id: 'tpl' } } })
    expect(calls[0]!.init.method).toBe('PUT')
    const body = JSON.parse(String(calls[0]!.init.body)) as Record<string, unknown>
    expect(body.version).toBe(4)
    expect(body.timezone).toBe('Asia/Shanghai')
    expect(body.template_defaults).toEqual({ xray: { id: 'tpl' } })
  })

  it('批量预览向冻结路径提交显式成员集合', async () => {
    const calls = stubFetch({ data: { id: 'prv_1', version: 2, affected_count: 1, affected_nodes: 1, exceptions: [], warnings: [], expires_at: '2026-10-20T06:20:00Z' } })
    const preview = await adminApi.batches.preview({ operation: 'assign_subscription', member_ids: ['m1'], values: { subscription_id: 'sub_1' } })
    expect(calls[0]!.url).toBe('/api/v1/admin/batches/preview')
    expect(JSON.parse(String(calls[0]!.init.body))).toEqual({
      operation: 'assign_subscription',
      member_ids: ['m1'],
      values: { subscription_id: 'sub_1' },
    })
    expect(preview.id).toBe('prv_1')
  })

  it('登录是公共端点：不带 CSRF，且不触发全局登出', async () => {
    const calls = stubFetch({ data: { user: { id: 'm1', username: 'a', role: 'member' }, csrf_token: 'new' } })
    const payload = await authApi.login('a', 'password-123')
    const headers = calls[0]!.init.headers as Record<string, string>
    expect(headers['X-CSRF-Token']).toBeUndefined()
    expect(calls[0]!.url).toBe('/api/v1/auth/login')
    expect(payload.csrf_token).toBe('new')
  })

  // 真实服务端形状：GET /api/v1/admin/members/{id} 返回 {"data":{"member":…,"access":…}}
  // （internal/panel/api/admin2.go handleGetMember），列表端点则是 decorateMember 派生后的
  // 扁平记录。详情必须解包并按同一派生规则合并，否则详情页拿不到 username/version/订阅。
  const memberDetailEnvelope = () => ({
    data: {
      member: {
        id: 'mem_01',
        username: 'alice',
        role: 'member',
        active: true,
        proxy_enabled: true,
        archived: false,
        subscription_id: 'sub_01',
        expires_at: '2026-12-01T00:00:00Z',
        quota_override_bytes: null,
        quota_override_enabled: false,
        over_quota: false,
        bound_at: '2026-10-01T00:00:00Z',
        period_start: '2026-10-01T00:00:00Z',
        period_end: '2026-11-01T00:00:00Z',
        period_timezone: 'Asia/Shanghai',
        period_anchor_day: 1,
        last_login_at: '2026-10-21T09:00:00Z',
        version: 7,
        created_at: '2026-10-01T00:00:00Z',
        updated_at: '2026-10-21T09:00:00Z',
        // 存储记录里派生的零值：必须由 access 覆盖。
        used_bytes: 0,
        effective_quota_bytes: null,
        access_reasons: null,
        proxy_access: '',
        allowed_node_ids: null,
      },
      access: {
        algorithm_version: 'auth-v1',
        member_id: 'mem_01',
        access: true,
        proxy_access: 'enabled',
        access_reasons: [] as string[],
        access_chain: [{ step: 'account_active', result: 'pass' }],
        effective_quota_bytes: 1073741824,
        quota_source: 'subscription',
        used_bytes: 1234,
        expires_at: '2026-12-01T00:00:00Z',
        allowed_node_ids: ['node_01'],
        node_reasons: {},
      },
    },
  })

  it('成员详情 {member, access} 解包为扁平 MemberRecord（订阅/版本/派生值）', async () => {
    const calls = stubFetch(memberDetailEnvelope())
    const member = await adminApi.members.get('mem_01')
    expect(calls[0]!.url).toBe('/api/v1/admin/members/mem_01')
    expect(calls[0]!.init.method).toBe('GET')
    expect(member.id).toBe('mem_01')
    expect(member.username).toBe('alice')
    expect(member.role).toBe('member')
    expect(member.active).toBe(true)
    expect(member.subscription_id).toBe('sub_01')
    // version 必须存在，详情页的 PATCH 依赖它。
    expect(member.version).toBe(7)
    // 派生字段以 access 为准（与列表 decorateMember 一致）。
    expect(member.used_bytes).toBe(1234)
    expect(member.effective_quota_bytes).toBe(1073741824)
    expect(member.proxy_access).toBe('enabled')
    expect(member.over_quota).toBe(false)
    expect(member.access_reasons).toEqual([])
    expect(member.allowed_node_ids).toEqual(['node_01'])
    expect(member.quota_source).toBe('subscription')
    // 周期字段保留存储记录值。
    expect(member.period_start).toBe('2026-10-01T00:00:00Z')
    expect(member.period_timezone).toBe('Asia/Shanghai')
    expect((member.access as { algorithm_version?: string }).algorithm_version).toBe('auth-v1')
  })

  it('成员详情 over_quota 与 proxy_access 只由判定结果得出', async () => {
    const envelope = memberDetailEnvelope()
    envelope.data.access.proxy_access = 'over_quota'
    envelope.data.access.access_reasons = ['over_quota']
    stubFetch(envelope)
    const member = await adminApi.members.get('mem_01')
    expect(member.proxy_access).toBe('over_quota')
    expect(member.over_quota).toBe(true)
    expect(member.access_reasons).toEqual(['over_quota'])
    // 额度判定值以 access 为准（存储记录里的 null 不能被当作“不限量”）。
    expect(member.effective_quota_bytes).toBe(1073741824)
  })

  it('成员详情兼容服务端改为扁平返回', async () => {
    stubFetch({ data: { id: 'mem_02', username: 'bob', role: 'admin', active: true, proxy_enabled: false, version: 3, subscription_id: null } })
    const member = await adminApi.members.get('mem_02')
    expect(member.username).toBe('bob')
    expect(member.version).toBe(3)
    expect(member.subscription_id).toBeNull()
    // 没有 access 时不做派生，保留服务端返回值。
    expect(member.proxy_access).toBeUndefined()
    expect(member.effective_quota_bytes).toBeUndefined()
  })

  it('memberRecordFromDetail 直接处理空/异常载荷不抛错', () => {
    expect(memberRecordFromDetail(null).id).toBeUndefined()
    expect(memberRecordFromDetail({ member: { id: 'm', version: 1 }, access: null }).version).toBe(1)
    expect(memberRecordFromDetail('oops').id).toBeUndefined()
  })

})