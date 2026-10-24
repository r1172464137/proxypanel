import type { MeStatus, MeSubscription, SubscriptionFormat } from '@/api/types'
import { accessReasonDetails } from '@/lib/accessReasons'

export function memberSummary(data: MeStatus | null | undefined) {
  const access = data?.access
  const member = data?.member
  // Explicit null means unlimited; it must not fall through to another source.
  const total = access?.effective_quota_bytes !== undefined ? access.effective_quota_bytes : member?.effective_quota_bytes
  const used = access?.used_bytes !== undefined ? access.used_bytes : member?.used_bytes
  const codes = access?.access_reasons ?? member?.access_reasons ?? []
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
    return { label: '服务暂不可用', action: '请联系为你开通服务的管理员核对，诊断详情可在服务状态页查看。' }
  })
  return {
    total, used, unlimited: total === null,
    remaining: typeof total === 'number' && typeof used === 'number' ? Math.max(0, total - used) : undefined,
    title: access?.access === false ? (labels[codes[0] ?? ''] ?? '服务暂不可用')
      : data?.data_complete === false || member?.data_complete === false ? '部分服务资料需核对'
      : access?.access === true ? '服务可用' : '服务状态尚未确认',
    reasons,
    incomplete: data?.data_complete === false || member?.data_complete === false,
    start: member?.period_start, end: member?.period_end, timezone: member?.period_timezone,
    expires: access?.expires_at !== undefined ? access.expires_at : member?.expires_at,
  }
}

export const MEMBER_CLIENTS: { format: SubscriptionFormat; name: string; instruction: string }[] = [
  { format: 'v2rayn', name: 'v2rayN · Windows', instruction: '在「订阅分组设置」中添加订阅地址，保存后更新订阅。' },
  { format: 'mihomo', name: 'Clash Verge Rev · Windows / macOS / Linux', instruction: '在「订阅」中粘贴链接并导入，更新后选择该配置。需要支持 Mihomo 的客户端版本。' },
  { format: 'singbox', name: 'sing-box · 高级配置', instruction: '使用支持远程配置的 sing-box 客户端添加链接，或下载 JSON 后按客户端文档载入。版本兼容性需在客户端核对。' },
  { format: 'xray', name: 'Xray · 高级配置', instruction: '下载原生 JSON，按 Xray 文档载入；不支持直接添加订阅的客户端请勿粘贴该链接。' },
]

export function formatAvailability(data: MeSubscription | null, format: SubscriptionFormat) {
  if (!data?.formats?.includes(format)) return { available: false, message: '当前服务未提供此格式，请选择其他客户端。' }
  const entry = data.format_status?.find(item => item.format === format)
  if (!entry) return { available: undefined, message: '格式兼容性尚未确认。请先检查能否获取配置，再复制或下载。' }
  if (entry.available) return { available: true, message: '此格式可生成配置；仍需在客户端确认版本兼容性与实际连接。' }
  const message = entry.reason === 'nodes_empty' ? '此格式暂无可导入的节点。'
    : entry.reason.startsWith('protocol_unsupported') ? '此格式无法表达当前节点的部分参数。'
    : entry.reason.startsWith('node_incomplete') ? '节点配置资料不完整。' : '此格式暂不可用。'
  return { available: false, message: `${message}请选择其他客户端，或联系为你开通服务的管理员。` }
}

export function memberSubscriptionUrl(data: MeSubscription | null, format: SubscriptionFormat, origin: string): string {
  if (!data?.url) return ''
  try {
    const base = (import.meta.env.VITE_API_BASE ?? '').replace(/\/+$/, '') || origin
    const url = new URL(data.url.replace('{token}', encodeURIComponent(data.token)), base)
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) return ''
    url.searchParams.set('format', format)
    return url.toString()
  } catch { return '' }
}
