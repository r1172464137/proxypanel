import { api } from '@/api/http'
import type { MeUsageResult, QuotaAdjustmentRecord, UsageItem } from '@/api/types'

function object(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null
}

/** Preserve the member endpoint's object-shaped items; malformed data is not an empty ledger. */
export function unpackMeUsage(payload: unknown): MeUsageResult {
  const envelope = object(payload)
  const legacy = Array.isArray(payload) ? payload : Array.isArray(envelope?.items) ? envelope.items : null
  if (legacy) {
    if (!legacy.every(object)) throw new Error('用量记录不完整，请联系管理员核对。')
    return { items: legacy as UsageItem[], adjustments: [], next_cursor: typeof envelope?.next_cursor === 'string' ? envelope.next_cursor : null }
  }
  const items = object(envelope?.items)
  if (!items || !Array.isArray(items.usage) || !Array.isArray(items.adjustments)) {
    throw new Error('用量资料暂时无法读取，请重试或联系为你开通服务的管理员。')
  }
  if (!items.usage.every(object) || !items.adjustments.every(object)) {
    throw new Error('用量记录不完整，请联系为你开通服务的管理员核对。')
  }
  return {
    items: items.usage as UsageItem[],
    adjustments: items.adjustments as QuotaAdjustmentRecord[],
    next_cursor: typeof envelope?.next_cursor === 'string' && envelope.next_cursor ? envelope.next_cursor : null,
  }
}

export async function loadMeUsage(cursor?: string | null, signal?: AbortSignal): Promise<MeUsageResult> {
  const base = (import.meta.env.VITE_API_BASE ?? '').replace(/\/+$/, '')
  return unpackMeUsage(await api.get<unknown>(`${base}/api/v1/me/usage`, { query: { cursor }, signal }))
}
