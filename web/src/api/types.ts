/**
 * 领域类型。字段以 docs/specs/implementation-contract.md（冻结契约）为准，
 * 并与已落地的 internal/panel 响应字段对齐（domain/types.go）。
 * 未在契约中逐字段列出的资源使用“已知可选字段 + 未知字段保留”的宽容类型。
 */

export type Role = 'admin' | 'member'

export interface User {
  id: string
  username: string
  role: Role
  active?: boolean
}

export interface SessionPayload {
  user: User
  csrf_token: string
}

export interface LoginResponse extends SessionPayload {}

export interface RedeemResponse {
  user?: User
  [key: string]: unknown
}

export type SubscriptionFormat = 'xray' | 'mihomo' | 'singbox' | 'v2rayn'

export const SUBSCRIPTION_FORMATS: SubscriptionFormat[] = ['xray', 'mihomo', 'singbox', 'v2rayn']

export const SUBSCRIPTION_FORMAT_LABELS: Record<SubscriptionFormat, string> = {
  xray: 'Xray（原生 JSON）',
  mihomo: 'Mihomo / Clash',
  singbox: 'sing-box',
  v2rayn: 'v2rayN（节点 URI）',
}

export type NodeProtocol = 'vless' | 'vmess' | 'trojan' | 'shadowsocks'

export const NODE_PROTOCOLS: NodeProtocol[] = ['vless', 'vmess', 'trojan', 'shadowsocks']

export const NODE_PROTOCOL_LABELS: Record<NodeProtocol, string> = {
  vless: 'VLESS',
  vmess: 'VMess',
  trojan: 'Trojan',
  shadowsocks: 'Shadowsocks',
}

export const TEMPLATE_FORMATS = ['xray', 'mihomo', 'singbox'] as const
export type TemplateFormat = (typeof TEMPLATE_FORMATS)[number]

export interface ServerRecord {
  id: string
  version: number
  name: string
  address: string
  region?: string | null
  notes?: string | null
  expires_at?: string | null
  cost_minor?: number | null
  currency?: string | null
  archived?: boolean
  created_at?: string
  updated_at?: string
  [key: string]: unknown
}

export interface ServerWrite {
  name: string
  address: string
  region?: string | null
  notes?: string | null
  expires_at?: string | null
  cost_minor?: number | null
  currency?: string | null
}

export interface RegistrationCode {
  code: string
  expires_at: string
}

export interface NodeRecord {
  id: string
  /** 客户端连接的端口；为空表示与 port 相同（用于「对外 443 → 实际 20101」）。 */
  external_port?: number
  version: number
  name: string
  server_id: string
  enabled: boolean
  host: string
  port: number
  listen: string
  protocol: NodeProtocol
  flow?: string | null
  cipher?: string | null
  stream_settings: Record<string, unknown>
  protocol_settings: Record<string, unknown>
  client_settings: Record<string, unknown>
  sort_order: number
  /** 服务端是否已保存节点级秘密；明文 server_key 永不返回。 */
  server_key_set?: boolean
  archived?: boolean
  created_at?: string
  updated_at?: string
  [key: string]: unknown
}

export interface NodeWrite {
  name: string
  server_id: string
  enabled: boolean
  host: string
  port: number
  /** 客户端连接的端口（对外端口）；0 表示与 port 相同。 */
  external_port?: number
  listen: string
  protocol: NodeProtocol
  flow?: string | null
  cipher?: string | null
  /** 仅写：服务端加密保存，读取只返回 server_key_set。 */
  server_key?: string | null
  stream_settings: Record<string, unknown>
  protocol_settings: Record<string, unknown>
  client_settings: Record<string, unknown>
  sort_order: number
}

export interface NodeGroupRecord {
  id: string
  version: number
  name: string
  description?: string | null
  node_ids: string[]
  created_at?: string
  updated_at?: string
  [key: string]: unknown
}

export interface NodeGroupWrite {
  name: string
  description?: string | null
  node_ids: string[]
}

export interface SubscriptionRecord {
  id: string
  version: number
  name: string
  description?: string | null
  node_group_ids: string[]
  /** null = 不限量 */
  quota_bytes: number | null
  timezone: string
  anchor_day: number
  default_valid_days: number | null
  allow_new_bindings: boolean
  access_enabled: boolean
  archived: boolean
  template_id: string | null
  pending_period_rule?: Record<string, unknown> | null
  bound_members?: number
  created_at?: string
  updated_at?: string
  [key: string]: unknown
}

export interface SubscriptionWrite {
  name: string
  description?: string | null
  node_group_ids: string[]
  quota_bytes: number | null
  timezone: string
  anchor_day: number
  default_valid_days: number | null
  allow_new_bindings: boolean
  access_enabled: boolean
  archived: boolean
  template_id: string | null
}

