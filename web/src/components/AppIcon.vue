<script setup lang="ts">
import { computed, type Component } from 'vue'
import {
  Activity,
  ArrowLeft,
  BarChart3,
  Bell,
  Bookmark,
  Boxes,
  Check,
  ChevronDown,
  ChevronRight,
  Copy,
  Eye,
  EyeOff,
  Inbox,
  Info,
  KeyRound,
  Layers,
  LayoutDashboard,
  ListChecks,
  LogOut,
  Menu,
  Moon,
  Plus,
  RefreshCw,
  Rss,
  ScrollText,
  Search,
  Server,
  Settings,
  ShieldCheck,
  Sun,
  TriangleAlert,
  UserPlus,
  Users,
  X,
} from 'lucide-vue-next'
import type { IconName } from '@/lib/icons'

/**
 * 唯一图标出口：把 `src/lib/icons.ts` 的语义名映射到 lucide 图标。
 *
 * 调用方只依赖语义名，不关心具体图标库；以后换图标只需改这张表。
 * 纯装饰时对辅助技术隐藏；需要语义时传 label（例如仅图标按钮），
 * 但按钮本身仍应保留 aria-label 或可见文字。
 */
const ICONS: Record<IconName, Component> = {
  overview: LayoutDashboard,
  server: Server,
  node: Boxes,
  groups: Layers,
  subscription: Rss,
  members: Users,
  invite: UserPlus,
  usage: BarChart3,
  events: Bell,
  tasks: ListChecks,
  audit: ScrollText,
  settings: Settings,
  key: KeyRound,
  status: Activity,
  shield: ShieldCheck,
  bookmark: Bookmark,
  menu: Menu,
  close: X,
  logout: LogOut,
  sun: Sun,
  moon: Moon,
  refresh: RefreshCw,
  copy: Copy,
  check: Check,
  eye: Eye,
  eyeOff: EyeOff,
  empty: Inbox,
  alert: TriangleAlert,
  search: Search,
  plus: Plus,
  chevronDown: ChevronDown,
  chevronRight: ChevronRight,
  arrowLeft: ArrowLeft,
  info: Info,
}

const props = withDefaults(
  defineProps<{
    name: IconName
    size?: number
    strokeWidth?: number
    label?: string
  }>(),
  { size: 18, strokeWidth: 1.6, label: '' },
)

const icon = computed(() => ICONS[props.name])
</script>

<template>
  <component
    :is="icon"
    class="app-icon"
    :size="size"
    :stroke-width="strokeWidth"
    :role="label ? 'img' : undefined"
    :aria-label="label || undefined"
    :aria-hidden="label ? undefined : 'true'"
    focusable="false"
  />
</template>

<style scoped>
.app-icon {
  flex: none;
  display: inline-block;
  vertical-align: -0.18em;
}
</style>
