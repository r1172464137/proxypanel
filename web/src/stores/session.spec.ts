import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, getCsrfToken, setCsrfToken } from '@/api/http'
import type { SessionPayload, User } from '@/api/types'

const mocks = vi.hoisted(() => ({
  session: vi.fn(),
  login: vi.fn(),
  logout: vi.fn(),
  redeem: vi.fn(),
}))

vi.mock('@/api/endpoints', () => ({ authApi: mocks }))

import { clearSession, ensureSession, login, markAnonymous, sessionState } from './session'

function user(id: string, role: User['role'] = 'member'): User {
  return { id, username: id, role }
}

function payload(id: string, token: string, role: User['role'] = 'member'): SessionPayload {
  return { user: user(id, role), csrf_token: token }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((done, fail) => {
    resolve = done
    reject = fail
  })
  return { promise, resolve, reject }
}

beforeEach(() => {
  mocks.session.mockReset()
  mocks.login.mockReset()
  mocks.logout.mockReset()
  mocks.redeem.mockReset()
  setCsrfToken(null)
  sessionState.status = 'unknown'
  sessionState.user = null
  sessionState.error = null
})

describe('会话身份 epoch 与请求所有权', () => {
  it('clearSession 后旧的会话读取不能复活身份或 CSRF', async () => {
    const old = deferred<SessionPayload>()
    mocks.session.mockReturnValueOnce(old.promise)

    const pending = ensureSession()
    expect(sessionState.status).toBe('loading')
    clearSession()
    old.resolve(payload('old', 'csrf-old'))
    await pending

    expect(sessionState.status).toBe('anonymous')
    expect(sessionState.user).toBeNull()
    expect(sessionState.error).toBeNull()
    expect(getCsrfToken()).toBeNull()
  })

  it('新登录成功后旧的会话读取不能覆盖新身份与 token', async () => {
    const old = deferred<SessionPayload>()
    mocks.session.mockReturnValueOnce(old.promise)
    mocks.login.mockResolvedValueOnce(payload('new', 'csrf-new', 'admin'))

    const pending = ensureSession()
    await login('new', 'secret')
    old.resolve(payload('old', 'csrf-old'))
    await pending

    expect(sessionState.status).toBe('ready')
    expect(sessionState.user?.id).toBe('new')
    expect(getCsrfToken()).toBe('csrf-new')
  })

  it('clear 后新 ensure 的单飞不会被旧请求 finally 释放', async () => {
    const old = deferred<SessionPayload>()
    const current = deferred<SessionPayload>()
    mocks.session.mockReturnValueOnce(old.promise)

    const stale = ensureSession()
    clearSession()

    mocks.session.mockReturnValueOnce(current.promise)
    const fresh = ensureSession()
    expect(mocks.session).toHaveBeenCalledTimes(2)

    old.resolve(payload('old', 'csrf-old'))
    await stale

    // 旧请求收尾后，新的读取仍在进行中；再次 ensure 必须复用它而不是发第三个请求。
    const joined = ensureSession()
    expect(mocks.session).toHaveBeenCalledTimes(2)

    current.resolve(payload('new', 'csrf-new'))
    await fresh
    await joined

    expect(sessionState.status).toBe('ready')
    expect(sessionState.user?.id).toBe('new')
    expect(getCsrfToken()).toBe('csrf-new')
  })

  it('同身份的 force ensure 仍然合并为一次请求', async () => {
    const first = deferred<SessionPayload>()
    mocks.session.mockReturnValueOnce(first.promise)

    const a = ensureSession()
    const b = ensureSession(true)
    expect(mocks.session).toHaveBeenCalledTimes(1)

    first.resolve(payload('self', 'csrf-self'))
    await Promise.all([a, b])
    expect(sessionState.status).toBe('ready')
    expect(sessionState.user?.id).toBe('self')
  })

  it('401 仍按匿名处理，网络错误仍保留错误反馈', async () => {
    mocks.session.mockRejectedValueOnce(new ApiError(401, { code: 'UNAUTHENTICATED', message: 'unauthenticated' }))
    await ensureSession()
    expect(sessionState.status).toBe('anonymous')
    expect(sessionState.user).toBeNull()
    expect(sessionState.error).toBeNull()

    mocks.session.mockRejectedValueOnce(new ApiError(0, { code: 'NETWORK_ERROR', message: '面板服务不可达' }))
    await ensureSession()
    expect(sessionState.status).toBe('anonymous')
    expect(sessionState.error?.code).toBe('NETWORK_ERROR')
  })

  it('废弃请求的失败不覆盖 clear 后的匿名状态', async () => {
    const old = deferred<SessionPayload>()
    mocks.session.mockReturnValueOnce(old.promise)

    const pending = ensureSession()
    markAnonymous()
    old.reject(new ApiError(500, { code: 'PANEL_ERROR', message: 'boom' }))
    await pending

    expect(sessionState.status).toBe('anonymous')
    expect(sessionState.user).toBeNull()
    expect(sessionState.error).toBeNull()
  })
})
