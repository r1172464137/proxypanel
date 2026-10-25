import type { MeSubscription, SubscriptionFormat } from '@/api/types'

/**
 * Clients offered in the member import UI.
 *
 * Only Xray and Clash (Mihomo) are surfaced: they cover the two shapes members
 * actually use (native JSON and Clash YAML), and keeping the list short avoids
 * offering formats nobody asked for. The server still renders v2rayN and
 * sing-box for /sub?format=..., so existing links keep working — this list only
 * controls what the UI advertises.
 */
export const MEMBER_CLIENTS: { format: SubscriptionFormat; name: string; instruction: string }[] = [
  { format: 'mihomo', name: 'Clash Verge Rev · Windows / macOS / Linux', instruction: '在「订阅」中粘贴链接并导入，更新后选择该配置。需要支持 Mihomo 的客户端版本。' },
  { format: 'xray', name: 'Xray · 高级配置', instruction: '下载原生 JSON，按 Xray 文档载入；不支持直接添加订阅的客户端请勿粘贴该链接。' },
]

export function formatAvailability(data: MeSubscription | null, format: SubscriptionFormat) {
  if (!data?.formats?.includes(format)) return { available: false, message: '当前服务未提供此格式，请选择其他客户端。' }
  const entry = data.format_status?.find(item => item.format === format)
  if (!entry) return { available: false, message: '格式兼容性尚未确认，请刷新导入资料或联系管理员。' }
  if (entry.available === true) return { available: true, message: '此格式可生成配置；仍需在客户端确认版本兼容性与实际连接。' }
  const reason = entry.reason ?? ''
  const message = reason === 'nodes_empty' ? '此格式暂无可导入的节点。'
    : reason.startsWith('protocol_unsupported') ? '此格式无法表达当前节点的部分参数。'
    : reason.startsWith('node_incomplete') ? '节点配置资料不完整。' : '此格式暂不可用，请联系管理员核对未知检查项。'
  return { available: false, message: `${message}请选择其他客户端，或联系为你开通服务的管理员。` }
}

/** No optimistic link: only the server's affirmative format verdict can expose a credential. */
export function memberSubscriptionUrl(data: MeSubscription | null, format: SubscriptionFormat, origin: string): string {
  if (!data?.url || !formatAvailability(data, format).available) return ''
  try {
    const base = (import.meta.env.VITE_API_BASE ?? '').replace(/\/+$/, '') || origin
    const url = new URL(data.url.replace('{token}', encodeURIComponent(data.token)), base)
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) return ''
    url.searchParams.set('format', format)
    return url.toString()
  } catch { return '' }
}

export function credentialRotationRequest(scope: 'single_node' | 'all_nodes', nodeId: string, allConfirmed: boolean) {
  if (scope === 'all_nodes') {
    if (!allConfirmed) throw new Error('请明确确认全部节点的影响')
    return { scope } as const
  }
  if (!nodeId.trim()) throw new Error('请选择节点')
  return { scope, node_id: nodeId } as const
}

export function safeLoginNext(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.startsWith('/') && !value.startsWith('//') && !/[\\\u0000-\u001f]/.test(value) ? value : fallback
}
