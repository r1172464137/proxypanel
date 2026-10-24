import type { MeStatus } from '@/api/types'
import { accessReasonDetails } from '@/lib/accessReasons'

/** Display the authoritative verdict, never infer authorization from an absent/future state. */
export function memberAccessState(data: MeStatus | null | undefined): 'enabled' | 'denied' | 'unknown' {
  const access = data?.access
  const known = ['enabled', 'paused_by_admin', 'expired', 'over_quota', 'no_subscription', 'account_blocked']
  if (data?.data_complete === false || data?.member?.data_complete === false
    || (access?.proxy_access !== undefined && !known.includes(access.proxy_access))) return 'unknown'
  if (access?.access === true && (!access.proxy_access || access.proxy_access === 'enabled')) return 'enabled'
  if (access?.access === false && access.proxy_access !== 'enabled') return 'denied'
  return 'unknown'
}

export function memberSummary(data: MeStatus | null | undefined) {
  const access = data?.access
  const member = data?.member
  // Explicit null means unlimited; it must not fall through to another source.
  const total = access?.effective_quota_bytes !== undefined ? access.effective_quota_bytes : member?.effective_quota_bytes
  const used = access?.used_bytes !== undefined ? access.used_bytes : member?.used_bytes
  const state = memberAccessState(data)
  // `ok` is an affirmative marker, not a denial reason. Do not fall back to stale member reasons.
  const codes = (access ? access.access_reasons ?? [] : member?.access_reasons ?? []).filter(code => code !== 'ok')
  const labels: Record<string, string> = {
    no_binding: '尚未开通服务', binding_expired: '服务已到期', expired: '服务已到期',
    over_quota: '本周期额度已用完', proxy_paused: '服务已暂停',
    subscription_access_paused: '服务已暂停', subscription_archived: '服务方案已停用',
    account_suspended: '账号已停用', account_archived: '账号已归档',
    no_nodes_authorized: '暂无可用节点', node_disabled: '节点暂不可用',
  }
  const reasons = codes.map(code => {
    if (code === 'no_binding') return { label: '尚未分配服务方案', action: '请联系为你开通服务的管理员。' }
    if (code === 'over_quota') return { label: '本周期额度已用完', action: '请等待下次重置，或联系为你开通服务的管理员调整额度。' }
    if (code === 'binding_expired' || code === 'expired') return { label: '服务已到期', action: '请联系为你开通服务的管理员续期。' }
    if (labels[code]) return { label: labels[code]!, action: accessReasonDetails([code])[0]?.action ?? '请联系为你开通服务的管理员。' }
    return { label: state === 'denied' ? '服务暂不可用' : '服务资料需核对', action: '请联系为你开通服务的管理员核对，诊断详情可在服务状态页查看。' }
  })
  return {
    total, used, unlimited: total === null,
    remaining: typeof total === 'number' && typeof used === 'number' ? Math.max(0, total - used) : undefined,
    state,
    title: state === 'denied' ? (labels[codes[0] ?? ''] ?? '服务暂不可用')
      : state === 'enabled' ? '服务可用' : '服务状态尚未确认',
    reasons,
    incomplete: data?.data_complete === false || member?.data_complete === false,
    start: member?.period_start, end: member?.period_end, timezone: member?.period_timezone,
    expires: access?.expires_at !== undefined ? access.expires_at : member?.expires_at,
  }
}

// Compatibility for existing consumers; implementation has one owner.
export { MEMBER_CLIENTS, formatAvailability, memberSubscriptionUrl } from './clientImport'
