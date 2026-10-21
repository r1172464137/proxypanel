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

const list = usePagedList<EventRecord>((cursor) => adminApi.events.list({ limit: 50, cursor }))
const severity = ref('')
const acking = ref<string | null>(null)

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
  <div>
    <PageHeader title="事件与告警" description="节点异常、证书/服务器到期、上报积压、撤权失败等；确认仅代表已知悉，恢复需服务端再次上报。">
      <button type="button" class="btn" :disabled="list.loading.value" @click="list.refresh()">刷新</button>
    </PageHeader>

    <div class="toolbar">
      <select v-model="severity" class="select" style="max-width: 220px" aria-label="按级别过滤">
        <option value="">全部级别</option>
        <option v-for="value in severities" :key="value" :value="value">{{ value }}</option>
      </select>
      <span class="text-xs text-muted">过滤仅作用于当前页。</span>
    </div>

    <ErrorState v-if="list.error.value" :error="list.error.value" @retry="list.refresh()" />
    <LoadingBlock v-else-if="list.loading.value && !list.items.value.length" label="正在读取事件" />
    <div v-else class="card card__body--flush">
      <div v-if="!list.items.value.length" class="card__body">
        <EmptyState title="没有事件" description="系统异常、到期提醒与撤权失败会在此记录；告警去重与恢复事件由服务端维护。" />
      </div>
      <div v-else-if="!visible.length" class="state-block">
        <p>当前页没有该级别的事件。</p>
      </div>
      <template v-else>
        <ul class="event-list">
          <li v-for="event in visible" :key="event.id" class="event-item">
            <div class="row">
              <StatusBadge :status="event.severity ?? 'unknown'" />
              <strong>{{ event.message ?? event.title ?? event.code ?? event.kind ?? '事件' }}</strong>
              <span class="topbar__spacer" />
              <time class="text-xs text-faint" :title="utcLabel(event.created_at)">{{ formatDateTime(event.created_at) }}</time>
            </div>
            <p v-if="event.state" class="text-sm text-muted">状态：{{ event.state }}<template v-if="event.dedupe_key"> · 去重键：{{ event.dedupe_key }}</template></p>
            <p class="text-xs text-faint">
              对象：{{ event.subject_type ?? '—' }} / <span class="mono">{{ event.subject_id ?? event.id }}</span>
            </p>
            <div class="row">
              <button
                type="button"
                class="btn btn--sm"
                :disabled="isAcked(event) || acking === event.id"
                @click="ack(event)"
              >
                {{ isAcked(event) ? '已确认' : acking === event.id ? '确认中…' : '确认告警' }}
              </button>
              <details>
                <summary class="text-xs text-muted">原始字段</summary>
                <pre class="code-block">{{ prettyJson(event) }}</pre>
              </details>
            </div>
          </li>
        </ul>
        <div class="row row--between" style="padding: var(--space-3) var(--space-4)">
          <span class="text-xs text-muted">第 {{ list.page.value }} 页</span>
          <div class="row">
            <button type="button" class="btn btn--sm" :disabled="list.page.value <= 1 || list.loading.value" @click="list.prev()">上一页</button>
            <button type="button" class="btn btn--sm" :disabled="!list.nextCursor.value || list.loading.value" @click="list.next()">下一页</button>
          </div>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.event-list {
  list-style: none;
  margin: 0;
  padding: 0;
}

.event-item {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-4) var(--space-5);
  border-bottom: 1px solid var(--border);
  min-width: 0;
}

/* 告警原文常含超长不可断 token（校验输出/JSON 片段）：
   作为 flex item 默认 min-width:auto 会按 min-content 撑开，
   在窄屏把整页推到横向溢出；显式允许任意位置折行。 */
.event-item > .row > strong {
  min-width: 0;
  overflow-wrap: anywhere;
}

.event-item:last-child {
  border-bottom: 0;
}
</style>