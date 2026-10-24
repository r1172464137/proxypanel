import { beforeEach, describe, expect, it, vi } from 'vitest'

const session = vi.hoisted(() => ({ status: 'ready', user: { id: 'admin_test', username: 'admin', role: 'admin' } as { id: string; username: string; role: string } | null }))
vi.mock('@/stores/session', () => ({
  sessionState: session,
  ensureSession: vi.fn(async () => undefined),
  homeRoute: () => session.user?.role === 'admin' ? '/admin/overview' : '/me/subscription',
  logout: vi.fn(), login: vi.fn(), redeem: vi.fn(), clearSession: vi.fn(), markAnonymous: vi.fn(),
}))
import { router } from './index'

beforeEach(() => {
  session.status = 'ready'
  session.user = { id: 'admin_test', username: 'admin', role: 'admin' }
})

describe('approved navigation and role boundaries', () => {
  it('has an actual setup view rather than an overview redirect', async () => {
    await router.push('/admin/setup')
    expect(router.currentRoute.value.name).toBe('admin-setup')
    expect(router.currentRoute.value.path).toBe('/admin/setup')
  })
  it('moves old link/QR bookmarks to the dedicated connection page without dropping query intent', async () => {
    await router.push('/me/subscription?format=xray#link')
    expect(router.currentRoute.value.path).toBe('/me/connect')
    expect(router.currentRoute.value.query).toEqual({ format: 'xray' })
    expect(router.currentRoute.value.hash).toBe('#link')
    await router.push('/me/subscription#qrcode')
    expect(router.currentRoute.value.path).toBe('/me/connect')
    expect(router.currentRoute.value.hash).toBe('#qrcode')
  })
  it.each([
    ['/me/credential', '/me/credentials'],
    ['/admin/invite', '/admin/invitations'],
    ['/admin/usage-detail', '/admin/usage'],
  ])('keeps the old bookmark %s usable', async (old, target) => {
    await router.push(old)
    expect(router.currentRoute.value.path).toBe(target)
  })
  it('does not let a member reach setup through either a new route or a legacy alias', async () => {
    session.user = { id: 'member_test', username: 'member', role: 'member' }
    await router.push('/admin/setup')
    expect(router.currentRoute.value.path).toBe('/me/subscription')
    expect(router.currentRoute.value.query.denied).toBe('/admin/setup')
    await router.push('/admin/invite')
    expect(router.currentRoute.value.path).toBe('/me/subscription')
    expect(router.currentRoute.value.query.denied).toBe('/admin/invitations')
  })
  it('US-PR-001: anonymous root visit lands the admin on the workbench, not the member page', async () => {
    session.status = 'anonymous'
    session.user = null
    await router.push('/')
    expect(router.currentRoute.value.path).toBe('/login')
    expect(router.currentRoute.value.query.next).toBeUndefined()
    // After login the guard applies the role default (no `next` pending).
    session.status = 'ready'
    session.user = { id: 'admin_test', username: 'admin', role: 'admin' }
    await router.push('/admin/members')
    await router.push('/login')
    expect(router.currentRoute.value.path).toBe('/admin/overview')
  })
  it('US-PR-001: anonymous root visit lands a member on 我的服务', async () => {
    session.status = 'anonymous'
    session.user = null
    await router.push('/')
    expect(router.currentRoute.value.path).toBe('/login')
    session.status = 'ready'
    session.user = { id: 'member_test', username: 'member', role: 'member' }
    await router.push('/redeem')
    await router.push('/login')
    expect(router.currentRoute.value.path).toBe('/me/subscription')
  })
  it('retains the actual private destination for an anonymous login', async () => {
    session.status = 'anonymous'
    session.user = null
    await router.push('/me/connect#qrcode')
    expect(router.currentRoute.value.name).toBe('login')
    expect(router.currentRoute.value.query.next).toBe('/me/connect#qrcode')
  })
})
