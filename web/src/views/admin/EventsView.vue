<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { adminApi } from '@/api/endpoints'
import type { EventRecord, MemberRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText } from '@/lib/errors'
import { formatDateTime, prettyJson, utcLabel } from '@/lib/format'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'

const list = usePagedList<EventRecord>((cursor) => adminApi.events.list({ limit: 50, cursor }))
const severity = ref('')
const acking = ref<string | null>(null)

// 后端 severity 的取值固定为 info / warning / error；中文只在前端翻译。
const SEVERITY_OPTIONS = [
  { value: 'info', label: '提示' },
  { value: 'warning', label: '警告' },
  { value: 'error', label: '严重' },
]
const SEVERITY_LABELS: Record<string, string> = { info: '提示', warning: '警告', error: '严重' }
const SEVERITY_TONES: Record<string, 'info' | 'warn' | 'danger'> = {
  info: 'info',
  warning: 'warn',
  error: 'danger',
}
const STATE_LABELS: Record<string, string> = { open: '未处理', acked: '已确认', recovered: '已恢复' }
// 对象类型的中文说法；未登记的类型统一叫「其他对象」，不直接暴露内部码。
const SUBJECT_LABELS: Record<string, string> = {
  member: '成员',
  agent: 'Agent',
  server: '服务器',
  node: '节点',
  core_instance: '核心实例',
}
const KIND_LABELS: Record<string, string> = {
  member_over_quota: '成员流量超额',
  agent_offline: 'Agent 离线',
  core_instance_failed: '核心实例启动失败',
  member_expired: '成员到期',
  server_expiring: '服务器到期提醒',
}
const RECOVERY_LABELS: Record<string, string> = {
  member_over_quota: '成员的额度已恢复',
  agent_offline: 'Agent 重新上线',
  core_instance_failed: '核心实例恢复正常',
  member_expired: '成员到期状态已更新',
  server_expiring: '服务器到期提醒已解除',
}

// Select 需要非空值：空串（全部级别）用一个哨兵值表示，事件筛选逻辑仍读 severity。
const ALL = '__all__'
const severityFilter = computed({
  get: () => severity.value || ALL,
  set: (value: string) => {
    severity.value = value === ALL ? '' : value
  },
})

// 只用于把 mbr_xxx 变成「成员「labuser」」；拿不到就退回显示 ID，不影响事件列表本身。
const members = ref<MemberRecord[]>([])

onMounted(async () => {
  void list.refresh()
  try {
    members.value = (await adminApi.members.list({ limit: 200 })).items
  } catch {
    members.value = []
  }
})

const visible = computed(() => {
  if (!severity.value) return list.items.value
  return list.items.value.filter((event) => severityOf(event) === severity.value)
})

const memberNames = computed(() => {
  const map = new Map<string, string>()
  for (const member of members.value) map.set(member.id, member.username)
  return map
})

function severityOf(event: EventRecord): string {
  return String(event.severity ?? event.kind ?? '').toLowerCase()
}

function severityLabel(event: EventRecord): string {
  return SEVERITY_LABELS[severityOf(event)] ?? '未知级别'
}

function severityTone(event: EventRecord): 'info' | 'warn' | 'danger' | 'muted' {
  return SEVERITY_TONES[severityOf(event)] ?? 'muted'
}

function stateLabel(state: string): string {
  return STATE_LABELS[state] ?? '其他状态'
}

function kindText(event: EventRecord): string {
  return KIND_LABELS[String(event.kind ?? '')] ?? '运维事件'
}

/** 对象描述：中文类型 + 可读名称（成员优先用用户名），拿不到名称才显示 ID。 */
function subjectText(event: EventRecord): string {
  const type = String(event.subject_type ?? '').trim()
  const id = String(event.subject_id ?? '').trim()
  const label = SUBJECT_LABELS[type] ?? (type ? '其他对象' : '未注明')
  if (type === 'member' && id) {
    const name = memberNames.value.get(id)
    if (name) return `${label}「${name}」`
  }
  if (!id) return label
  return `${label}（${id}）`
}

/**
 * 事件正文。
 *
 * 后端已把消息改成中文（「【异常】…」/「【已恢复】…」），中文正文原样展示。
 * 数据库里还留着改造前写入的英文消息（"[proxypanel] RECOVERED over_quota: …"），
 * 这些记录改不了后端，所以在这里按同一套中文口径回显；原始英文仍可在
 * 「原始数据」里查到，信息不丢。
 */
function eventMessage(event: EventRecord): string {
  const raw = String(event.message ?? event.title ?? '').trim()
  const subject = subjectText(event)
  if (!raw) return `${kindText(event)}：${subject}`
  if (/[\u4e00-\u9fff]/.test(raw)) {
    // 后端中文消息末尾会自带一行「对象：…」；下方已单独展示对象，去掉重复的一行。
    return raw.replace(/\n\s*对象：[^\n]*\s*$/, '').trim()
  }

  let text = raw.replace(/^\[proxypanel\]\s*/i, '')
  if (/^RECOVERED\s+[\w-]+:/i.test(text)) {
    const reason = RECOVERY_LABELS[String(event.kind ?? '')] ?? '状态已恢复'
    return `【已恢复】${reason}：${subject}`
  }
  const alert = /^ALERT\s+[\w-]+:\s*(.+)$/i.exec(text)
  if (alert) text = alert[1].trim()

  const quota = /^member(?:\s+\S+)?\s+reached the effective quota/i.exec(text)
  if (quota) return `【异常】${subject}已用满本期额度`
  const expired = /^member\s+\S+\s+binding expired at\s+(.+)$/i.exec(text)
  if (expired) return `【异常】${subject}的订阅已于 ${expired[1].trim()} 到期`
  const offline = /^agent\s+"?([^"]+)"?\s+has been offline for\s+(.+?)\s+\(last heartbeat\s+(.+?)\)$/i.exec(text)
  if (offline) return `【异常】Agent「${offline[1]}」已离线 ${offline[2]}（最后心跳 ${offline[3]}）`
  const never = /^agent\s+"?([^"]+)"?\s+has not sent a heartbeat since registration/i.exec(text)
  if (never) return `【异常】Agent「${never[1]}」注册后一直没有心跳`
  const instance = /^core instance for node\s+"?([^"]+)"?\s+is in state failed(?::\s*(.*))?$/i.exec(text)
  if (instance) {
    const detail = instance[2]?.trim()
    return `【异常】节点「${instance[1]}」的核心实例启动失败${detail ? `：${detail}` : ''}`
  }
  const server = /^server\s+"?([^"]+)"?\s+(expired|expires)/i.exec(text)
  if (server) return `【异常】服务器「${server[1]}」到期提醒`

  return `${kindText(event)}：${subject}（原文见「原始数据」）`
}

