<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import { adminApi } from '@/api/endpoints'
import type { TaskRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText } from '@/lib/errors'
import { formatDateTime, prettyJson, utcLabel } from '@/lib/format'
import { usePolling } from '@/lib/async'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import AppModal from '@/components/AppModal.vue'
import DataTable from '@/components/DataTable.vue'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'

const list = usePagedList<TaskRecord>((cursor) => adminApi.tasks.list({ limit: 50, cursor }))
const retryTarget = ref<TaskRecord | null>(null)
const retrying = ref(false)
const retryError = ref('')
const retryNotice = ref('')
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
    retryNotice.value = `任务 ${target.id} 已重新排队，等待新回执；这不代表节点已恢复。`
    pushToast('success', '重试已受理', '重试受版本检查限制，旧任务不会覆盖更新后的规则。')
    retryTarget.value = null
    await list.refresh()
  } catch (caught) {
    retryError.value = errorText(caught)
  } finally {
    retrying.value = false
  }
}

const columns = [
  { label: '任务' },
  { label: '状态' },
  { label: '节点 / Agent' },
  { label: '尝试', hideOnMobile: true, numeric: true },
  { label: '错误' },
  { label: '时间', hideOnMobile: true },
  { label: '操作' },
]
</script>

<template>
  <div class="space-y-4">
    <PageHeader
      title="任务与同步"
      description="你改的配置要送到节点上才算真的生效。这里看每一条走到哪一步了；标红的可以点重试。"
    >
      <Button variant="outline" :disabled="list.loading.value" @click="list.refresh()">刷新</Button>
      <Button :variant="autoRefresh ? 'default' : 'outline'" @click="toggleAuto">
        {{ autoRefresh ? '停止自动刷新' : '自动刷新（10s）' }}
      </Button>
    </PageHeader>

    <p v-if="retryNotice" role="status" class="rounded border p-4">{{ retryNotice }}</p>
    <DataTable
      :list="list"
      :columns="columns"
      empty-title="没有任务"
      empty-description="规则变更或凭据轮换会生成下发任务；Agent 离线时任务会保持待下发。"
      loading-label="正在读取任务"
    >
      <template #row="{ item, columnIndex }">
        <template v-if="columnIndex === 0">
          <span class="font-mono text-xs">{{ item.id }}</span>
        </template>
        <template v-else-if="columnIndex === 1">
          <StatusBadge :status="item.state ?? item.status ?? 'unknown'" />
        </template>
        <template v-else-if="columnIndex === 2">
          <div class="flex flex-col text-xs">
            <span class="font-mono">{{ item.node_id ?? '—' }}</span>
            <span class="font-mono text-muted-foreground">{{ item.agent_id ?? item.target_agent_id ?? '—' }}</span>
          </div>
        </template>
        <template v-else-if="columnIndex === 3">
          {{ item.attempts ?? item.retry_count ?? '—' }}
        </template>
        <template v-else-if="columnIndex === 4">
          <span class="break-all text-sm">{{ errorOf(item) }}</span>
        </template>
        <template v-else-if="columnIndex === 5">
          <div class="flex flex-col text-xs">
            <span :title="utcLabel(item.created_at)">{{ formatDateTime(item.created_at) }}</span>
            <span v-if="item.applied_at ?? item.updated_at" class="text-muted-foreground">
              回执/更新 {{ formatDateTime(item.applied_at ?? item.updated_at) }}
            </span>
          </div>
        </template>
        <template v-else-if="columnIndex === 6">
          <div class="flex flex-col items-start gap-1">
            <Button
              variant="outline"
              size="sm"
              :disabled="!['failed', 'timeout'].includes(String(item.state ?? item.status))"
              @click="retryTarget = item"
            >
              重试
            </Button>
            <Collapsible>
              <CollapsibleTrigger as-child>
                <Button variant="ghost" size="sm" class="text-muted-foreground">详情</Button>
              </CollapsibleTrigger>
              <CollapsibleContent>
                <pre class="mt-2 max-h-80 min-w-60 overflow-auto rounded-md border border-border bg-muted p-3 font-mono text-xs whitespace-pre-wrap break-all">{{ prettyJson(item) }}</pre>
              </CollapsibleContent>
            </Collapsible>
          </div>
        </template>
      </template>
      <template #empty-action>
        <Button variant="outline" @click="list.refresh()">刷新</Button>
      </template>
    </DataTable>

    <AppModal :open="retryTarget !== null" title="重试任务" size="sm" @close="retryTarget = null">
      <div class="space-y-3">
        <p>任务 <span class="font-mono">{{ retryTarget?.id }}</span>（{{ retryTarget?.status }}）。</p>
        <p class="text-sm text-muted-foreground">重试仅针对失败/超时项，服务端会校验版本；旧任务不会覆盖更高版本的规则。</p>
        <Alert v-if="retryError" variant="destructive">
          <AlertDescription class="break-all">{{ retryError }}</AlertDescription>
        </Alert>
      </div>
      <template #footer>
        <Button variant="outline" :disabled="retrying" @click="retryTarget = null">取消</Button>
        <Button :disabled="retrying" @click="retry">
          <Loader2 v-if="retrying" class="animate-spin" aria-hidden="true" />
          {{ retrying ? '重试中…' : '确认重试' }}
        </Button>
      </template>
    </AppModal>
  </div>
</template>
