import { describe, expect, it } from 'vitest'
import type { BatchPreview } from '@/api/types'
import {
  applyPreview,
  batchSelectionKey,
  canPreview,
  canSubmit,
  createBatchState,
  isValidPreviewTime,
  newIdempotencyKey,
  previewSummary,
  submitRequest,
} from './batch'
import { ApiError } from '@/api/http'
import { isPreviewRequiredError } from './batch'

const preview: BatchPreview = {
  id: 'prv_1',
  version: 41,
  affected_count: 3,
  affected_nodes: 5,
  exceptions: [{ member_id: 'mbr_x', reason: 'LAST_ADMIN' }],
  warnings: ['mbr_C 已超额'],
  expires_at: new Date(Date.now() + 10 * 60 * 1000).toISOString(),
}

describe('批量状态机', () => {
  it('未选择成员或缺少必填参数时不能预览', () => {
    const state = createBatchState('assign_subscription')
    expect(canPreview(state)).toEqual({ ok: false, reason: '请先选择至少一名成员' })
    state.memberIds = ['mbr_a']
    expect(canPreview(state).ok).toBe(false)
    state.values = { subscription_id: 'sub_1' }
    expect(canPreview(state).ok).toBe(true)
  })

  it('预览后必须显式确认且集合未变化才能提交', () => {
    const state = createBatchState('assign_subscription')
    state.memberIds = ['mbr_a', 'mbr_b']
    state.values = { subscription_id: 'sub_1' }
    const withPreview = applyPreview(state, preview)
    expect(canSubmit(withPreview).ok).toBe(false)
    const confirmed = { ...withPreview, confirmed: true }
    expect(canSubmit(confirmed).ok).toBe(true)
  })

  it('预览后成员集合变化 → 必须重新预览', () => {
    const state = createBatchState('set_active')
    state.memberIds = ['mbr_a']
    state.values = { active: false }
    const confirmed = { ...applyPreview(state, preview), confirmed: true }
    expect(canSubmit(confirmed).ok).toBe(true)

    const changed = { ...confirmed, memberIds: ['mbr_a', 'mbr_b'] }
    expect(canSubmit(changed)).toEqual({ ok: false, reason: '成员集合或参数已变化，需重新预览' })
    expect(batchSelectionKey(changed)).not.toBe(batchSelectionKey(confirmed))
  })

  it('参数变化与顺序无关的成员集合产生相同指纹', () => {
    const a = { operation: 'set_expiry' as const, memberIds: ['m1', 'm2'], values: { expires_at: 'x' } }
    const b = { operation: 'set_expiry' as const, memberIds: ['m2', 'm1'], values: { expires_at: 'x' } }
    expect(batchSelectionKey(a)).toBe(batchSelectionKey(b))
  })

  it('过期预览不可提交', () => {
    const state = createBatchState('clear_override')
    state.memberIds = ['m1']
    const expired = { ...preview, expires_at: new Date(Date.now() - 1000).toISOString() }
    const confirmed = { ...applyPreview(state, expired), confirmed: true }
    expect(isValidPreviewTime(expired.expires_at)).toBe(false)
    expect(canSubmit(confirmed)).toEqual({ ok: false, reason: '预览已过期，请重新生成预览' })
  })

  it('提交体只包含 preview_id 与 confirm:true', () => {
    expect(submitRequest(preview)).toEqual({ preview_id: 'prv_1', confirm: true })
  })

  it('幂等键每次不同', () => {
    const keys = new Set(Array.from({ length: 20 }, () => newIdempotencyKey()))
    expect(keys.size).toBe(20)
  })

  it('PREVIEW_REQUIRED 判定', () => {
    expect(isPreviewRequiredError(new ApiError(409, { code: 'PREVIEW_REQUIRED', message: '需要预览' }))).toBe(true)
    expect(isPreviewRequiredError(new ApiError(409, { code: 'CONFLICT', message: '冲突' }))).toBe(false)
    expect(isPreviewRequiredError(new Error('x'))).toBe(false)
  })

  it('摘要包含影响成员与节点，不宣称全部生效', () => {
    const summary = previewSummary(preview)
    expect(summary).toContain('影响成员 3 名')
    expect(summary).toContain('受影响节点 5 个')
    expect(summary).toContain('不可执行 1 项')
    expect(summary).not.toContain('已生效')
  })
})