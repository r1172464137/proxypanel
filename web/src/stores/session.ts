/** 会话状态：Cookie 由浏览器持有；这里只保存用户与 CSRF 令牌。 */
import { computed, reactive } from 'vue'
import { authApi } from '@/api/endpoints'
import { setCsrfToken } from '@/api/http'
import type { ApiError } from '@/api/http'
import type { SessionPayload, User } from '@/api/types'
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

let inflight: { epoch: number; promise: Promise<void> } | null = null
// 身份 epoch：clear/markAnonymous/login 成功会推进 epoch，使已发出的只读会话请求失效。
let identityEpoch = 0

function invalidatePendingSession(): void {
  identityEpoch += 1
  inflight = null
}

export function clearSession(): void {
  invalidatePendingSession()
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
  invalidatePendingSession()
  sessionState.status = 'anonymous'
  sessionState.user = null
  sessionState.error = null
  setCsrfToken(null)
}

export async function ensureSession(force = false): Promise<void> {
  if (!force && sessionState.status === 'ready') return
  if (inflight && inflight.epoch === identityEpoch) return inflight.promise
  const epoch = identityEpoch
  sessionState.status = 'loading'
  let started: Promise<SessionPayload>
  try {
    started = Promise.resolve(authApi.session())
  } catch (caught) {
    started = Promise.reject(caught)
  }
  const entry = { epoch, promise: Promise.resolve() }
  entry.promise = (async () => {
    try {
      const payload = await started
      if (epoch !== identityEpoch) return
      sessionState.user = payload.user
      setCsrfToken(payload.csrf_token)
      sessionState.status = 'ready'
      sessionState.error = null
    } catch (caught) {
      // 已被 clear/markAnonymous/login 取代的读取不得回写旧身份或错误。
      if (epoch !== identityEpoch) return
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
      // 只释放自己登记的 singleflight，避免旧请求清掉 clear 后新发起的读取。
      if (inflight === entry) inflight = null
    }
  })()
  inflight = entry
  return entry.promise
}

export async function login(username: string, password: string): Promise<void> {
  const payload = await authApi.login(username, password)
  // 登录成功即身份切换：此前的只读会话请求一律作废，不能再回写旧 user/CSRF。
  invalidatePendingSession()
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