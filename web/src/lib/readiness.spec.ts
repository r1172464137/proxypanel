import { describe, expect, it } from 'vitest'
import { readinessCheckLanguage, readinessCheckText, UNKNOWN_CHECK } from './readiness'

describe('readiness check language', () => {
  it('已知检查项给出短中文与下一步，不直接展示后端英文 remedy', () => {
    expect(readinessCheckText('migration_checksum_mismatch')).toContain('迁移校验和不一致')
    expect(readinessCheckText('migration_checksum_mismatch')).toContain('下一步')
    expect(readinessCheckText('pending_migration')).toContain('待应用迁移')
    expect(readinessCheckLanguage('member_cycle_unproven').label).not.toMatch(/[a-z_]{4,}/)
  })

  it('未知或未来检查项不冒充通过，回落中文兜底', () => {
    for (const id of ['future_check_state', '', null, undefined]) {
      expect(readinessCheckLanguage(id)).toEqual(UNKNOWN_CHECK)
      expect(readinessCheckText(id)).toContain('未知检查项')
      expect(readinessCheckText(id)).not.toContain('通过')
      expect(readinessCheckText(id)).not.toContain('正常')
    }
  })
})
