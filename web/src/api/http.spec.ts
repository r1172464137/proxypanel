import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, request, requestPage, setCsrfToken, onUnauthorized } from './http'

interface Captured {
  url: string
  init: RequestInit
}

function jsonResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: async () => JSON.stringify(body),
  } as unknown as Response
}

function rawResponse(status: number, body: string, ok = false): Response {
  return { ok, status, text: async () => body } as unknown as Response
}

function fetcher(handler: (url: string, init: RequestInit) => Response): { fetchImpl: typeof fetch; calls: Captured[] } {
  const calls: Captured[] = []
  const fetchImpl = (async (url: string, init?: RequestInit) => {
    calls.push({ url, init: init ?? {} })
    return handler(url, init ?? {})
  }) as unknown as typeof fetch
  return { fetchImpl, calls }
}

beforeEach(() => {
  setCsrfToken(null)
  onUnauthorized(null)
})

describe('request', () => {
  it('解包 data 信封', async () => {
    const { fetchImpl } = fetcher(() => jsonResponse(200, { data: { id: 'srv_1', version: 3 } }))
    const result = await request<{ id: string; version: number }>('/api/v1/admin/servers/srv_1', { fetchImpl })
    expect(result).toEqual({ id: 'srv_1', version: 3 })
  })

  it('写请求携带 CSRF，GET 不携带', async () => {
    setCsrfToken('csrf-value')
    const { fetchImpl, calls } = fetcher(() => jsonResponse(200, { data: {} }))
    await request('/api/v1/admin/members', { method: 'POST', body: { username: 'a' }, fetchImpl })
    await request('/api/v1/admin/members', { fetchImpl })
    const writeHeaders = calls[0]!.init.headers as Record<string, string>
    const readHeaders = calls[1]!.init.headers as Record<string, string>
    expect(writeHeaders['X-CSRF-Token']).toBe('csrf-value')
    expect(readHeaders['X-CSRF-Token']).toBeUndefined()
    expect(calls[0]!.init.body).toBe(JSON.stringify({ username: 'a' }))
  })

  it('公共端点（登录）不附加 CSRF', async () => {
    setCsrfToken('csrf-value')
    const { fetchImpl, calls } = fetcher(() => jsonResponse(200, { data: { user: {}, csrf_token: 'x' } }))
    await request('/api/v1/auth/login', { method: 'POST', body: {}, publicEndpoint: true, fetchImpl })
    const headers = calls[0]!.init.headers as Record<string, string>
    expect(headers['X-CSRF-Token']).toBeUndefined()
  })

  it('批量提交携带 Idempotency-Key', async () => {
    const { fetchImpl, calls } = fetcher(() => jsonResponse(202, { data: { id: 'bat_1', state: 'accepted', results: [] } }))
    await request('/api/v1/admin/batches', {
      method: 'POST',
      body: { preview_id: 'prv_1', confirm: true },
      idempotencyKey: 'idem-1',
      fetchImpl,
    })
    const headers = calls[0]!.init.headers as Record<string, string>
    expect(headers['Idempotency-Key']).toBe('idem-1')
  })

  it('错误信封转为 ApiError 并保留 code/message/details/retryable', async () => {
    const { fetchImpl } = fetcher(() =>
      jsonResponse(409, {
        error: {
          code: 'PREVIEW_STALE',
          message: '预览已过期，请重新预览',
          details: { changed: ['rules_version'] },
          retryable: true,
          request_id: 'req_9',
        },
      }),
    )
    const caught = await request('/api/v1/admin/batches', { method: 'POST', fetchImpl }).catch((error: unknown) => error)
    expect(caught).toBeInstanceOf(ApiError)
    const error = caught as ApiError
    expect(error.status).toBe(409)
    expect(error.code).toBe('PREVIEW_STALE')
    expect(error.message).toBe('预览已过期，请重新预览')
    expect(error.details).toEqual({ changed: ['rules_version'] })
    expect(error.retryable).toBe(true)
    expect(error.requestId).toBe('req_9')
  })

  it('非契约 HTML 错误页不回显原始 HTML', async () => {
    const { fetchImpl } = fetcher(() => rawResponse(502, '<html><body>Bad Gateway</body></html>'))
    const caught = await request('/api/v1/admin/overview', { fetchImpl }).catch((error: unknown) => error)
    const error = caught as ApiError
    expect(error.code).toBe('PANEL_ERROR')
    expect(error.message).not.toContain('<html')
    expect(error.retryable).toBe(true)
  })

  it('非契约纯文本诊断也不向页面暴露凭据形状正文', async () => {
    const { fetchImpl } = fetcher(() => rawResponse(503, 'PANEL_MASTER_KEY=synthetic-private-diagnostic'))
    const caught = await request('/api/v1/admin/overview', { fetchImpl }).catch((error: unknown) => error)
    expect(caught).toBeInstanceOf(ApiError)
    const error = caught as ApiError
    expect(error.message).not.toContain('synthetic-private')
    expect(error.message).not.toContain('PANEL_MASTER_KEY')
    expect(error.retryable).toBe(true)
  })

  it('网络失败映射为 NETWORK_ERROR 且可重试', async () => {
    const fetchImpl = (async () => {
      throw new TypeError('failed to fetch')
    }) as unknown as typeof fetch
    const caught = await request('/api/v1/admin/overview', { fetchImpl }).catch((error: unknown) => error)
    const error = caught as ApiError
    expect(error.code).toBe('NETWORK_ERROR')
    expect(error.status).toBe(0)
    expect(error.retryable).toBe(true)
  })

  it('204 返回 undefined', async () => {
    const { fetchImpl } = fetcher(() => rawResponse(204, '', true))
    await expect(request('/api/v1/me/sessions/s1', { method: 'DELETE', fetchImpl })).resolves.toBeUndefined()
  })

  it('401 触发全局未登录回调，登录失败不触发', async () => {
    const handler = vi.fn()
    onUnauthorized(handler)
    const unauthorized = fetcher(() => jsonResponse(401, { error: { code: 'UNAUTHENTICATED', message: '未登录' } }))
    await request('/api/v1/admin/overview', { fetchImpl: unauthorized.fetchImpl }).catch(() => undefined)
    expect(handler).toHaveBeenCalledTimes(1)

    const login = fetcher(() => jsonResponse(401, { error: { code: 'UNAUTHENTICATED', message: '用户名或密码错误' } }))
    await request('/api/v1/auth/login', {
      method: 'POST',
      publicEndpoint: true,
      suppressAuthRedirect: true,
      fetchImpl: login.fetchImpl,
    }).catch(() => undefined)
    expect(handler).toHaveBeenCalledTimes(1)
  })
})

describe('requestPage', () => {
  it('解析 items/next_cursor 并保留 null', async () => {
    const { fetchImpl } = fetcher(() => jsonResponse(200, { items: [{ id: 'a' }], next_cursor: null }))
    const page = await requestPage<{ id: string }>('/api/v1/admin/members', { fetchImpl })
    expect(page.items).toHaveLength(1)
    expect(page.next_cursor).toBeNull()
  })

  it('缺少 items 视为结构错误', async () => {
    const { fetchImpl } = fetcher(() => jsonResponse(200, { data: {} }))
    const caught = await requestPage('/api/v1/admin/members', { fetchImpl }).catch((error: unknown) => error)
    expect((caught as ApiError).code).toBe('MALFORMED_RESPONSE')
  })
})