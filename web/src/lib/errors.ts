/** 错误归类与文案。服务端 message 一律按纯文本展示（不使用 v-html）。 */
import { ApiError } from '@/api/http'

export function isApiError(value: unknown): value is ApiError {
  return value instanceof ApiError
}

export function toApiError(value: unknown): ApiError {
  if (value instanceof ApiError) return value
  if (value instanceof DOMException && value.name === 'AbortError') {
    return new ApiError(0, { code: 'ABORTED', message: '请求已取消' })
  }
  if (value instanceof Error) {
    return new ApiError(0, { code: 'CLIENT_ERROR', message: value.message || '客户端错误' })
  }
  return new ApiError(0, { code: 'UNKNOWN', message: '未知错误' })
}

export function errorCode(value: unknown): string | undefined {
  return value instanceof ApiError ? value.code : undefined
}

/** 展示用文案：优先服务端 message；网络/解析失败有明确中文兜底。 */
export function errorText(value: unknown): string {
  const error = toApiError(value)
  if (!error.message) return '操作失败，请重试'
  return error.message
}

export function isRetryable(value: unknown): boolean {
  return value instanceof ApiError ? value.retryable : false
}

const HINTS: Record<string, string> = {
  PREVIEW_REQUIRED: '该修改影响已有成员授权，必须先生成预览并确认，不能直接保存。',
  PREVIEW_STALE: '预览已过期或规则已变化，请重新生成预览。',
  PREVIEW_CONCLUSION_CHANGED: '提交时动态值变化导致结论改变（超额/失去权限），需要重新预览确认。',
  IDEMPOTENCY_KEY_REUSED: '该幂等键已用于不同内容的请求，服务端拒绝重复提交。',
  LAST_ADMIN: '该操作会移除最后一名可用管理员，已阻止。',
  PROTOCOL_NOT_SUPPORTED: '该协议/传输/安全组合不在已验证矩阵内。',
  NODE_IN_USE: '对象仍被其它资源引用（订阅/节点组/成员），需先解除引用。',
  DB_UNAVAILABLE: '数据库不可用，写入未被确认；这不是成功，可稍后重试。',
  RATE_LIMITED: '请求被限流，请等待后重试。',
  CSRF_FAILED: 'CSRF 校验失败，可能登录状态已变化；请刷新页面。',
  UNAUTHENTICATED: '会话已失效，请重新登录。',
  FORBIDDEN: '当前账号没有执行该操作的权限。',
  NOT_FOUND: '对象不存在或不在可见范围内。',
  VALIDATION_FAILED: '提交内容未通过服务端校验，请按提示修正字段。',
  NETWORK_ERROR: '面板服务不可达；请确认面板已启动且网络可达。',
  MALFORMED_RESPONSE: '面板返回了非契约结构，可能是版本不匹配。',
  FORMAT_UNSUPPORTED: '该客户端格式无法表示当前节点参数；服务端会说明不兼容的节点/字段。',
  UNSUPPORTED_FORMAT: '该客户端格式无法表示当前节点参数；服务端会说明不兼容的节点/字段。',
  RECOVERY_MODE: '面板处于恢复核对中，普通授权变更暂不可用；账号救援仍可用。',
  READINESS_BLOCKED: '升级预检已阻断，请先核对列出的资料或历史问题，不要强行继续。',
  LEDGER_UNATTRIBUTABLE: '用量区间暂时无法核对，系统不会自动补算或改变历史，请联系管理员处理。',
  CURSOR_INVALID: '列表已过期，请回到第一页重新读取并核对选择范围。',
  SUBSCRIPTION_NOT_BINDABLE: '此服务方案暂不可分配，请选择其他方案或先核对方案状态。',
}

export function errorHint(value: unknown): string | undefined {
  const code = errorCode(value)
  return code ? HINTS[code] : undefined
}

export function errorTitle(value: unknown): string {
  const error = toApiError(value)
  if (error.code === 'NETWORK_ERROR') return '无法连接面板'
  if (error.code === 'CURSOR_INVALID') return '列表已过期'
  if (error.code === 'READINESS_BLOCKED') return '升级预检已阻断'
  if (error.code === 'LEDGER_UNATTRIBUTABLE') return '用量资料需核对'
  if (error.code === 'SUBSCRIPTION_NOT_BINDABLE') return '服务方案不可分配'
  if (error.status === 401) return '未登录或会话失效'
  if (error.status === 403) return '权限不足'
  if (error.status === 404) return '对象不存在'
  if (error.status === 409) return '存在冲突'
  if (error.status === 422) return error.code === 'PROTOCOL_NOT_SUPPORTED' || error.code === 'FORMAT_UNSUPPORTED' || error.code === 'UNSUPPORTED_FORMAT'
    ? '参数无法被该客户端格式表示' : '请求内容需核对'
  if (error.status === 429) return '请求过于频繁'
  if (error.status >= 500) return '面板或依赖服务故障'
  if (error.status === 400) return '提交内容有误'
  return '操作失败'
}

export interface ErrorView {
  title: string
  message: string
  hint?: string
  code?: string
  requestId?: string
  retryable: boolean
  status: number
}

export function describeError(value: unknown): ErrorView {
  const error = toApiError(value)
  return {
    title: errorTitle(error),
    message: errorText(error),
    hint: errorHint(error),
    code: error.code,
    requestId: error.requestId,
    retryable: error.retryable,
    status: error.status,
  }
}

/**
 * 从 ApiError.details 里取出字段级错误。键是后端字段名，值是消息。
 *
 * 后端在 `VALIDATION_FAILED` 上用 `details.field_errors` 返回字段级原因
 * （例如 `client_settings.server_name`）。这里只做形状收敛：非字符串值转成
 * 字符串保留（宁可显示得粗糙，也不静默丢掉服务端给出的字段），非对象返回空表。
 */
export function fieldErrorsOf(error: unknown): Record<string, string> {
  if (!(error instanceof ApiError)) return {}
  const details = error.details
  if (typeof details !== 'object' || details === null || Array.isArray(details)) return {}
  const raw = (details as Record<string, unknown>).field_errors
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) return {}
  const fields: Record<string, string> = {}
  for (const [field, value] of Object.entries(raw as Record<string, unknown>)) {
    if (typeof value === 'string') {
      if (value.trim()) fields[field] = value
      continue
    }
    if (value === null || value === undefined) continue
    const text = String(value)
    if (text.trim()) fields[field] = text
  }
  return fields
}

/** 是否需要请求预览（409 PREVIEW_REQUIRED 或 details.preview_required）。 */
export function isPreviewRequired(value: unknown): boolean {
  if (!(value instanceof ApiError)) return false
  if (value.code === 'PREVIEW_REQUIRED') return true
  if (value.status === 409 && value.details && typeof value.details === 'object') {
    const details = value.details as Record<string, unknown>
    return details.preview_required === true
  }
  return false
}