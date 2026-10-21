/**
 * 批量预览/提交状态机。契约要求：
 * 预览 → 确认 → 提交（preview_id + confirm:true + Idempotency-Key）；
 * 成员集合或操作变化后必须重新预览，不许复用旧预览提交。
 */
import type { BatchOperation, BatchPreview } from '@/api/types'
import { errorCode } from './errors'

export interface BatchValueField {
  key: string
  label: string
  type: 'subscription' | 'boolean' | 'datetime' | 'patch'
  required: boolean
  help?: string
}

export interface BatchOperationMeta {
  value: BatchOperation
  label: string
  description: string
  fields: BatchValueField[]
}

export const BATCH_OPERATIONS: BatchOperationMeta[] = [
  {
    value: 'assign_subscription',
    label: '分配 / 切换订阅',
    description: '把选中的成员绑定到目标订阅；保留成员当前周期与已用量。',
    fields: [{ key: 'subscription_id', label: '目标订阅', type: 'subscription', required: true }],
  },
  {
    value: 'set_active',
    label: '启用 / 停用账户',
    description: '停用会撤销会话并硬撤权（现有连接中断）。',
    fields: [{ key: 'active', label: '账户状态', type: 'boolean', required: true }],
  },
  {
    value: 'set_expiry',
    label: '调整成员有效期',
    description: '只改成员绑定的有效期，不修改订阅周期规则。',
    fields: [{ key: 'expires_at', label: '新的到期时间', type: 'datetime', required: true }],
  },
  {
    value: 'clear_override',
    label: '清除个人覆盖',
    description: '清除配额/有效期覆盖，恢复继承订阅默认值。',
    fields: [],
  },
  {
    value: 'update_subscription',
    label: '订阅规则影响预览',
    description: '订阅规则变更影响现有成员时使用；预览后提交，服务端事务提交规则。',
    fields: [{ key: 'patch', label: '订阅变更', type: 'patch', required: true }],
  },
  {
    value: 'update_node_group',
    label: '节点组成员影响预览',
    description: '节点组成员变化影响现有订阅/成员授权时使用。',
    fields: [{ key: 'patch', label: '节点组变更', type: 'patch', required: true }],
  },
]

export function batchOperationMeta(operation: BatchOperation): BatchOperationMeta {
  return BATCH_OPERATIONS.find((item) => item.value === operation) ?? BATCH_OPERATIONS[0]!
}

export interface BatchState {
  operation: BatchOperation
  memberIds: string[]
  values: Record<string, unknown>
  preview: BatchPreview | null
  previewKey: string
  confirmed: boolean
}

export function createBatchState(operation: BatchOperation = 'assign_subscription'): BatchState {
  return { operation, memberIds: [], values: {}, preview: null, previewKey: '', confirmed: false }
}

export function batchSelectionKey(state: Pick<BatchState, 'operation' | 'memberIds' | 'values'>): string {
  const ids = [...state.memberIds].sort()
  return JSON.stringify({ operation: state.operation, ids, values: state.values })
}

export function isValidPreviewTime(expiresAt: string, now: number = Date.now()): boolean {
  const time = new Date(expiresAt).getTime()
  return Number.isFinite(time) && time > now
}

export interface Gate {
  ok: boolean
  reason?: string
}

export function canPreview(state: BatchState): Gate {
  if (!state.memberIds.length) return { ok: false, reason: '请先选择至少一名成员' }
  if (batchOperationMeta(state.operation).fields.some((field) => field.required && isEmptyValue(state.values[field.key]))) {
    return { ok: false, reason: '请填写操作所需的参数' }
  }
  return { ok: true }
}

function isEmptyValue(value: unknown): boolean {
  if (value === undefined || value === null || value === '') return true
  if (typeof value === 'object' && !Array.isArray(value) && Object.keys(value as object).length === 0) return true
  return false
}

export function canSubmit(state: BatchState, now: number = Date.now()): Gate {
  if (!state.preview) return { ok: false, reason: '尚未生成预览' }
  if (state.previewKey !== batchSelectionKey(state)) {
    return { ok: false, reason: '成员集合或参数已变化，需重新预览' }
  }
  if (!state.confirmed) return { ok: false, reason: '请先确认预览中的影响范围' }
  if (!isValidPreviewTime(state.preview.expires_at, now)) {
    return { ok: false, reason: '预览已过期，请重新生成预览' }
  }
  return { ok: true }
}

/** 生成预览后记录指纹；之后任何选择变化都会让提交被拒绝。 */
export function applyPreview(state: BatchState, preview: BatchPreview): BatchState {
  return {
    ...state,
    preview,
    previewKey: batchSelectionKey(state),
    confirmed: false,
  }
}

export function previewRequest(state: BatchState): { operation: BatchOperation; member_ids: string[]; values: Record<string, unknown> } {
  return { operation: state.operation, member_ids: [...state.memberIds], values: { ...state.values } }
}

export function submitRequest(preview: BatchPreview): { preview_id: string; confirm: true } {
  return { preview_id: preview.id, confirm: true }
}

export function newIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  const random = Math.random().toString(36).slice(2)
  return `pp-${Date.now().toString(36)}-${random}`
}

/** 契约：直接 PATCH 影响现有授权时返回 PREVIEW_REQUIRED，前端转预览流程。 */
export function isPreviewRequiredError(error: unknown): boolean {
  return errorCode(error) === 'PREVIEW_REQUIRED'
}

export function previewSummary(preview: BatchPreview): string {
  const parts = [
    `影响成员 ${preview.affected_count} 名`,
    `受影响节点 ${preview.affected_nodes} 个`,
  ]
  if (preview.exceptions.length) parts.push(`不可执行 ${preview.exceptions.length} 项`)
  if (preview.warnings.length) parts.push(`警告 ${preview.warnings.length} 条`)
  return parts.join(' · ')
}