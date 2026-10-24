/**
 * 统一 HTTP 客户端。
 *
 * 端口契约（docs/design/implementation-contract.md）：
 * - 成功单对象：{"data": ...}；列表：{"items": [], "next_cursor": null}
 * - 错误：{"error": {"code","message","details","retryable","request_id"}}
 * - 写请求携带 X-CSRF-Token；批量提交携带 Idempotency-Key。
 * 本模块只做传输与信封解析，不含业务字段。
 */

export type HttpMethod = 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE'

export interface ApiErrorPayload {
  code: string
  message: string
  details?: unknown
  retryable?: boolean
  request_id?: string
}

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly details: unknown
  readonly retryable: boolean
  readonly requestId?: string

  constructor(status: number, payload: ApiErrorPayload) {
    super(payload.message)
    this.name = 'ApiError'
    this.status = status
    this.code = payload.code
    this.details = payload.details
    this.retryable = payload.retryable ?? (status >= 500 || status === 429)
    this.requestId = payload.request_id
  }

  /** 前端需要区分“未登录”与其它错误。 */
  get isAuthFailure(): boolean {
    return this.status === 401
  }
}

export interface PageResult<T> {
  items: T[]
  next_cursor: string | null
}

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>

export interface RequestOptions {
  method?: HttpMethod
  body?: unknown
  query?: Record<string, string | number | boolean | null | undefined>
  signal?: AbortSignal
  /** 批量提交等幂等写操作；同键同指纹服务端返回原结果。 */
  idempotencyKey?: string
  headers?: Record<string, string>
  /** 仅测试使用；生产代码不传。 */
  fetchImpl?: FetchLike
  /** 登录/兑换等尚无 CSRF 的公共端点。 */
  publicEndpoint?: boolean
  /** 该请求的 401 不触发全局登出（登录失败由表单自行展示）。 */
  suppressAuthRedirect?: boolean
}

// 与 endpoints.ts 使用同一个基地址判断：配置了 VITE_API_BASE 就是跨域部署，
// 必须用 credentials: 'include'，否则浏览器不会带上会话 Cookie。
const API_BASE = (import.meta.env.VITE_API_BASE ?? '').trim()

const WRITE_METHODS: HttpMethod[] = ['POST', 'PATCH', 'PUT', 'DELETE']

let csrfToken: string | null = null
let unauthorizedHandler: (() => void) | null = null

export function setCsrfToken(token: string | null): void {
  csrfToken = token
}

export function getCsrfToken(): string | null {
  return csrfToken
}

export function onUnauthorized(handler: (() => void) | null): void {
  unauthorizedHandler = handler
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function buildUrl(path: string, query?: RequestOptions['query']): string {
  if (!query) return path
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === '') continue
    params.set(key, String(value))
  }
  const qs = params.toString()
  if (!qs) return path
  return path.includes('?') ? `${path}&${qs}` : `${path}?${qs}`
}

function statusFallback(status: number): ApiErrorPayload {
  if (status === 401) return { code: 'UNAUTHENTICATED', message: '会话已失效，请重新登录' }
  if (status === 403) return { code: 'FORBIDDEN', message: '没有权限执行该操作' }
  if (status === 404) return { code: 'NOT_FOUND', message: '对象不存在或不在可见范围内' }
  if (status === 429) return { code: 'RATE_LIMITED', message: '操作过于频繁，请稍后再试', retryable: true }
  if (status >= 500) return { code: 'PANEL_ERROR', message: `面板服务暂时不可用（HTTP ${status}）`, retryable: true }
  return { code: 'REQUEST_FAILED', message: `请求失败（HTTP ${status}）` }
}

function parseBody(text: string): unknown {
  if (!text) return undefined
  try {
    return JSON.parse(text) as unknown
  } catch {
    return undefined
  }
}

function normalizeError(status: number, payload: unknown): ApiErrorPayload {
  if (isRecord(payload) && isRecord(payload.error)) {
    const error = payload.error
    const code = typeof error.code === 'string' && error.code ? error.code : statusFallback(status).code
    const message =
      typeof error.message === 'string' && error.message.trim()
        ? error.message
        : statusFallback(status).message
    return {
      code,
      message,
      details: error.details,
      retryable: typeof error.retryable === 'boolean' ? error.retryable : undefined,
      request_id: typeof error.request_id === 'string' ? error.request_id : undefined,
    }
  }
  // Uncontracted gateway bodies can include credentials or private diagnostics,
  // whether they are HTML, plain text or arbitrary JSON. Never display them.
  return statusFallback(status)
}

export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = options.method ?? 'GET'
  const headers: Record<string, string> = { Accept: 'application/json', ...options.headers }
  const fetcher: FetchLike = options.fetchImpl ?? ((input, init) => fetch(input, init))

  if (options.body !== undefined) headers['Content-Type'] = 'application/json'
  if (WRITE_METHODS.includes(method) && !options.publicEndpoint && csrfToken) {
    headers['X-CSRF-Token'] = csrfToken
  }
  if (options.idempotencyKey) headers['Idempotency-Key'] = options.idempotencyKey

  let response: Response
  try {
    response = await fetcher(buildUrl(path, options.query), {
      method,
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      credentials: API_BASE ? 'include' : 'same-origin',
      signal: options.signal,
    })
  } catch (caught) {
    if (caught instanceof DOMException && caught.name === 'AbortError') throw caught
    throw new ApiError(0, {
      code: 'NETWORK_ERROR',
      message: '无法连接面板服务，请确认网络或服务状态后重试',
      retryable: true,
    })
  }

  const rawText = response.status === 204 ? '' : await response.text()
  const payload = parseBody(rawText)

  if (!response.ok) {
    if (response.status === 401 && !options.suppressAuthRedirect) unauthorizedHandler?.()
    throw new ApiError(response.status, normalizeError(response.status, payload))
  }

  if (response.status === 204 || rawText === '') return undefined as T
  if (isRecord(payload) && 'data' in payload) return payload.data as T
  return payload as T
}

export async function requestPage<T>(path: string, options: RequestOptions = {}): Promise<PageResult<T>> {
  const payload = await request<unknown>(path, options)
  if (Array.isArray(payload)) return { items: payload as T[], next_cursor: null }
  if (isRecord(payload) && Array.isArray(payload.items)) {
    return {
      items: payload.items as T[],
      next_cursor: typeof payload.next_cursor === 'string' ? payload.next_cursor : null,
    }
  }
  throw new ApiError(0, {
    code: 'MALFORMED_RESPONSE',
    message: '面板返回了无法识别的列表结构',
  })
}

export const api = {
  get: <T>(path: string, options?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...options, method: 'GET' }),
  post: <T>(path: string, body?: unknown, options?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...options, method: 'POST', body }),
  patch: <T>(path: string, body?: unknown, options?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...options, method: 'PATCH', body }),
  put: <T>(path: string, body?: unknown, options?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...options, method: 'PUT', body }),
  delete: <T>(path: string, body?: unknown, options?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...options, method: 'DELETE', body }),
  page: <T>(path: string, options?: Omit<RequestOptions, 'method' | 'body'>) =>
    requestPage<T>(path, { ...options, method: 'GET' }),
}
