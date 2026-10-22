<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from './AppIcon.vue'
import ThemeToggle from './ThemeToggle.vue'
import { logout, sessionState } from '@/stores/session'
import type { IconName } from '@/lib/icons'

interface NavLink {
  label: string
  to: string
  icon: IconName
}

interface NavSection {
  title: string
  links: NavLink[]
}

const route = useRoute()
const router = useRouter()
const drawerOpen = ref(false)
const signingOut = ref(false)
const menuButton = ref<HTMLButtonElement | null>(null)

// 账户自身的 5 个视图：成员侧栏与管理员侧栏共用，避免两处各写一份后漂移。
const myAccountLinks: NavSection['links'] = [
  { label: '我的订阅', to: '/me/subscription', icon: 'subscription' },
  { label: '我的用量', to: '/me/usage', icon: 'usage' },
  { label: '凭据', to: '/me/credentials', icon: 'key' },
  { label: '账户状态', to: '/me/status', icon: 'status' },
  { label: '账号安全', to: '/me/security', icon: 'shield' },
]

const adminNav: NavSection[] = [
  { title: '概览', links: [{ label: '总览', to: '/admin/overview', icon: 'overview' }] },
  {
    title: '资源',
    links: [
      { label: '服务器', to: '/admin/servers', icon: 'server' },
      { label: '节点', to: '/admin/nodes', icon: 'node' },
      { label: '节点组', to: '/admin/node-groups', icon: 'groups' },
      { label: '订阅', to: '/admin/subscriptions', icon: 'subscription' },
    ],
  },
  {
    title: '成员',
    links: [
      { label: '成员', to: '/admin/members', icon: 'members' },
      { label: '邀请', to: '/admin/invitations', icon: 'invite' },
    ],
  },
  {
    title: '运行',
    links: [
      { label: '用量', to: '/admin/usage', icon: 'usage' },
      { label: '事件与告警', to: '/admin/events', icon: 'events' },
      { label: '任务与同步', to: '/admin/tasks', icon: 'tasks' },
      { label: '审计', to: '/admin/audit', icon: 'audit' },
    ],
  },
  { title: '系统', links: [{ label: '设置', to: '/admin/settings', icon: 'settings' }] },
  // 管理员看的是自己的账户视图，与上方"成员"（管理他人）是两回事，
  // 所以标题写"我的账户"，并给出全部 5 个入口而不是只给订阅。
  { title: '我的账户', links: [...myAccountLinks] },
]

const memberNav: NavSection[] = [{ title: '我的', links: [...myAccountLinks] }]

const nav = computed(() => (sessionState.user?.role === 'admin' ? adminNav : memberNav))
const title = computed(() => (typeof route.meta.title === 'string' ? route.meta.title : '代理面板'))

watch(
  () => route.fullPath,
  () => {
    drawerOpen.value = false
  },
)

/** 关闭抽屉并把焦点还给触发按钮，键盘用户不会丢焦点。 */
function closeDrawer(): void {
  drawerOpen.value = false
  menuButton.value?.focus()
}

/** 抽屉是临时覆盖层：Escape 关闭即可，不额外引入焦点陷阱。 */
function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape' && drawerOpen.value) closeDrawer()
}

onMounted(() => document.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => document.removeEventListener('keydown', onKeydown))

async function signOut(): Promise<void> {
  signingOut.value = true
  try {
    await logout()
    await router.push({ name: 'login' })
  } finally {
    signingOut.value = false
  }
}
</script>

<template>
  <div class="app-shell">
    <div v-if="drawerOpen" class="sidebar-backdrop" @click="closeDrawer" />
    <aside
      id="app-sidebar"
      class="sidebar"
      :class="{ 'sidebar--open': drawerOpen }"
      aria-label="主导航"
    >
      <div class="sidebar__brand">
        <span class="sidebar__seal" aria-hidden="true">代</span>
        <div class="sidebar__brand-text">
          <strong>代理面板</strong>
          <span>私人合租运维</span>
        </div>
      </div>
      <nav class="sidebar__nav" aria-label="页面导航">
        <template v-for="section in nav" :key="section.title">
          <div class="sidebar__section">{{ section.title }}</div>
          <router-link
            v-for="link in section.links"
            :key="link.to"
            :to="link.to"
            class="sidebar__link"
          >
            <AppIcon :name="link.icon" :size="16" />
            <span>{{ link.label }}</span>
          </router-link>
        </template>
      </nav>
      <div class="sidebar__footer">
        <strong>{{ sessionState.user?.username }}</strong>
        <span>{{ sessionState.user?.role === 'admin' ? '管理员' : '成员' }}</span>
      </div>
    </aside>

    <div class="main">
      <header class="topbar">
        <button
          ref="menuButton"
          type="button"
          class="btn btn--ghost btn--icon hide-desktop"
          aria-label="打开导航"
          aria-controls="app-sidebar"
          :aria-expanded="drawerOpen ? 'true' : 'false'"
          @click="drawerOpen = true"
        >
          <AppIcon name="menu" :size="18" />
        </button>
        <span class="topbar__title">{{ title }}</span>
        <div class="topbar__spacer" />
        <ThemeToggle />
        <div class="topbar__user">
          <span class="badge badge--accent badge--pill hide-sm">{{ sessionState.user?.role === 'admin' ? '管理员' : '成员' }}</span>
          <span class="hide-sm">{{ sessionState.user?.username }}</span>
        </div>
        <button type="button" class="btn btn--sm" :disabled="signingOut" @click="signOut">
          <AppIcon name="logout" :size="15" />
          <span>{{ signingOut ? '退出中…' : '退出登录' }}</span>
        </button>
      </header>
      <main class="content content--wide">
        <slot />
      </main>
    </div>
  </div>
</template>

<style scoped>
.hide-desktop {
  display: none;
}

@media (max-width: 960px) {
  .hide-desktop {
    display: inline-flex;
  }
}
</style>
