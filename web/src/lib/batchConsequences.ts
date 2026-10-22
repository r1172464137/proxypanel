/**
 * 批量预览后果码的中文说明。
 *
 * 服务端在 preview 的每个成员条目里给出 `consequences`（如 `nodes_removed`），
 * 以及 `blocked` / `over_quota_after` / `has_access_after` 等判定。这些直接
 * 显示会变成机器码，所以统一在这里翻译。
 *
 * 与 `accessReasons.ts` 同样属于"把内部标识翻成人话"的共享模块。
 */

export interface ConsequenceText {
  /** 一句话说明会发生什么。 */
  label: string
  /** 严重程度，用于选颜色。 */
  tone: 'danger' | 'warn' | 'info' | 'ok'
}

const CONSEQUENCES: Record<string, ConsequenceText> = {
  nodes_added: { label: '会新增可用节点', tone: 'ok' },
  nodes_removed: { label: '会失去部分或全部可用节点', tone: 'danger' },
  loses_access: { label: '会失去代理权限（连接立即中断）', tone: 'danger' },
  over_quota_after: { label: '变更后会超出流量额度', tone: 'warn' },
  expires_soon: { label: '变更后即将到期', tone: 'warn' },
  quota_changed: { label: '流量额度会改变', tone: 'info' },
  expiry_changed: { label: '到期时间会改变', tone: 'info' },
  subscription_changed: { label: '绑定的订阅会改变', tone: 'info' },
}

/** 单个后果码 → 中文。未收录的原样回显，便于发现后端新增。 */
export function describeConsequence(code: string): ConsequenceText {
  return CONSEQUENCES[code] ?? { label: `未识别的后果：${code}`, tone: 'warn' }
}

/** 后果码列表 → 中文条目（去重，保持顺序）。 */
export function describeConsequences(codes: string[] | undefined | null): ConsequenceText[] {
  if (!codes || codes.length === 0) return []
  const seen = new Set<string>()
  const out: ConsequenceText[] = []
  for (const code of codes) {
    if (seen.has(code)) continue
    seen.add(code)
    out.push(describeConsequence(code))
  }
  return out
}

/**
 * 把 before/after 两个快照压成几句人话差异。
 *
 * 只比较预览里真正有意义的字段，避免把内部字段全摊开。返回空数组表示
 * 这些字段都没变（或字段缺失）。
 */
export function describeSnapshotChanges(
  before: Record<string, unknown> | undefined,
  after: Record<string, unknown> | undefined,
): string[] {
  if (!before || !after) return []
  const out: string[] = []

  const bool = (v: unknown) => (typeof v === 'boolean' ? v : undefined)
  const num = (v: unknown) => (typeof v === 'number' ? v : undefined)

  const bActive = bool(before.active)
  const aActive = bool(after.active)
  if (bActive !== undefined && aActive !== undefined && bActive !== aActive) {
    out.push(aActive ? '账户由停用改为启用' : '账户由启用改为停用')
  }

  const bNodes = num(before.allowed_nodes)
  const aNodes = num(after.allowed_nodes)
  if (bNodes !== undefined && aNodes !== undefined && bNodes !== aNodes) {
    out.push(`可用节点 ${bNodes} → ${aNodes} 个`)
  }

  const bQuota = before.quota_bytes
  const aQuota = after.quota_bytes
  if (bQuota !== aQuota) {
    out.push(`流量额度 ${formatQuotaValue(bQuota)} → ${formatQuotaValue(aQuota)}`)
  }

  const bExp = before.expires_at
  const aExp = after.expires_at
  if (bExp !== aExp) {
    out.push(`到期时间 ${formatDateValue(bExp)} → ${formatDateValue(aExp)}`)
  }

  return out
}

function formatQuotaValue(v: unknown): string {
  if (v === null || v === undefined) return '不限量'
  if (typeof v !== 'number') return String(v)
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let n = v
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i += 1
  }
  return `${n >= 100 ? n.toFixed(0) : n.toFixed(2)} ${units[i]}`
}

function formatDateValue(v: unknown): string {
  if (v === null || v === undefined || v === '') return '不设'
  if (typeof v !== 'string') return String(v)
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return v
  return d.toISOString().slice(0, 16).replace('T', ' ')
}

/**
 * 预览里"不可执行"的原因码。
 *
 * 服务端在 blocked_reason 与 exceptions[].reason 里给的是 `LAST_ADMIN` 这类
 * 大写常量。直接显示会让管理员看到一个常量名而不知道该怎么办。
 */
const BLOCK_REASONS: Record<string, string> = {
  LAST_ADMIN: '这也是最后一个启用的管理员，停用后没人能管理系统',
  NODE_IN_USE: '该对象仍被其它资源引用',
  MEMBER_BOUND: '该成员仍绑定着订阅',
}

/** 不可执行原因 → 中文解释。未收录的原样回显。 */
export function describeBlockReason(code: string | null | undefined): string {
  if (!code) return '不可执行'
  return BLOCK_REASONS[code] ?? code
}
