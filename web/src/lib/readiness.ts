/**
 * 升级预检 check.id → 简短中文及下一步。
 * 只翻译已知的检查 id；未知/未来 id 一律回落到明确的“未知检查项”，
 * 不得因此显示“正常/通过”，原始 id、summary、evidence 由调用方保留在技术详情中。
 */
export interface ReadinessCheckLanguage {
  label: string
  next: string | null
}

const KNOWN: Record<string, ReadinessCheckLanguage> = {
  member_cycle_unproven: { label: '成员周期无法验证', next: '核对成员绑定历史与账本原始记录，不假定周期已成立' },
  ledger_rows_without_period: { label: '用量账本缺少可用周期', next: '只报告不自动补算，请核对原始记录' },
  member_period_overlap: { label: '成员周期存在重叠', next: '核对历史区间后按部署说明处理' },
  usage_batch_clock_skew: { label: '用量批次时间偏差', next: '核对服务器时钟' },
  batch_task_orphan: { label: '变更任务缺少关联记录', next: '核对批次与任务' },
  agent_state_unreadable: { label: '无法读取管理程序状态', next: '重试核对或查看诊断详情' },
  migration_checksum_mismatch: { label: '迁移校验和不一致', next: '核对部署文件与数据库登记' },
  pending_migration: { label: '存在待应用迁移', next: '先备份并按部署说明处理，不要直接升级' },
  node_unattributable_pending: { label: '待确认用量无法证明节点归属', next: '先核对待补报队列' },
}

export const UNKNOWN_CHECK: ReadinessCheckLanguage = { label: '未知检查项', next: '请查看诊断详情' }

export function readinessCheckLanguage(id: string | null | undefined): ReadinessCheckLanguage {
  return (id ? KNOWN[id] : undefined) ?? UNKNOWN_CHECK
}

/** 单行文案：已知项给短中文 + 下一步，未知项给中文兜底。 */
export function readinessCheckText(id: string | null | undefined): string {
  const language = readinessCheckLanguage(id)
  return language.next ? `${language.label}；下一步：${language.next}` : language.label
}
