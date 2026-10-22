<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { adminApi } from '@/api/endpoints'
import type { EventRecord } from '@/api/types'
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

// Select 需要非空值：空串（全部级别）用一个哨兵值表示，事件筛选逻辑仍读 severity。
const ALL = '__all__'
const severityFilter = computed({
  get: () => severity.value || ALL,
  set: (value: string) => {
    severity.value = value === ALL ? '' : value
  },
})

onMounted(() => void list.refresh())

const visible = computed(() => {
  if (!severity.value) return list.items.value
  return list.items.value.filter((event) => (event.severity ?? event.kind ?? '') === severity.value)
})

const severities = computed(() => {
  const values = new Set<string>()
  for (const event of list.items.value) {
    const value = event.severity ?? event.kind
    if (value) values.add(String(value))
  }
  return [...values]
})

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
          <SelectItem v-for="value in severities" :key="value" :value="value">{{ value }}</SelectItem>
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
              <StatusBadge :status="event.severity ?? 'unknown'" />
              <strong class="min-w-0 break-words [overflow-wrap:anywhere]">
                {{ event.message ?? event.title ?? event.code ?? event.kind ?? '事件' }}
              </strong>
              <time class="ml-auto text-xs text-muted-foreground" :title="utcLabel(event.created_at)">
                {{ formatDateTime(event.created_at) }}
              </time>
            </div>
            <p v-if="event.state" class="text-sm text-muted-foreground">
              状态：{{ event.state }}<template v-if="event.dedupe_key"> · 去重键：{{ event.dedupe_key }}</template>
            </p>
            <p class="text-xs text-muted-foreground">
              对象：{{ event.subject_type ?? '—' }} /
              <span class="font-mono">{{ event.subject_id ?? event.id }}</span>
            </p>
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
                  <Button variant="ghost" size="sm" class="text-muted-foreground">原始字段</Button>
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
