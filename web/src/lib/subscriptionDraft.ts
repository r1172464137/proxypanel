/** 订阅表单逻辑：配额不限量映射为 quota_bytes=null；周期规则与访问开关分离。 */
import { bytesToGib, gibToBytes, isValidTimezone } from './format'
import type { SubscriptionRecord, SubscriptionWrite } from '@/api/types'

export interface SubscriptionDraft {
  name: string
  description: string
  node_group_ids: string[]
  unlimited: boolean
  quota_gib: string
  timezone: string
  anchor_day: string
  default_valid_days: string
  allow_new_bindings: boolean
  access_enabled: boolean
  archived: boolean
  template_id: string
}

export const DEFAULT_TIMEZONE = 'Asia/Shanghai'

export function emptySubscriptionDraft(): SubscriptionDraft {
  return {
    name: '',
    description: '',
    node_group_ids: [],
    unlimited: false,
    quota_gib: '100',
    timezone: DEFAULT_TIMEZONE,
    anchor_day: '1',
    default_valid_days: '',
    allow_new_bindings: true,
    access_enabled: true,
    archived: false,
    template_id: '',
  }
}

export function draftFromSubscription(sub: SubscriptionRecord): SubscriptionDraft {
  return {
    name: sub.name ?? '',
    description: sub.description ?? '',
    node_group_ids: [...(sub.node_group_ids ?? [])],
    unlimited: sub.quota_bytes === null,
    quota_gib: sub.quota_bytes === null ? '' : bytesToGib(sub.quota_bytes),
    timezone: sub.timezone || DEFAULT_TIMEZONE,
    anchor_day: String(sub.anchor_day ?? 1),
    default_valid_days: sub.default_valid_days === null || sub.default_valid_days === undefined ? '' : String(sub.default_valid_days),
    allow_new_bindings: sub.allow_new_bindings !== false,
    access_enabled: sub.access_enabled !== false,
    archived: sub.archived === true,
    template_id: sub.template_id ?? '',
  }
}

export interface SubscriptionBuild {
  payload: SubscriptionWrite | null
  errors: string[]
  warnings: string[]
}

export function buildSubscriptionWrite(draft: SubscriptionDraft): SubscriptionBuild {
  const errors: string[] = []
  const warnings: string[] = []

  if (!draft.name.trim()) errors.push('订阅名称必填')
  if (!draft.timezone.trim()) errors.push('业务时区必填（IANA 名称，如 Asia/Shanghai）')
  else if (!isValidTimezone(draft.timezone.trim())) errors.push(`业务时区无效：${draft.timezone}`)

  const anchor = Number(draft.anchor_day)
  if (!Number.isInteger(anchor) || anchor < 1 || anchor > 31) errors.push('周期起始日必须是 1–31 的整数（月末自动裁剪）')

  let quota: number | null = null
  if (!draft.unlimited) {
    quota = gibToBytes(draft.quota_gib)
    if (quota === null || quota <= 0) errors.push('每人配额必须是大于 0 的 GiB 数值（或选择不限量）')
  }

  let defaultDays: number | null = null
  if (draft.default_valid_days.trim()) {
    const parsed = Number(draft.default_valid_days)
    if (!Number.isInteger(parsed) || parsed < 1 || parsed > 3650) {
      errors.push('默认有效期必须是 1–3650 天的整数，或留空表示不设置默认')
    } else {
      defaultDays = parsed
    }
  }

  if (!draft.node_group_ids.length) warnings.push('未选择节点组：成员将没有可用节点')
  if (!draft.allow_new_bindings) warnings.push('已关闭新绑定：成员不能再切换到该订阅')
  if (!draft.access_enabled) warnings.push('已暂停访问：现有成员的代理访问将被硬撤权（连接中断）')
  if (draft.archived) warnings.push('归档后不可再绑定；仍有绑定成员时服务端会拒绝或要求迁移')
  if (!draft.default_valid_days.trim()) warnings.push('未设置默认有效期：绑定成员时需明确填写实际有效期')

  if (errors.length) return { payload: null, errors, warnings }
  return {
    payload: {
      name: draft.name.trim(),
      description: draft.description.trim() || null,
      node_group_ids: [...draft.node_group_ids],
      quota_bytes: quota,
      timezone: draft.timezone.trim(),
      anchor_day: anchor,
      default_valid_days: defaultDays,
      allow_new_bindings: draft.allow_new_bindings,
      access_enabled: draft.access_enabled,
      archived: draft.archived,
      template_id: draft.template_id.trim() || null,
    },
    errors: [],
    warnings,
  }
}

/** 从表单生成 PATCH 用的 patch 对象（批量影响预览 values.patch 同样使用）。 */
export function subscriptionPatch(draft: SubscriptionDraft): { patch: SubscriptionWrite | null; errors: string[] } {
  const built = buildSubscriptionWrite(draft)
  return { patch: built.payload, errors: built.errors }
}