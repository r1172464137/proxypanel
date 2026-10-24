<script setup lang="ts">
import { computed, onMounted, onUnmounted, watch } from 'vue'
import { batchResultCounts } from '@/lib/batch'
import { useRoute } from 'vue-router'
import { adminApi } from '@/api/endpoints'
import type { BatchDetail, BatchResultItem } from '@/api/types'
import { useResource, usePolling } from '@/lib/async'
import { formatDateTime, prettyJson, utcLabel } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

const route = useRoute()
const batchId = computed(() => String(route.params.id))
const batch = useResource<BatchDetail>(() => adminApi.batches.get(batchId.value))

const poll = usePolling(async () => { if (!batch.loading.value) await batch.reload() }, 5000)
onMounted(() => { void batch.reload(); poll.start() })
onUnmounted(poll.stop)
watch(() => batch.data.value?.state, state => {
  if (state && ['completed', 'succeeded', 'failed'].includes(state)) poll.stop()
})

const results = computed<BatchResultItem[]>(() => (Array.isArray(batch.data.value?.results) ? batch.data.value!.results : []))

const summary = computed(() => {
  const counts = new Map<string, number>()
  for (const item of results.value) {
    const status = String(item.state ?? item.status ?? 'unknown')
    counts.set(status, (counts.get(status) ?? 0) + 1)
  }
  return [...counts.entries()]
})

const counts = computed(() => batchResultCounts(results.value))

function errorTextOf(item: BatchResultItem): string {
  const error = item.error
  if (!error) return item.note ?? '—'
  if (typeof error === 'string') return error
  return `${error.code ?? ''} ${error.message ?? ''}`.trim() || '—'
}
</script>

<template>
  <div class="space-y-4">
    <PageHeader
      :title="`变更记录 · ${batchId}`"
      description="这里是这次批量修改的明细。每条都要等节点那边回报成功才算生效，失败的可以到「任务与同步」重试。"
    >
      <Button as-child variant="outline" size="sm">
        <router-link :to="`/admin/tasks?batch_id=${encodeURIComponent(batchId)}`">本批次任务与同步</router-link>
      </Button>
      <Button as-child variant="outline" size="sm">
        <router-link to="/admin/members">成员</router-link>
      </Button>
      <Button variant="outline" size="sm" :disabled="batch.loading.value" @click="batch.reload()">刷新</Button>
    </PageHeader>

    <LoadingBlock v-if="batch.loading.value && !batch.data.value" label="正在读取批量任务" />
    <ErrorState v-else-if="batch.error.value" :error="batch.error.value" @retry="batch.reload()" />
    <div v-else-if="batch.data.value" class="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle class="text-base">汇总</CardTitle>
        </CardHeader>
        <CardContent class="space-y-4">
          <dl class="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-6 gap-y-2 text-sm">
            <dt class="text-muted-foreground">任务 ID</dt>
            <dd class="font-mono break-all">{{ batch.data.value.id }}</dd>
            <dt class="text-muted-foreground">状态</dt>
            <dd><StatusBadge :status="batch.data.value.state ?? 'unknown'" /></dd>
            <dt class="text-muted-foreground">操作</dt>
            <dd>{{ batch.data.value.operation ?? '未提供' }}</dd>
            <dt class="text-muted-foreground">创建时间</dt>
            <dd>{{ formatDateTime(batch.data.value.created_at) }}</dd>
            <dt class="text-muted-foreground">逐对象结果</dt>
            <dd class="tabular" data-testid="batch-result-counts">已更新 {{ counts.updated }} / 等待 {{ counts.waiting }} / 失败或需处理 {{ counts.failed }}</dd>
            <dt v-if="batch.data.value.idempotent_replay">提交核对</dt>
            <dd v-if="batch.data.value.idempotent_replay">这是之前那次提交的结果</dd>
          </dl>
          <div class="flex flex-wrap items-center gap-1">
            <Badge v-for="[status, count] in summary" :key="status" variant="secondary">
              {{ status }}：{{ count }}
            </Badge>
          </div>
          <p class="text-xs text-muted-foreground">
            汇总不代表全部生效：未收到匹配版本回执的对象仍可能处于 pending/dispatched/applying。
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle class="text-base">逐对象结果</CardTitle>
        </CardHeader>
        <CardContent v-if="!results.length" class="text-sm text-muted-foreground">
          服务端尚未返回逐对象结果，请稍后刷新。
        </CardContent>
        <CardContent v-else class="px-0 pb-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>成员</TableHead>
                <TableHead>节点</TableHead>
                <TableHead>Agent</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>错误 / 说明</TableHead>
                <TableHead class="hidden md:table-cell">回执时间</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="(item, index) in results" :key="`${item.member_id}-${item.node_id}-${index}`">
                <TableCell class="font-mono">{{ item.member_id ?? '—' }}</TableCell>
                <TableCell class="font-mono">{{ item.node_id ?? '—' }}</TableCell>
                <TableCell class="font-mono">{{ item.agent_id ?? '—' }}</TableCell>
                <TableCell><StatusBadge :status="item.state ?? item.status ?? 'unknown'" /></TableCell>
                <TableCell><span class="break-all text-sm">{{ errorTextOf(item) }}</span><router-link v-if="item.task_id" :to="`/admin/tasks?batch_id=${encodeURIComponent(batchId)}`">任务 {{ item.task_id }}</router-link></TableCell>
                <TableCell class="hidden md:table-cell">
                  <span :title="utcLabel(item.applied_at as string)">{{ formatDateTime(item.applied_at as string) }}</span>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle class="text-base">原始响应</CardTitle>
        </CardHeader>
        <CardContent>
          <Collapsible>
            <CollapsibleTrigger as-child>
              <Button variant="ghost" size="sm" class="text-muted-foreground">展开完整字段</Button>
            </CollapsibleTrigger>
            <CollapsibleContent>
              <pre class="mt-2 max-h-96 overflow-auto rounded-md border border-border bg-muted p-3 font-mono text-xs whitespace-pre-wrap break-all">{{ prettyJson(batch.data.value) }}</pre>
            </CollapsibleContent>
          </Collapsible>
        </CardContent>
      </Card>
    </div>
  </div>
</template>
