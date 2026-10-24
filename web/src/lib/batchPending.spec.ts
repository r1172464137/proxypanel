import { beforeEach, describe, expect, it, vi } from 'vitest'
import { clearPending, readPending, savePending, scopeDigest, type PendingBatch } from './batchPending'

const record: PendingBatch = {
  accountId: 'a', operation: 'set_active', scope: 'set_active:scope-a',
  targetDigest: 'targets', targetCount: 2, key: 'same-key',
  request: { preview_id: 'preview-a', rules_version: 7, member_set_digest: 'server-digest', confirm: true },
}

beforeEach(() => localStorage.clear())
describe('batch pending persistence', () => {
  it('隔离账号和操作范围，重读保持同 key 与完整校验字段，明确完成后只清对应项', () => {
    savePending(record)
    savePending({ ...record, scope: 'set_expiry:scope-b', operation: 'set_expiry', key: 'other-key' })
    expect(readPending('b')).toEqual([])
    expect(readPending('a')).toEqual([record, { ...record, scope: 'set_expiry:scope-b', operation: 'set_expiry', key: 'other-key' }])
    expect(() => savePending({ ...record, key: 'new-key' })).toThrow('已有未确认提交')
    clearPending(record)
    expect(readPending('a').map((r) => r.key)).toEqual(['other-key'])
  })
  it('范围摘要稳定，不持久化原始 values 或认证秘密', () => {
    expect(scopeDigest({ b: 2, a: 1 })).toBe(scopeDigest({ a: 1, b: 2 }))
    expect(scopeDigest(['a'])).not.toBe(scopeDigest(['b']))
    savePending(record)
    const raw = localStorage.getItem(localStorage.key(0)!)!
    expect(JSON.parse(raw)).toEqual([record])
    expect(raw).not.toMatch(/csrf|password|token|values/)
  })
  it('读取失败、损坏和静默丢写均 fail closed，保留损坏记录而非覆盖', () => {
    localStorage.setItem('proxypanel.batch-pending.v1:a', '{broken')
    expect(() => readPending('a')).toThrow('无法安全恢复')
    expect(() => savePending(record)).toThrow('无法安全恢复')
    localStorage.clear()
    const read = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied') })
    expect(() => readPending('a')).toThrow('无法安全恢复')
    read.mockRestore()
    const write = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => undefined)
    expect(() => savePending(record)).toThrow('无法安全恢复')
    write.mockRestore()
  })
})
