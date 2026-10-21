/** 成员表单：创建/编辑/配额覆盖；创建必须给密码，编辑留空表示不修改。 */
import { bytesToGib, gibToBytes, fromLocalInput, toLocalInput } from './format'
import type { MemberRecord, MemberWrite, Role } from '@/api/types'

export interface MemberDraft {
  username: string
  password: string
  role: Role
  active: boolean
  proxy_enabled: boolean
  subscription_id: string
  expires_at_local: string
  quota_override_enabled: boolean
  quota_override_gib: string
}

export function emptyMemberDraft(): MemberDraft {
  return {
    username: '',
    password: '',
    role: 'member',
    active: true,
    proxy_enabled: true,
    subscription_id: '',
    expires_at_local: '',
    quota_override_enabled: false,
    quota_override_gib: '',
  }
}

export function draftFromMember(member: MemberRecord): MemberDraft {
  return {
    username: member.username ?? '',
    password: '',
    role: member.role ?? 'member',
    active: member.active !== false,
    proxy_enabled: member.proxy_enabled !== false,
    subscription_id: member.subscription_id ?? '',
    expires_at_local: toLocalInput(member.expires_at),
    quota_override_enabled: member.quota_override_enabled === true,
    quota_override_gib:
      member.quota_override_bytes === null || member.quota_override_bytes === undefined
        ? ''
        : bytesToGib(member.quota_override_bytes),
  }
}

const USERNAME_RE = /^[A-Za-z0-9._-]{3,64}$/

export interface MemberBuild {
  payload: MemberWrite | null
  errors: string[]
  warnings: string[]
}

export function buildMemberWrite(draft: MemberDraft, mode: 'create' | 'update'): MemberBuild {
  const errors: string[] = []
  const warnings: string[] = []

  if (!USERNAME_RE.test(draft.username.trim())) {
    errors.push('用户名需为 3–64 位字母、数字、点、下划线或连字符')
  }
  if (mode === 'create' && draft.password.length < 10) {
    errors.push('初始密码至少 10 位')
  }
  if (mode === 'update' && draft.password && draft.password.length < 10) {
    errors.push('新密码至少 10 位')
  }
  if (draft.role !== 'admin' && draft.role !== 'member') errors.push('角色只能是 admin 或 member')
  if (draft.role === 'admin' && !draft.active) warnings.push('停用管理员会撤销其会话；服务端会阻止移除最后一名可用管理员')

  let overrideBytes: number | null = null
  if (draft.quota_override_enabled) {
    overrideBytes = gibToBytes(draft.quota_override_gib)
    if (overrideBytes === null || overrideBytes <= 0) errors.push('配额覆盖必须填写大于 0 的 GiB 数值（关闭覆盖即恢复继承订阅默认值）')
  }

  if (!draft.subscription_id) warnings.push('未绑定订阅：成员可以登录查看状态，但没有代理权限')
  if (!draft.expires_at_local) warnings.push('未设置有效期：将采用订阅默认有效期（若有）')

  if (errors.length) return { payload: null, errors, warnings }

  const payload: MemberWrite = {
    username: draft.username.trim(),
    role: draft.role,
    active: draft.active,
    proxy_enabled: draft.proxy_enabled,
    subscription_id: draft.subscription_id || null,
    expires_at: fromLocalInput(draft.expires_at_local),
    quota_override_enabled: draft.quota_override_enabled,
    quota_override_bytes: draft.quota_override_enabled ? overrideBytes : null,
  }
  if (draft.password) payload.password = draft.password
  return { payload, errors: [], warnings }
}

/** 有效额度来源：用于列表“覆盖”徽标与详情展示。 */
export function quotaSource(member: MemberRecord): 'override' | 'subscription' | 'unlimited' | 'unknown' {
  if (member.quota_override_enabled && member.quota_override_bytes !== null) return 'override'
  if (member.effective_quota_bytes === null) return 'unlimited'
  if (member.effective_quota_bytes === undefined) return 'unknown'
  return 'subscription'
}