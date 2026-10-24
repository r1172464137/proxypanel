<script setup lang="ts">
import { onMounted } from 'vue'
import { api } from '@/api/http'
import type { BatchDetail } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { formatDateTime } from '@/lib/format'
import { BATCH_OPERATIONS, batchResultCounts } from '@/lib/batch'
import PageHeader from '@/components/PageHeader.vue'
import DataTable from '@/components/DataTable.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { Button } from '@/components/ui/button'

// 批次列表：契约里是 GET /admin/batches（items + next_cursor）。endpoints.ts 只封装了
// preview/submit/get，这里用同一个 api.page 走相同路径，避免改动共享的端点文件。
const list = usePagedList<BatchDetail>((cursor) => api.page<BatchDetail>('/api/v1/admin/batches', { query: { limit: 50, ...(cursor ? { cursor } : {}) } }))

onMounted(() => void list.refresh())

function operationLabel(operation: string | undefined): string {
  if (!operation) return '未提供'
  // 已知操作展示中文名；服务端新增操作按原值透传，不臆造文案。
  return BATCH_OPERATIONS.find((item) => item.value === operation)?.label ?? operation
}

function memberCount(batch: BatchDetail): number | '—' {
  if (typeof batch.member_count === 'number') return batch.member_count
  if (typeof batch.affected_count === 'number') return batch.affected_count
  if (Array.isArray(batch.results)) return batch.results.length
  return '—'
}

function succeededCount(batch: BatchDetail): number {
  if (!Array.isArray(batch.results)) return 0
  return batch.results.filter((item) => (item.state ?? item.status) === 'succeeded').length
}

function summaryText(batch: BatchDetail): string {
  if (Array.isArray(batch.results)) {
    const counts = batchResultCounts(batch.results)
    return `已更新 ${counts.updated} / 等待 ${counts.waiting} / 失败或需处理 ${counts.failed}`
  }
  const summary = batch.summary
  if (!summary || typeof summary !== 'object') return '—'
  if (typeof summary.succeeded === 'number' && typeof summary.pending === 'number' && typeof summary.failed === 'number') {
    const updated = summary.succeeded + (typeof summary.rules_applied === 'number' ? summary.rules_applied : 0)
    const failed = summary.failed + (typeof summary.timeout === 'number' ? summary.timeout : 0) + (typeof summary.superseded === 'number' ? summary.superseded : 0)
    return `已更新 ${updated} / 等待 ${summary.pending} / 失败或需处理 ${failed}`
  }
  const entries = Object.entries(summary as Record<string, unknown>)
    .filter(([, value]) => typeof value === 'number' || typeof value === 'string')
    .slice(0, 3)
  if (!entries.length) return '—'
  return entries.map(([key, value]) => `${key}：${String(value)}`).join(' · ')
}

/** 详情页展示逐对象结果；列表只带汇总，两者都允许为空。 */
function detailPath(batch: BatchDetail): string {
  return `/admin/batches/${encodeURIComponent(batch.id)}`
}

const columns = [
  { label: '任务', width: 'w-56' },
  { label: '操作', width: 'w-40' },
  { label: '状态', width: 'w-28' },
  { label: '成员数', width: 'w-24', numeric: true },
  { label: '逐对象生效', width: 'w-28', hideOnMobile: true, numeric: true },
  { label: '创建时间', width: 'w-40', hideOnMobile: true },
  { label: '操作', width: 'w-32' },
]
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="变更记录" description="批量修改的历史记录。提交后逐个成员下发到节点，全部生效才算完成。">
      <Button as-child size="sm" variant="outline">
        <router-link to="/admin/members">成员</router-link>
      </Button>
      <Button as-child size="sm" variant="outline">
        <router-link to="/admin/tasks">任务与同步</router-link>
      </Button>
      <Button size="sm" variant="outline" :disabled="list.loading.value" @click="list.refresh()">刷新</Button>
    </PageHeader>

    <DataTable
      :list="list"
      :columns="columns"
      empty-title="还没有批量任务"
      empty-description="批量操作提交后会出现在这里。批量变更在成员页或订阅/节点组编辑时经过“预览影响 → 确认提交”产生；规则提交与节点生效是两件事。"
      loading-label="正在读取批量任务"
      :count-label="`本页 ${list.items.value.length} 个任务`"
    >
      <template #row="{ item, columnIndex }">
        <template v-if="columnIndex === 0">
          <router-link :to="detailPath(item)" class="font-mono text-xs font-medium hover:underline">{{ item.id }}</router-link>
          <span class="mt-0.5 block text-xs text-muted-foreground">{{ summaryText(item) }}</span>
        </template>
        <template v-else-if="columnIndex === 1">{{ operationLabel(item.operation) }}</template>
        <template v-else-if="columnIndex === 2">
          <StatusBadge :status="item.state ?? 'unknown'" />
          <span v-if="item.idempotent_replay">这是之前那次提交的结果</span>
        </template>
        <template v-else-if="columnIndex === 3">{{ memberCount(item) }}</template>
        <template v-else-if="columnIndex === 4">
          <template v-if="Array.isArray(item.results)">
            {{ succeededCount(item) }} / {{ item.results.length }}
          </template>
          <template v-else>—</template>
        </template>
        <template v-else-if="columnIndex === 5">{{ formatDateTime(item.created_at) }}</template>
        <template v-else>
          <Button as-child size="sm" variant="outline">
            <router-link :to="detailPath(item)">查看详情</router-link>
          </Button>
        </template>
      </template>
      <template #empty-action>
        <Button as-child>
          <router-link to="/admin/members">前往成员页发起批量操作</router-link>
        </Button>
      </template>
    </DataTable>
  </div>
</template>
