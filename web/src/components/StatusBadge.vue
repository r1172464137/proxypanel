<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  status?: string | boolean | null
  label?: string
  tone?: 'ok' | 'warn' | 'danger' | 'info' | 'muted' | 'accent'
}>()

const LABELS: Record<string, string> = {
  active: '正常',
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
  no_subscription: '无订阅',
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

const TONES: Record<string, 'ok' | 'warn' | 'danger' | 'info' | 'muted' | 'accent'> = {
  active: 'ok',
  enabled: 'ok',
  online: 'ok',
  healthy: 'ok',
  succeeded: 'ok',
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

const key = computed(() => (typeof props.status === 'boolean' ? String(props.status) : String(props.status ?? 'unknown')))
const tone = computed(() => props.tone ?? TONES[key.value] ?? 'muted')
const text = computed(() => props.label ?? LABELS[key.value] ?? (props.status === true ? '是' : props.status === false ? '否' : key.value))
</script>

<template>
  <span class="badge" :class="`badge--${tone}`">
    <span class="dot" :class="`dot--${tone === 'muted' ? '' : tone === 'accent' ? 'ok' : tone}`" />
    {{ text }}
  </span>
</template>