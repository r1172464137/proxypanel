<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { adminApi } from '@/api/endpoints'
import type { TaskRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText } from '@/lib/errors'
import { formatDateTime, prettyJson, utcLabel } from '@/lib/format'
import { usePolling } from '@/lib/async'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import AppModal from '@/components/AppModal.vue'

const list = usePagedList<TaskRecord>((cursor) => adminApi.tasks.list({ limit: 50, cursor }))
const retryTarget = ref<TaskRecord | null>(null)
const retrying = ref(false)
const retryError = ref('')
const autoRefresh = ref(false)

const poller = usePolling(() => list.refresh(), 10000)

onMounted(() => void list.refresh())
onBeforeUnmount(() => poller.stop())

function toggleAuto(): void {
  autoRefresh.value = !autoRefresh.value
  if (autoRefresh.value) poller.start()
  else poller.stop()
}

function errorOf(task: TaskRecord): string {
  if (task.last_error) return task.last_error
  const error = task.error
  if (!error) return '—'
  if (typeof error === 'string') return error
  return `${error.code ?? ''} ${error.message ?? ''}`.trim() || '—'
}

async function retry(): Promise<void> {
  const target = retryTarget.value
  if (!target) return
  retrying.value = true
  retryError.value = ''
  try {
    await adminApi.tasks.retry(target.id)
    pushToast('success', '重试已受理', '重试受版本检查限制，旧任务不会覆盖更新后的规则。')
    retryTarget.value = null
    await list.refresh()
  } catch (caught) {
    retryError.value = errorText(caught)
  } finally {
    retrying.value = false
  }
}
</script>

<template>
  <div>
    <PageHeader title="任务与同步" description="你改的配置要送到节点上才算真的生效。这里看每一条走到哪一步了；标红的可以点重试。">
      <button type="button" class="btn" :disabled="list.loading.value" @click="list.refresh()">刷新</button>
      <button type="button" class="btn" :class="{ 'btn--primary': autoRefresh }" @click="toggleAuto">
        {{ autoRefresh ? '停止自动刷新' : '自动刷新（10s）' }}
      </button>
    </PageHeader>

    <ErrorState v-if="list.error.value" :error="list.error.value" @retry="list.refresh()" />
    <LoadingBlock v-else-if="list.loading.value && !list.items.value.length" label="正在读取任务" />
    <div v-else class="card card__body--flush">
      <div v-if="!list.items.value.length" class="card__body">
        <EmptyState title="没有任务" description="规则变更或凭据轮换会生成下发任务；Agent 离线时任务会保持待下发。" />
      </div>
      <template v-else>
        <div class="table-wrap">
          <table class="table">
            <thead>
              <tr>
                <th>任务</th>
                <th>状态</th>
                <th>节点 / Agent</th>
                <th class="hide-sm">尝试</th>
                <th>错误</th>
                <th class="hide-sm">时间</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="task in list.items.value" :key="task.id">
                <td class="mono">{{ task.id }}</td>
                <td><StatusBadge :status="task.state ?? task.status ?? 'unknown'" /></td>
                <td class="text-xs">
                  <span class="mono">{{ task.node_id ?? '—' }}</span>
                  <span class="cell-sub mono">{{ task.agent_id ?? task.target_agent_id ?? '—' }}</span>
                </td>
                <td class="hide-sm num">{{ task.attempts ?? task.retry_count ?? '—' }}</td>
                <td class="text-sm break-all">{{ errorOf(task) }}</td>
                <td class="hide-sm text-xs">
                  <span :title="utcLabel(task.created_at)">{{ formatDateTime(task.created_at) }}</span>
                  <span v-if="task.applied_at ?? task.updated_at" class="cell-sub">回执/更新 {{ formatDateTime(task.applied_at ?? task.updated_at) }}</span>
                </td>
                <td class="actions">
                  <button
                    type="button"
                    class="btn btn--sm"
                    :disabled="!['failed', 'timeout'].includes(String(task.state ?? task.status))"
                    @click="retryTarget = task"
                  >
                    重试
                  </button>
                  <details>
                    <summary class="text-xs text-muted">详情</summary>
                    <pre class="code-block">{{ prettyJson(task) }}</pre>
                  </details>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="row row--between" style="padding: var(--space-3) var(--space-4)">
          <span class="text-xs text-muted">第 {{ list.page.value }} 页</span>
          <div class="row">
            <button type="button" class="btn btn--sm" :disabled="list.page.value <= 1 || list.loading.value" @click="list.prev()">上一页</button>
            <button type="button" class="btn btn--sm" :disabled="!list.nextCursor.value || list.loading.value" @click="list.next()">下一页</button>
          </div>
        </div>
      </template>
    </div>

    <AppModal :open="retryTarget !== null" title="重试任务" size="sm" @close="retryTarget = null">
      <div class="stack">
        <p>任务 <span class="mono">{{ retryTarget?.id }}</span>（{{ retryTarget?.status }}）。</p>
        <p class="text-sm text-muted">重试仅针对失败/超时项，服务端会校验版本；旧任务不会覆盖更高版本的规则。</p>
        <p v-if="retryError" class="banner banner--danger" role="alert">{{ retryError }}</p>
      </div>
      <template #footer>
        <button type="button" class="btn" :disabled="retrying" @click="retryTarget = null">取消</button>
        <button type="button" class="btn btn--primary" :disabled="retrying" @click="retry">确认重试</button>
      </template>
    </AppModal>
  </div>
</template>