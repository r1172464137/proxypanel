import { describe, expect, it } from 'vitest'
import {
  describeBlockReason,
  describeConsequences,
  describeSnapshotChanges,
  describeConsequence,
} from './batchConsequences'

// 批量预览的后果码与快照差异必须翻成人话。
//
// 背景：这些码以前前端完全没处理（grep 零结果），一旦渲染明细就会露出
// nodes_removed / loses_access / LAST_ADMIN 这类机器码。

describe('后果码翻译', () => {
  it('每个已知后果码都有中文说明与色调', () => {
    for (const code of ['nodes_added', 'nodes_removed', 'loses_access', 'over_quota_after']) {
      const t = describeConsequence(code)
      expect(t.label, `${code} 缺中文`).not.toBe(code)
      expect(t.label).toMatch(/[\u4e00-\u9fa5]/)
      expect(['danger', 'warn', 'info', 'ok']).toContain(t.tone)
    }
  })

  it('失去访问是 danger，新增节点是 ok', () => {
    expect(describeConsequence('loses_access').tone).toBe('danger')
    expect(describeConsequence('nodes_removed').tone).toBe('danger')
    expect(describeConsequence('nodes_added').tone).toBe('ok')
  })

  it('未收录的码原样回显并标为 warn', () => {
    const t = describeConsequence('brand_new_cause')
    expect(t.label).toContain('brand_new_cause')
    expect(t.tone).toBe('warn')
  })

  it('列表去重且保持顺序', () => {
    const out = describeConsequences(['nodes_removed', 'loses_access', 'nodes_removed'])
    expect(out).toHaveLength(2)
    expect(out[0]!.label).toContain('失去部分或全部可用节点')
    expect(out[1]!.label).toContain('失去代理权限')
  })

  it('空输入不产生条目', () => {
    expect(describeConsequences([])).toEqual([])
    expect(describeConsequences(undefined)).toEqual([])
    expect(describeConsequences(null)).toEqual([])
  })
})

describe('不可执行原因翻译', () => {
  it('LAST_ADMIN 说明为什么不能停用', () => {
    const text = describeBlockReason('LAST_ADMIN')
    expect(text).not.toBe('LAST_ADMIN')
    expect(text).toContain('管理员')
  })

  it('未知码原样回显，空值给默认文案', () => {
    expect(describeBlockReason('SOME_NEW_CODE')).toBe('SOME_NEW_CODE')
    expect(describeBlockReason(null)).toBe('不可执行')
    expect(describeBlockReason(undefined)).toBe('不可执行')
  })
})

describe('快照差异', () => {
  it('停用会说明账户状态变化', () => {
    const changes = describeSnapshotChanges({ active: true }, { active: false })
    expect(changes.join(' ')).toContain('停用')
  })

  it('节点数变化会显示前后对比', () => {
    const changes = describeSnapshotChanges({ allowed_nodes: 5 }, { allowed_nodes: 0 })
    expect(changes.join(' ')).toBe('可用节点 5 → 0 个')
  })

  it('额度变化用可读单位', () => {
    const changes = describeSnapshotChanges({ quota_bytes: 1073741824 }, { quota_bytes: null })
    expect(changes.join(' ')).toContain('不限量')
    expect(changes.join(' ')).toContain('1.00 GB')
  })

  it('字段没变或缺失时不产生噪音', () => {
    expect(describeSnapshotChanges({ active: true }, { active: true })).toEqual([])
    expect(describeSnapshotChanges(undefined, { active: true })).toEqual([])
    expect(describeSnapshotChanges({ active: true }, undefined)).toEqual([])
  })
})