export interface MemberRecord {
  id: string
  version: number
  username: string
  role: Role
  active: boolean
  proxy_enabled: boolean
  subscription_id: string | null
  expires_at: string | null
  quota_override_bytes: number | null
  quota_override_enabled: boolean
  data_complete?: boolean
  data_issues?: string[]
  /** 派生值（服务端逐请求计算） */
  used_bytes?: number
  effective_quota_bytes?: number | null
  period_start?: string | null
  period_end?: string | null
  period_timezone?: string | null
  period_anchor_day?: number | null
  access_reasons?: string[]
  proxy_access?: string | null
  over_quota?: boolean
  archived?: boolean
  allowed_node_ids?: string[]
  bound_at?: string | null
  created_at?: string
  updated_at?: string
  last_login_at?: string | null
  [key: string]: unknown
}

export interface MemberWrite {
  username?: string
  password?: string
  role?: Role
  active?: boolean
  proxy_enabled?: boolean
  subscription_id?: string | null
  expires_at?: string | null
  quota_override_bytes?: number | null
  quota_override_enabled?: boolean
}

export interface TemplateRecord {
  id: string
  version: number
  name: string
  format: TemplateFormat
  content: Record<string, unknown>
  description?: string | null
  [key: string]: unknown
}

export interface TemplateWrite {
  name: string
  format: TemplateFormat
  content: Record<string, unknown>
  description?: string | null
}

export interface OverviewCounts {
  members: number
  servers: number
  nodes: number
  subscriptions: number
}

export interface EventRecord {
  id: string
  kind?: string | null
  severity?: string | null
  subject_type?: string | null
  subject_id?: string | null
  message?: string | null
  title?: string | null
  code?: string | null
  state?: string | null
  created_at?: string | null
  acked_at?: string | null
  acknowledged_at?: string | null
  acked_by?: string | null
  dedupe_key?: string | null
  [key: string]: unknown
}

export interface Overview {
  counts: OverviewCounts
  usage_bytes: number
  agents_online: number
  pending_tasks: number
  recent_events: EventRecord[]
}

export interface AgentRecord {
  id?: string
  agent_id?: string
  server_id?: string
  name?: string
  online?: boolean
  status?: string | null
  last_heartbeat_at?: string | null
  heartbeat_at?: string | null
  applied_revision?: number | null
  desired_revision?: number | null
  revision?: number | null
  version?: string | null
  xray_version?: string | null
  control_generation?: number | null
  applied_at?: string | null
  offline_seconds?: number | null
  queue_depth?: number | null
  token_prefix?: string | null
  [key: string]: unknown
}

export interface CoreInstanceRecord {
  id?: string
  node_id?: string
  node_name?: string
  server_id?: string
  agent_id?: string | null
  state?: string | null
  status?: string | null
  generation?: number | null
  config_revision?: number | null
  applied_revision?: number | null
  desired_revision?: number | null
  last_started_at?: string | null
  applied_at?: string | null
  last_error?: string | null
  error?: string | null
  updated_at?: string | null
  [key: string]: unknown
}

export interface InvitationRecord {
  id: string
  subscription_id?: string | null
  expires_at?: string | null
  state?: string | null
  status?: string | null
  max_uses?: number | null
  used_count?: number | null
  created_at?: string | null
  used_at?: string | null
  revoked_at?: string | null
  [key: string]: unknown
}

export interface InvitationCreate {
  subscription_id?: string | null
  expires_at?: string | null
}

export interface InvitationCreated {
  id?: string
  code: string
  expires_at: string
}

export type BatchOperation =
  | 'assign_subscription'
  | 'set_active'
  | 'set_expiry'
  | 'clear_override'
  | 'update_subscription'
  | 'update_node_group'

/**
 * 批量预览里的单个成员影响。
 *
 * 服务端会给出 before/after 快照与后果码。**前端必须把 consequences 翻成
 * 中文并逐条展示** —— 预览的意义就是让管理员在提交前看清"这个人会变成
 * 什么样"，只显示汇总数字等于没有预览。
 */
export interface PreviewItem {
  member_id: string
  username: string
  before?: Record<string, unknown>
  after?: Record<string, unknown>
  consequences?: string[]
  blocked?: boolean
  blocked_reason?: string
  over_quota_after?: boolean
  has_access_after?: boolean
  nodes_added?: number
  nodes_removed?: number
  expiring?: boolean
}

export interface BatchPreview {
  id: string
  version: number
  affected_count: number
  affected_nodes: number
  /** 逐成员影响明细。可能为空数组（例如只改节点组时没有成员级变化）。 */
  items?: PreviewItem[]
  exceptions: unknown[]
  warnings: string[]
  expires_at: string
  [key: string]: unknown
}

export interface BatchResultItem {
  member_id?: string
  node_id?: string
  agent_id?: string
  /** 服务端 domain.BatchItem 使用 state；兼容 status。 */
  state?: string
  status?: string
  task_id?: string
  error?: { code?: string; message?: string } | string | null
  note?: string | null
  retryable?: boolean
  [key: string]: unknown
}

