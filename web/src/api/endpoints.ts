/**
 * 端点封装。路径与语义严格对齐 docs/specs/implementation-contract.md。
 * 所有列表返回 PageResult（items + next_cursor）。
 */
import { api } from './http'
import { loadMeUsage } from '@/lib/meUsage'
import type {
  AgentRecord,
  AuditRecord,
  BatchDetail,
  BatchOperation,
  BatchPreview,
  BatchSubmitResult,
  CoreInstanceRecord,
  CredentialRotationInput,
  EventRecord,
  InvitationCreate,
  InvitationCreated,
  InvitationRecord,
  LoginResponse,
  MeNode,
  MeStatus,
  MeSubscription,
  MemberRecord,
  MemberWrite,
  NicCounter,
  NodeGroupRecord,
  NodeGroupWrite,
  NodeRecord,
  NodeWrite,
  Overview,
  PanelSettings,
  QuotaAdjustmentInput,
  RedeemResponse,
  RegistrationCode,
  ServerRecord,
  ServerWrite,
  SessionPayload,
  SessionRecord,
  SubscriptionRecord,
  SubscriptionWrite,
  TaskRecord,
  TemplateRecord,
  TemplateWrite,
  TokenRotationResult,
  UsageItem,
  UsagePage,
} from './types'

/**
 * API 基地址。默认空字符串 = 同源，与改造前的行为完全一致。
 *
 * 前后端分离部署时在构建期注入 VITE_API_BASE（如 https://api.example.com）。
 * 注意不能带尾部斜杠，这里统一裁掉以免拼出 `//api/v1`。
 */
const BASE = (import.meta.env.VITE_API_BASE ?? '').replace(/\/+$/, '')

const ADMIN = `${BASE}/api/v1/admin`
const ME = `${BASE}/api/v1/me`

export interface PageQuery {
  limit?: number
  cursor?: string | null
}

function pageQuery(query?: PageQuery): Record<string, string | number> {
  const out: Record<string, string | number> = {}
  if (query?.limit) out.limit = query.limit
  if (query?.cursor) out.cursor = query.cursor
  for (const [key, value] of Object.entries(query ?? {})) {
    if (!['limit', 'cursor'].includes(key) && value !== undefined && value !== '') out[key] = String(value)
  }
  return out
}

type JsonObject = Record<string, unknown>

function asObject(value: unknown): JsonObject | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? (value as JsonObject) : null
}

/**
 * 成员详情端点 `/api/v1/admin/members/{id}` 返回 `{member, access}`：member 是存储记录，
 * access 是唯一授权算法的判定结果。列表端点返回的是服务端已派生的扁平记录。
 * 这里按同一套派生规则（internal/panel/api/admin2.go `decorateMember`）把判定值合并回
 * 成员记录，使详情页与列表页展示的 used_bytes / effective_quota_bytes / access_reasons /
 * allowed_node_ids / proxy_access / over_quota 完全一致，前端不再自行推导授权。
 * 若服务端改为扁平返回，则原样透传（存在 access 时仍做合并）。
 */
export function memberRecordFromDetail(payload: unknown): MemberRecord {
  const envelope = asObject(payload) ?? {}
  const member = asObject(envelope.member) ?? envelope
  const access = asObject(envelope.access)
  const record: JsonObject = { ...member }
  if (access) {
    record.used_bytes = typeof access.used_bytes === 'number' ? access.used_bytes : undefined
    record.effective_quota_bytes = access.effective_quota_bytes
    record.access_reasons = Array.isArray(access.access_reasons) ? access.access_reasons : []
    record.allowed_node_ids = Array.isArray(access.allowed_node_ids) ? access.allowed_node_ids : []
    // proxy_access 缺失时退回账户自身的代理开关（与列表页的兜底一致）。
    record.proxy_access =
      typeof access.proxy_access === 'string' && access.proxy_access
        ? access.proxy_access
        : 'unknown'
    // 与 decorateMember 相同：over_quota 只由判定结果 over_quota 得出。
    record.over_quota = access.proxy_access === 'over_quota'
    if (typeof access.quota_source === 'string') record.quota_source = access.quota_source
    record.access = access
  }
  return record as unknown as MemberRecord
}

export const authApi = {
  login: (username: string, password: string) =>
    api.post<LoginResponse>(
      '/api/v1/auth/login',
      { username, password },
      { publicEndpoint: true, suppressAuthRedirect: true },
    ),
  session: () =>
    api.get<SessionPayload>('/api/v1/auth/session', {
      publicEndpoint: true,
      suppressAuthRedirect: true,
    }),
  logout: () => api.post<void>('/api/v1/auth/logout'),
  redeem: (code: string, username: string, password: string) =>
    api.post<RedeemResponse>(
      '/api/v1/auth/redeem',
      { code, username, password },
      { publicEndpoint: true, suppressAuthRedirect: true },
    ),
}

