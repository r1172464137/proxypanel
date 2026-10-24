import { describe, expect, it } from 'vitest'
import { ApiError } from '@/api/http'
import { describeError, fieldErrorsOf } from './errors'

describe('actionable error classification', () => {
  it('does not misclassify an unattributable ledger interval as client-format incompatibility', () => {
    const view = describeError(new ApiError(422, { code: 'LEDGER_UNATTRIBUTABLE', message: 'interval needs review' }))
    expect(view.title).toBe('用量资料需核对')
    expect(view.hint).toContain('不会自动补算')
    expect(view.title).not.toContain('客户端')
  })
  it('keeps cursor recovery distinct from ordinary input errors and preserves field details', () => {
    const error = new ApiError(400, { code: 'CURSOR_INVALID', message: 'invalid cursor', details: { field_errors: { cursor: 'expired_scope' } } })
    expect(describeError(error).title).toBe('列表已过期')
    expect(describeError(error).hint).toContain('第一页')
    expect(fieldErrorsOf(error)).toEqual({ cursor: 'expired_scope' })
    expect(describeError(new ApiError(400, { code: 'VALIDATION_FAILED', message: 'bad limit' })).title).toBe('提交内容有误')
  })
  it('provides a safe fallback for future codes without losing request identity', () => {
    const view = describeError(new ApiError(503, { code: 'FUTURE_DEPENDENCY_ERROR', message: 'temporarily unavailable', request_id: 'req_test', retryable: true }))
    expect(view).toMatchObject({ code: 'FUTURE_DEPENDENCY_ERROR', message: 'temporarily unavailable', requestId: 'req_test', retryable: true })
  })
})