export interface BatchSubmitResult {
  id: string
  state: string
  results: BatchResultItem[]
  [key: string]: unknown
}

export interface BatchDetail {
  id: string
  state?: string
  operation?: string
  created_at?: string
  affected_count?: number
  results?: BatchResultItem[]
  summary?: Record<string, unknown>
  [key: string]: unknown
}

export interface UsagePeriod {
  start?: string | null
  end?: string | null
  timezone?: string | null
}

export interface UsageItem {
  member_id?: string | null
  node_id?: string | null
  /** 服务端平铺周期（internal/panel domain.UsageRow）。 */
  period_start?: string | null
  period_end?: string | null
  period?: UsagePeriod | null
  up_bytes?: number
  down_bytes?: number
  estimated?: boolean
  boundary_split?: boolean
  correction?: boolean
  corrections?: unknown[]
  direction?: string | null
  algorithm_version?: string | null
  metering?: Record<string, unknown>
  [key: string]: unknown
}

export interface NicCounter {
  server_id?: string | null
  rx_bytes?: number
  tx_bytes?: number
  [key: string]: unknown
}

export interface UsagePage {
  items: UsageItem[]
  nic_counters?: NicCounter[]
  next_cursor?: string | null
}

export interface QuotaAdjustmentRecord {
  id?: string
  member_id?: string
  bytes?: number
  reason?: string
  period_start?: string | null
  created_by?: string | null
  created_at?: string | null
  [key: string]: unknown
}

/** GET /me/usage 的归一化结果（服务端 items[0] 形如 {usage:[], adjustments:[]}）。 */
export interface MeUsageResult {
  items: UsageItem[]
  adjustments: QuotaAdjustmentRecord[]
  next_cursor: string | null
}

export interface TaskRecord {
  id: string
  kind?: string | null
  state?: string | null
  status?: string | null
  agent_id?: string | null
  target_agent_id?: string | null
  node_id?: string | null
  member_id?: string | null
  batch_id?: string | null
  attempts?: number | null
  max_attempts?: number | null
  last_error?: string | null
  error?: { code?: string; message?: string } | string | null
  available_at?: string | null
  locked_at?: string | null
  created_at?: string | null
  updated_at?: string | null
  applied_at?: string | null
  [key: string]: unknown
}

export interface AuditRecord {
  id: string
  action?: string | null
  actor?: string | null
  actor_id?: string | null
  actor_username?: string | null
  target_type?: string | null
  target_id?: string | null
  ip?: string | null
  request_id?: string | null
  created_at?: string | null
  details?: unknown
  detail?: unknown
  [key: string]: unknown
}

export interface PanelSettings {
  timezone?: string
  retention_days?: number
  telegram_enabled?: boolean
  telegram_chat_id?: string | null
  /** 只写字段：GET 时仅返回是否已设置。 */
  telegram_token?: string
  telegram_token_set?: boolean
  template_defaults?: Record<string, unknown>
  /** 并发控制：PUT 必须回传 GET 得到的版本。 */
  version?: number
  updated_at?: string
  [key: string]: unknown
}

export interface CredentialRotationInput {
  scope: 'all_nodes' | 'single_node'
  node_id?: string
}

export interface QuotaAdjustmentInput {
  bytes: number
  reason: string
}

export interface TokenRotationResult {
  token: string
  url: string
}

export interface MeSubscription {
  url: string
  token: string
  formats: SubscriptionFormat[]
  format_status?: { format: SubscriptionFormat; available: boolean; reason: string }[]
  expires_at?: string | null
  period_end?: string | null
  rotation_impact?: string
}

export interface AccessStep {
  step?: string
  result?: string
  reason?: string
  detail?: string
  [key: string]: unknown
}

export interface AccessDecision {
  algorithm_version?: string
  member_id?: string
  access?: boolean
  proxy_access?: string
  access_reasons?: string[]
  access_chain?: AccessStep[]
  effective_quota_bytes?: number | null
  quota_source?: string
  used_bytes?: number
  expires_at?: string | null
  allowed_node_ids?: string[]
  node_reasons?: Record<string, string>
  [key: string]: unknown
}

export interface MeStatus {
  data_complete?: boolean
  data_issues?: string[]
  /** 服务端返回的成员记录（含派生值）。 */
  member?: MemberRecord
  /** 授权判定结果（与 Agent 快照同算法）。 */
  access?: AccessDecision
  nodes_allowed?: number
  [key: string]: unknown
}

export interface MeNode {
  id?: string
  node_id?: string
  name?: string
  protocol?: string | null
  host?: string | null
  port?: number | null
  network?: string | null
  security?: string | null
  flow?: string | null
  [key: string]: unknown
}

export interface SessionRecord {
  id: string
  created_at?: string | null
  last_seen_at?: string | null
  expires_at?: string | null
  ip?: string | null
  user_agent?: string | null
  current?: boolean
  [key: string]: unknown
}