export const adminApi = {
  overview: () => api.get<Overview>(`${ADMIN}/overview`),

  servers: {
    list: (query?: PageQuery) => api.page<ServerRecord>(`${ADMIN}/servers`, { query: pageQuery(query) }),
    get: (id: string) => api.get<ServerRecord>(`${ADMIN}/servers/${encodeURIComponent(id)}`),
    create: (body: ServerWrite) => api.post<ServerRecord>(`${ADMIN}/servers`, body),
    update: (id: string, body: ServerWrite & { version: number }) =>
      api.patch<ServerRecord>(`${ADMIN}/servers/${encodeURIComponent(id)}`, body),
    remove: (id: string, version: number) =>
      api.delete<ServerRecord>(`${ADMIN}/servers/${encodeURIComponent(id)}`, { version }),
    registrationCode: (id: string) =>
      api.post<RegistrationCode>(`${ADMIN}/servers/${encodeURIComponent(id)}/registration-codes`, {}),
  },

  agents: {
    list: (query?: PageQuery) => api.page<AgentRecord>(`${ADMIN}/agents`, { query: pageQuery(query) }),
  },
  coreInstances: {
    list: (query?: PageQuery) =>
      api.page<CoreInstanceRecord>(`${ADMIN}/core-instances`, { query: pageQuery(query) }),
  },

  nodes: {
    list: (query?: PageQuery & { q?: string; server_id?: string }) => api.page<NodeRecord>(`${ADMIN}/nodes`, { query: pageQuery(query) }),
    get: (id: string) => api.get<NodeRecord>(`${ADMIN}/nodes/${encodeURIComponent(id)}`),
    create: (body: NodeWrite) => api.post<NodeRecord>(`${ADMIN}/nodes`, body),
    update: (id: string, body: NodeWrite & { version: number }) =>
      api.patch<NodeRecord>(`${ADMIN}/nodes/${encodeURIComponent(id)}`, body),
    remove: (id: string, version: number) =>
      api.delete<NodeRecord>(`${ADMIN}/nodes/${encodeURIComponent(id)}`, { version }),
  },

  nodeGroups: {
    list: (query?: PageQuery) =>
      api.page<NodeGroupRecord>(`${ADMIN}/node-groups`, { query: pageQuery(query) }),
    get: (id: string) => api.get<NodeGroupRecord>(`${ADMIN}/node-groups/${encodeURIComponent(id)}`),
    create: (body: NodeGroupWrite) => api.post<NodeGroupRecord>(`${ADMIN}/node-groups`, body),
    update: (id: string, body: NodeGroupWrite & { version: number }) =>
      api.patch<NodeGroupRecord>(`${ADMIN}/node-groups/${encodeURIComponent(id)}`, body),
    remove: (id: string, version: number) =>
      api.delete<NodeGroupRecord>(`${ADMIN}/node-groups/${encodeURIComponent(id)}`, { version }),
  },

  subscriptions: {
    list: (query?: PageQuery) =>
      api.page<SubscriptionRecord>(`${ADMIN}/subscriptions`, { query: pageQuery(query) }),
    get: (id: string) => api.get<SubscriptionRecord>(`${ADMIN}/subscriptions/${encodeURIComponent(id)}`),
    create: (body: SubscriptionWrite) => api.post<SubscriptionRecord>(`${ADMIN}/subscriptions`, body),
    update: (id: string, body: SubscriptionWrite & { version: number }) =>
      api.patch<SubscriptionRecord>(`${ADMIN}/subscriptions/${encodeURIComponent(id)}`, body),
    remove: (id: string, version: number) =>
      api.delete<SubscriptionRecord>(`${ADMIN}/subscriptions/${encodeURIComponent(id)}`, { version }),
  },

  members: {
    list: (query?: PageQuery & { q?: string; subscription_id?: string; include_archived?: boolean }) => api.page<MemberRecord>(`${ADMIN}/members`, { query: pageQuery(query) }),
    // 详情端点返回 {member, access}，按服务端派生规则解包成扁平的 MemberRecord。
    get: async (id: string) => memberRecordFromDetail(await api.get<unknown>(`${ADMIN}/members/${encodeURIComponent(id)}`)),
    create: (body: MemberWrite) => api.post<MemberRecord>(`${ADMIN}/members`, body),
    update: (id: string, body: MemberWrite & { version: number }) =>
      api.patch<MemberRecord>(`${ADMIN}/members/${encodeURIComponent(id)}`, body),
    remove: (id: string, version: number) =>
      api.delete<MemberRecord>(`${ADMIN}/members/${encodeURIComponent(id)}`, { version }),
    rotateSubscriptionToken: (id: string) =>
      api.post<TokenRotationResult>(`${ADMIN}/members/${encodeURIComponent(id)}/subscription-token/rotations`, {}),
    rotateCredentials: (id: string, body: CredentialRotationInput) =>
      api.post<Record<string, unknown>>(`${ADMIN}/members/${encodeURIComponent(id)}/credentials/rotations`, body),
    adjustQuota: (id: string, body: QuotaAdjustmentInput) =>
      api.post<Record<string, unknown>>(`${ADMIN}/members/${encodeURIComponent(id)}/quota-adjustments`, body),
  },

  invitations: {
    list: (query?: PageQuery) =>
      api.page<InvitationRecord>(`${ADMIN}/invitations`, { query: pageQuery(query) }),
    create: (body: InvitationCreate) => api.post<InvitationCreated>(`${ADMIN}/invitations`, body),
    revoke: (id: string) => api.post<InvitationRecord>(`${ADMIN}/invitations/${encodeURIComponent(id)}/revoke`, {}),
  },

  batches: {
    preview: (body: {
      operation: BatchOperation
      member_ids: string[]
      values: Record<string, unknown>
      reason?: string
    }) => api.post<BatchPreview>(`${ADMIN}/batches/preview`, body),
    submit: (body: { preview_id: string; rules_version?: unknown; member_set_digest?: unknown; confirm: true }, idempotencyKey: string) =>
      api.post<BatchSubmitResult>(`${ADMIN}/batches`, body, { idempotencyKey }),
    get: (id: string) => api.get<BatchDetail>(`${ADMIN}/batches/${encodeURIComponent(id)}`),
  },

  usage: {
    query: async (query: {
      member_id?: string
      node_id?: string
      from?: string
      to?: string
      limit?: number
      cursor?: string | null
    }): Promise<{ items: UsageItem[]; nic_counters: NicCounter[]; next_cursor: string | null }> => {
      const payload = await api.get<UsagePage>(`${ADMIN}/usage`, { query })
      return {
        items: Array.isArray(payload.items) ? payload.items : [],
        nic_counters: Array.isArray(payload.nic_counters) ? payload.nic_counters : [],
        next_cursor: typeof payload.next_cursor === 'string' ? payload.next_cursor : null,
      }
    },
  },

  events: {
    list: (query?: PageQuery) => api.page<EventRecord>(`${ADMIN}/events`, { query: pageQuery(query) }),
    ack: (id: string) => api.post<EventRecord>(`${ADMIN}/events/${encodeURIComponent(id)}/ack`, {}),
  },

  tasks: {
    list: (query?: PageQuery) => api.page<TaskRecord>(`${ADMIN}/tasks`, { query: pageQuery(query) }),
    retry: (id: string) => api.post<TaskRecord>(`${ADMIN}/tasks/${encodeURIComponent(id)}/retry`, {}),
  },

  auditLogs: {
    list: (query?: PageQuery) => api.page<AuditRecord>(`${ADMIN}/audit-logs`, { query: pageQuery(query) }),
  },

  settings: {
    get: () => api.get<PanelSettings>(`${ADMIN}/settings`),
    update: (body: PanelSettings) => api.put<PanelSettings>(`${ADMIN}/settings`, body),
  },

  templates: {
    list: (query?: PageQuery) => api.page<TemplateRecord>(`${ADMIN}/templates`, { query: pageQuery(query) }),
    get: (id: string) => api.get<TemplateRecord>(`${ADMIN}/templates/${encodeURIComponent(id)}`),
    create: (body: TemplateWrite) => api.post<TemplateRecord>(`${ADMIN}/templates`, body),
    update: (id: string, body: TemplateWrite & { version: number }) =>
      api.patch<TemplateRecord>(`${ADMIN}/templates/${encodeURIComponent(id)}`, body),
  },

  recoveryMode: {
    get: () => api.get<{ recovery_mode: boolean; control_generation: number }>(`${ADMIN}/recovery-mode`),
    enter: () => api.post<Record<string, unknown>>(`${ADMIN}/recovery-mode/enter`, {}),
    // 服务端要求显式核对确认字符串，避免误退出恢复协调模式。
    exit: (checks: { agent_revisions_checked: boolean; ledger_checked: boolean }) => api.post<Record<string, unknown>>(`${ADMIN}/recovery-mode/exit`, { confirm: 'verified', ...checks }),
  },
}

export const meApi = {
  status: () => api.get<MeStatus>(`${ME}/status`),
  nodes: () => api.page<MeNode>(`${ME}/nodes`),
  usage: loadMeUsage,
  subscription: () => api.get<MeSubscription>(`${ME}/subscription`),
  rotateSubscriptionToken: () => api.post<TokenRotationResult>(`${ME}/subscription/token/rotations`, {}),
  rotateCredentials: (body: CredentialRotationInput) =>
    api.post<Record<string, unknown>>(`${ME}/credentials/rotations`, body),
  changePassword: (currentPassword: string, newPassword: string) =>
    api.post<{ updated: boolean; sessions_revoked: number }>(`${ME}/password`, {
      current_password: currentPassword,
      new_password: newPassword,
    }, { suppressAuthRedirect: true }),
  sessions: () => api.page<SessionRecord>(`${ME}/sessions`),
  revokeSession: (id: string) => api.delete<void>(`${ME}/sessions/${encodeURIComponent(id)}`),
}
