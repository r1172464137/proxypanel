/** 会话状态：Cookie 由浏览器持有；这里只保存用户与 CSRF 令牌。 */
import { computed, reactive } from 'vue'
import { authApi } from '@/api/endpoints'
import { setCsrfToken } from '@/api/http'
import type { ApiError } from '@/api/http'
import type { User } from '@/api/types'
import { toApiError } from '@/lib/errors'

export type SessionStatus = 'unknown' | 'loading' | 'ready' | 'anonymous'

interface SessionState {
  status: SessionStatus
  user: User | null
  error: ApiError | null
}

export const sessionState = reactive<SessionState>({
  status: 'unknown',
  user: null,
  error: null,
})

let inflight: Promise<void> | null = null

export function clearSession(): void {
  sessionState.status = 'anonymous'
  sessionState.user = null
  sessionState.error = null
  setCsrfToken(null)
}

/**
 * 把会话状态直接标成"未登录"，不发起任何网络请求。
 *
 * 公开页（/login、/redeem）不需要会话信息。以前 App.vue 无条件调
 * ensureSession()，未登录访问公开页时会打 /auth/session 拿到 401，
 * 浏览器把这记进 console（fetch 层无法阻止 4xx 被记录），每个页面 4 条噪音。
 */
export function markAnonymous(): void {
  sessionState.status = 'anonymous'
  sessionState.user = null
  sessionState.error = null
  setCsrfToken(null)
}

export async function ensureSession(force = false): Promise<void> {
  if (!force && sessionState.status === 'ready') return
  if (inflight) return inflight
  sessionState.status = 'loading'
  inflight = (async () => {
    try {
      const payload = await authApi.session()
      sessionState.user = payload.user
      setCsrfToken(payload.csrf_token)
      sessionState.status = 'ready'
      sessionState.error = null
    } catch (caught) {
      const error = toApiError(caught)
      sessionState.user = null
      setCsrfToken(null)
      if (error.status === 401) {
        sessionState.status = 'anonymous'
        sessionState.error = null
      } else {
        sessionState.status = 'anonymous'
        sessionState.error = error
      }
    } finally {
      inflight = null
    }
  })()
  return inflight
}

export async function login(username: string, password: string): Promise<void> {
  const payload = await authApi.login(username, password)
  sessionState.user = payload.user
  setCsrfToken(payload.csrf_token)
  sessionState.status = 'ready'
  sessionState.error = null
}

export async function redeem(code: string, username: string, password: string): Promise<void> {
  await authApi.redeem(code, username, password)
  // 兑换成功后尝试建立会话；失败时由调用方提示“请使用新账号登录”。
  await login(username, password)
}

export async function logout(): Promise<void> {
  try {
    await authApi.logout()
  } finally {
    clearSession()
  }
}

export const isAuthenticated = computed(() => sessionState.status === 'ready' && sessionState.user !== null)
export const isAdmin = computed(() => sessionState.user?.role === 'admin')

export function homeRoute(user: User | null = sessionState.user): string {
  return user?.role === 'admin' ? '/admin/overview' : '/me/subscription'
}