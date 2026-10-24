import type { BatchOperation } from '@/api/types'
import type { submitRequest } from './batch'

export interface PendingBatch {
  accountId: string
  operation: BatchOperation
  scope: string
  targetDigest: string
  targetCount: number
  key: string
  request: ReturnType<typeof submitRequest>
}

const prefix = 'proxypanel.batch-pending.v1:'
const operations = ['assign_subscription', 'set_active', 'set_expiry', 'clear_override', 'update_subscription', 'update_node_group']
const storageError = () => new Error('本地存储不可用或核对记录损坏，无法安全恢复。已停止提交；请恢复存储后核对变更记录，不要重复创建变更。')

function canonical(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`
  if (value && typeof value === 'object') return `{${Object.entries(value).sort(([a], [b]) => a.localeCompare(b)).map(([k, v]) => `${JSON.stringify(k)}:${canonical(v)}`).join(',')}}`
  return JSON.stringify(value) ?? 'null'
}

/** UI 范围指纹，不是授权凭证；不保存原始表单（可能含秘密）或成员对象。 */
export function scopeDigest(value: unknown): string {
  const text = canonical(value)
  let a = 2166136261
  let b = 5381
  for (let i = 0; i < text.length; i++) {
    a = Math.imul(a ^ text.charCodeAt(i), 16777619)
    b = Math.imul(b, 33) ^ text.charCodeAt(i)
  }
  return `${(a >>> 0).toString(16)}-${(b >>> 0).toString(16)}-${text.length}`
}

function storageKey(accountId: string): string {
  if (!accountId) throw storageError()
  return prefix + encodeURIComponent(accountId)
}

export function readPending(accountId: string): PendingBatch[] {
  try {
    const raw = localStorage.getItem(storageKey(accountId))
    if (raw === null) return []
    const records: PendingBatch[] = JSON.parse(raw)
    if (!Array.isArray(records) || records.some((r) => !r || r.accountId !== accountId || !operations.includes(r.operation) || typeof r.scope !== 'string' || typeof r.targetDigest !== 'string' || !Number.isInteger(r.targetCount) || typeof r.key !== 'string' || !r.key || typeof r.request?.preview_id !== 'string' || r.request.confirm !== true)) throw storageError()
    return records
  } catch {
    throw storageError()
  }
}

export function savePending(record: PendingBatch): void {
  const records = readPending(record.accountId)
  const existing = records.find((r) => r.scope === record.scope)
  if (existing && existing.key !== record.key) throw new Error('该范围已有未确认提交，请先核对原提交。')
  persist(record.accountId, [...records.filter((r) => r.scope !== record.scope), record])
}

function persist(accountId: string, records: PendingBatch[]): void {
  try {
    const key = storageKey(accountId)
    const text = JSON.stringify(records)
    localStorage.setItem(key, text)
    if (localStorage.getItem(key) !== text) throw storageError()
  } catch {
    throw storageError()
  }
}

export function clearPending(record: PendingBatch): void {
  persist(record.accountId, readPending(record.accountId).filter((r) => r.key !== record.key))
}
