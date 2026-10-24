<script setup lang="ts">
import { computed } from 'vue'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'muted' | 'accent'

const props = defineProps<{
  status?: string | boolean | null
  label?: string
  tone?: Tone
}>()

/**
 * 状态 → 文案 / 色调的唯一映射表。
 * 新增后端状态时在这里补充；未知状态回落到 muted，不改变组件接口。
 */
const LABELS: Record<string, string> = {
  active: '正常',
  ok: '正常',
  enabled: '已启用',
  disabled: '已停用',
  maintenance: '维护中',
  online: '在线',
  offline: '离线',
  healthy: '健康',
  unhealthy: '异常',
  unknown: '未知',
  pending: '待下发',
  dispatched: '已下发',
  applying: '应用中',
  succeeded: '已生效',
  applied: '已生效',
  rollback_applied: '回滚运行 · 目标未完成',
  failed: '失败',
  timeout: '超时',
  superseded: '已被取代',
  running: '运行中',
  stopped: '已停止',
  restarting: '重启中',
  expired: '已过期',
  suspended: '已停用',
  archived: '已归档',
  over_quota: '已超额',
  paused_by_admin: '管理员暂停',
  no_subscription: '尚未开通服务',
  node_unavailable: '节点不可用',
  unused: '未使用',
  used: '已使用',
  revoked: '已撤销',
  queued: '排队中',
  accepted: '已受理',
  duplicate: '重复（已去重）',
  conflict: '载荷冲突',
  unverified: '未验证',
  verified: '已验证',
  estimated: '估计值',
  paused: '已暂停',
}

const TONES: Record<string, Tone> = {
  active: 'ok',
  ok: 'ok',
  enabled: 'ok',
  online: 'ok',
  healthy: 'ok',
  succeeded: 'ok',
  applied: 'ok',
  running: 'ok',
  accepted: 'ok',
  verified: 'ok',
  used: 'accent',
  pending: 'info',
  dispatched: 'info',
  applying: 'info',
  restarting: 'info',
  queued: 'info',
  unused: 'info',
  maintenance: 'warn',
  rollback_applied: 'warn',
  paused: 'warn',
  paused_by_admin: 'warn',
  expired: 'warn',
  estimated: 'warn',
  unverified: 'warn',
  offline: 'danger',
  failed: 'danger',
  timeout: 'danger',
  unhealthy: 'danger',
  over_quota: 'danger',
  suspended: 'danger',
  conflict: 'danger',
  superseded: 'muted',
  stopped: 'muted',
  archived: 'muted',
  revoked: 'muted',
  disabled: 'muted',
  no_subscription: 'muted',
  node_unavailable: 'muted',
  unknown: 'muted',
  duplicate: 'muted',
}

const key = computed(() => {
  if (typeof props.status === 'boolean') return props.status ? 'true' : 'false'
  return String(props.status ?? 'unknown')
})

const tone = computed<Tone>(() => {
  if (props.tone) return props.tone
  if (typeof props.status === 'boolean') return props.status ? 'ok' : 'danger'
  return TONES[key.value] ?? 'muted'
})

const text = computed(() => {
  if (props.label) return props.label
  if (typeof props.status === 'boolean') return props.status ? '是' : '否'
  return LABELS[key.value] ?? key.value
})

const variant = computed(() => {
  if (tone.value === 'danger') return 'destructive' as const
  if (tone.value === 'muted') return 'secondary' as const
  return 'outline' as const
})

const toneClass = computed(() => {
  switch (tone.value) {
    case 'ok':
      return 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950 dark:text-emerald-300'
    case 'warn':
      return 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-300'
    case 'info':
      return 'border-sky-200 bg-sky-50 text-sky-700 dark:border-sky-900 dark:bg-sky-950 dark:text-sky-300'
    case 'accent':
      return 'border-transparent bg-brand text-brand-foreground hover:bg-brand/90'
    default:
      return ''
  }
})
</script>

<template>
  <Badge :variant="variant" :class="cn('gap-1.5 whitespace-nowrap', toneClass)">
    <span class="size-1.5 shrink-0 rounded-full bg-current" aria-hidden="true" />
    <span>{{ text }}</span>
  </Badge>
</template>