function isAcked(event: EventRecord): boolean {
  return Boolean(event.acknowledged_at ?? event.acked_at)
}

async function ack(event: EventRecord): Promise<void> {
  acking.value = event.id
  try {
    await adminApi.events.ack(event.id)
    pushToast('success', '告警已确认', '确认只表示已知悉，不等于故障已恢复。')
    await list.refresh()
  } catch (caught) {
    pushToast('error', '确认失败', errorText(caught))
  } finally {
    acking.value = null
  }
}
</script>

<template>
  <div class="space-y-4">
    <PageHeader
      title="事件与告警"
      description="这里记录节点异常、到期提醒、上报积压等问题。点「确认」只是标记你已看到，问题本身要等服务端再次上报恢复才会消失。"
    >
      <Button variant="outline" :disabled="list.loading.value" @click="list.refresh()">刷新</Button>
    </PageHeader>

    <div class="flex flex-wrap items-center gap-3">
      <Select v-model="severityFilter">
        <SelectTrigger class="w-full max-w-[220px]" aria-label="按级别过滤">
          <SelectValue placeholder="全部级别" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem :value="ALL">全部级别</SelectItem>
          <SelectItem v-for="option in SEVERITY_OPTIONS" :key="option.value" :value="option.value">
            {{ option.label }}
          </SelectItem>
        </SelectContent>
      </Select>
      <span class="text-xs text-muted-foreground">过滤仅作用于当前页。</span>
    </div>

    <ErrorState v-if="list.error.value" :error="list.error.value" @retry="list.refresh()" />
    <LoadingBlock v-else-if="list.loading.value && !list.items.value.length" label="正在读取事件" />
    <Card v-else class="overflow-hidden">
      <div v-if="!list.items.value.length" class="p-6">
        <EmptyState
          title="没有事件"
          description="系统异常、到期提醒与撤权失败会在此记录；告警去重与恢复事件由服务端维护。"
        />
      </div>
      <div v-else-if="!visible.length" class="p-6 text-sm text-muted-foreground">
        <p>当前页没有该级别的事件。</p>
      </div>
      <template v-else>
        <ul class="divide-y divide-border">
          <li
            v-for="event in visible"
            :key="event.id"
            class="flex min-w-0 flex-col gap-2 p-4 sm:p-5"
          >
            <div class="flex flex-wrap items-center gap-2">
              <StatusBadge :status="severityOf(event)" :label="severityLabel(event)" :tone="severityTone(event)" />
              <strong class="min-w-0 break-words whitespace-pre-line [overflow-wrap:anywhere]">
                {{ eventMessage(event) }}
              </strong>
              <time class="ml-auto text-xs text-muted-foreground" :title="utcLabel(event.created_at)">
                {{ formatDateTime(event.created_at) }}
              </time>
            </div>
            <p v-if="event.state" class="text-sm text-muted-foreground">
              状态：{{ stateLabel(String(event.state)) }}
            </p>
            <p class="text-xs text-muted-foreground">对象：{{ subjectText(event) }}</p>
            <details v-if="event.dedupe_key" class="text-xs text-muted-foreground">
              <summary class="cursor-pointer select-none">去重键</summary>
              <code class="mt-1 block break-all font-mono">{{ event.dedupe_key }}</code>
            </details>
            <div class="flex flex-wrap items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                :disabled="isAcked(event) || acking === event.id"
                @click="ack(event)"
              >
                {{ isAcked(event) ? '已确认' : acking === event.id ? '确认中…' : '确认告警' }}
              </Button>
              <Collapsible>
                <CollapsibleTrigger as-child>
                  <Button variant="ghost" size="sm" class="text-muted-foreground">原始数据</Button>
                </CollapsibleTrigger>
                <CollapsibleContent>
                  <pre class="mt-2 max-h-80 overflow-auto rounded-md border border-border bg-muted p-3 font-mono text-xs whitespace-pre-wrap break-all">{{ prettyJson(event) }}</pre>
                </CollapsibleContent>
              </Collapsible>
            </div>
          </li>
        </ul>
        <div class="flex flex-wrap items-center justify-between gap-2 border-t border-border px-4 py-3">
          <span class="text-xs text-muted-foreground">第 {{ list.page.value }} 页</span>
          <div class="flex items-center gap-2">
            <Button variant="outline" size="sm" :disabled="list.page.value <= 1 || list.loading.value" @click="list.prev()">上一页</Button>
            <Button variant="outline" size="sm" :disabled="!list.nextCursor.value || list.loading.value" @click="list.next()">下一页</Button>
          </div>
        </div>
      </template>
    </Card>
  </div>
</template>
