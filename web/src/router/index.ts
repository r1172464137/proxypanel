import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { ensureSession, homeRoute, sessionState } from '@/stores/session'

const routes: RouteRecordRaw[] = [
  { path: '/', redirect: () => homeRoute() },
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/auth/LoginView.vue'),
    meta: { public: true, title: '登录' },
  },
  {
    path: '/redeem',
    name: 'redeem',
    component: () => import('@/views/auth/RedeemView.vue'),
    meta: { public: true, title: '邀请兑换' },
  },
  {
    path: '/admin/overview',
    name: 'admin-overview',
    component: () => import('@/views/admin/OverviewView.vue'),
    meta: { role: 'admin', title: '总览' },
  },
  {
    path: '/admin/servers',
    name: 'admin-servers',
    component: () => import('@/views/admin/ServersView.vue'),
    meta: { role: 'admin', title: '服务器' },
  },
  {
    path: '/admin/servers/:id/runtime',
    name: 'admin-server-runtime',
    component: () => import('@/views/admin/ServerRuntimeView.vue'),
    meta: { role: 'admin', title: 'Agent 与核心实例' },
  },
  {
    path: '/admin/nodes',
    name: 'admin-nodes',
    component: () => import('@/views/admin/NodesView.vue'),
    meta: { role: 'admin', title: '节点' },
  },
  {
    path: '/admin/nodes/new',
    name: 'admin-node-new',
    component: () => import('@/views/admin/NodeEditorView.vue'),
    meta: { role: 'admin', title: '新建节点' },
  },
  {
    path: '/admin/nodes/:id',
    name: 'admin-node-edit',
    component: () => import('@/views/admin/NodeEditorView.vue'),
    meta: { role: 'admin', title: '编辑节点' },
  },
  {
    path: '/admin/node-groups',
    name: 'admin-node-groups',
    component: () => import('@/views/admin/NodeGroupsView.vue'),
    meta: { role: 'admin', title: '节点组' },
  },
  {
    path: '/admin/subscriptions',
    name: 'admin-subscriptions',
    component: () => import('@/views/admin/SubscriptionsView.vue'),
    meta: { role: 'admin', title: '订阅' },
  },
  {
    path: '/admin/subscriptions/new',
    name: 'admin-subscription-new',
    component: () => import('@/views/admin/SubscriptionEditorView.vue'),
    meta: { role: 'admin', title: '新建订阅' },
  },
  {
    path: '/admin/subscriptions/:id',
    name: 'admin-subscription-edit',
    component: () => import('@/views/admin/SubscriptionEditorView.vue'),
    meta: { role: 'admin', title: '编辑订阅' },
  },
  {
    path: '/admin/members',
    name: 'admin-members',
    component: () => import('@/views/admin/MembersView.vue'),
    meta: { role: 'admin', title: '成员' },
  },
  {
    path: '/admin/members/:id',
    name: 'admin-member-detail',
    component: () => import('@/views/admin/MemberDetailView.vue'),
    meta: { role: 'admin', title: '成员详情' },
  },
  {
    path: '/admin/invitations',
    name: 'admin-invitations',
    component: () => import('@/views/admin/InvitationsView.vue'),
    meta: { role: 'admin', title: '邀请' },
  },
  {
    path: '/admin/templates',
    name: 'admin-templates',
    component: () => import('@/views/admin/TemplatesView.vue'),
    meta: { role: 'admin', title: '模板' },
  },
  {
    path: '/admin/batches',
    name: 'admin-batches',
    component: () => import('@/views/admin/BatchListView.vue'),
    meta: { role: 'admin', title: '批量任务' },
  },
  {
    path: '/admin/batches/:id',
    name: 'admin-batch-detail',
    component: () => import('@/views/admin/BatchDetailView.vue'),
    meta: { role: 'admin', title: '批量任务' },
  },
  {
    path: '/admin/usage',
    name: 'admin-usage',
    component: () => import('@/views/admin/UsageView.vue'),
    meta: { role: 'admin', title: '用量' },
  },
  {
    path: '/admin/events',
    name: 'admin-events',
    component: () => import('@/views/admin/EventsView.vue'),
    meta: { role: 'admin', title: '事件与告警' },
  },
  {
    path: '/admin/tasks',
    name: 'admin-tasks',
    component: () => import('@/views/admin/TasksView.vue'),
    meta: { role: 'admin', title: '任务与同步' },
  },
  {
    path: '/admin/audit',
    name: 'admin-audit',
    component: () => import('@/views/admin/AuditView.vue'),
    meta: { role: 'admin', title: '审计' },
  },
  {
    path: '/admin/settings',
    name: 'admin-settings',
    component: () => import('@/views/admin/SettingsView.vue'),
    meta: { role: 'admin', title: '设置' },
  },
  {
    path: '/me/subscription',
    name: 'me-subscription',
    component: () => import('@/views/me/MeSubscriptionView.vue'),
    meta: { title: '我的订阅' },
  },
  {
    path: '/me/usage',
    name: 'me-usage',
    component: () => import('@/views/me/MeUsageView.vue'),
    meta: { title: '我的用量' },
  },
  {
    path: '/me/credentials',
    name: 'me-credentials',
    component: () => import('@/views/me/MeCredentialsView.vue'),
    meta: { title: '凭据' },
  },
  {
    path: '/me/status',
    name: 'me-status',
    component: () => import('@/views/me/MeStatusView.vue'),
    meta: { title: '状态' },
  },
  {
    path: '/me/security',
    name: 'me-security',
    component: () => import('@/views/me/MeSecurityView.vue'),
    meta: { title: '账号安全' },
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    component: () => import('@/views/NotFoundView.vue'),
    meta: { public: true, title: '页面不存在' },
  },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior: () => ({ top: 0 }),
})

router.beforeEach(async (to) => {
  if (sessionState.status === 'unknown' || sessionState.status === 'loading') {
    await ensureSession()
  }
  const isPublic = to.meta.public === true
  const user = sessionState.user

  if (isPublic) {
    if ((to.name === 'login' || to.name === 'redeem') && user) return homeRoute(user)
    return true
  }
  if (!user) {
    return { name: 'login', query: { next: to.fullPath } }
  }
  if (to.meta.role === 'admin' && user.role !== 'admin') {
    return { name: 'me-subscription', query: { denied: to.fullPath } }
  }
  if (to.meta.role === undefined && user.role === 'admin' && to.path.startsWith('/me')) {
    // 管理员可查看成员端页面（后端仍按自身范围校验），不重定向。
    return true
  }
  return true
})

router.afterEach((to) => {
  const title = typeof to.meta.title === 'string' ? to.meta.title : ''
  document.title = title ? `${title} · 代理面板` : '代理面板'
})