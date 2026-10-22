<script setup lang="ts">
import { onMounted } from 'vue'
import { api } from '@/api/http'
import type { BatchDetail } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { formatDateTime } from '@/lib/format'
import { BATCH_OPERATIONS } from '@/lib/batch'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'

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
  if (typeof batch.affected_count === 'number') return batch.affected_count
  if (Array.isArray(batch.results)) return batch.results.length
  return '—'
}

function succeededCount(batch: BatchDetail): number {
  if (!Array.isArray(batch.results)) return 0
  return batch.results.filter((item) => (item.state ?? item.status) === 'succeeded').length
}

function summaryText(batch: BatchDetail): string {
  const summary = batch.summary
  if (!summary || typeof summary !== 'object') return '—'
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
</script>

<template>
  <div>
    <PageHeader title="批量任务" description="批量修改的历史记录。提交后逐个成员下发到节点，全部生效才算完成。">
      <router-link class="btn btn--sm" to="/admin/members">成员</router-link>
      <router-link class="btn btn--sm" to="/admin/tasks">任务与同步</router-link>
      <button type="button" class="btn btn--sm" :disabled="list.loading.value" @click="list.refresh()">刷新</button>
    </PageHeader>

    <ErrorState v-if="list.error.value" :error="list.error.value" @retry="list.refresh()" />
    <LoadingBlock v-else-if="list.loading.value && !list.items.value.length" label="正在读取批量任务" />
    <div v-else class="card card__body--flush">
      <div v-if="!list.items.value.length" class="card__body">
        <EmptyState
          title="还没有批量任务"
          description="批量操作提交后会出现在这里。批量变更在成员页或订阅/节点组编辑时经过“预览影响 → 确认提交”产生；规则提交与节点生效是两件事。"
        >
          <router-link class="btn btn--primary" to="/admin/members">前往成员页发起批量操作</router-link>
        </EmptyState>
      </div>
      <template v-else>
        <div class="table-wrap">
          <table class="table">
            <thead>
              <tr>
                <th>任务</th>
                <th>操作</th>
                <th>状态</th>
                <th>成员数</th>
                <th class="hide-sm">逐对象生效</th>
                <th class="hide-sm">创建时间</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="batch in list.items.value" :key="batch.id">
                <td>
                  <router-link :to="detailPath(batch)" class="mono">{{ batch.id }}</router-link>
                  <span class="cell-sub">{{ summaryText(batch) }}</span>
                </td>
                <td>{{ operationLabel(batch.operation) }}</td>
                <td><StatusBadge :status="batch.state ?? 'unknown'" /></td>
                <td class="num">{{ memberCount(batch) }}</td>
                <td class="hide-sm num">
                  <template v-if="Array.isArray(batch.results)">
                    {{ succeededCount(batch) }} / {{ batch.results.length }}
                  </template>
                  <template v-else>—</template>
                </td>
                <td class="hide-sm">{{ formatDateTime(batch.created_at) }}</td>
                <td class="actions">
                  <router-link class="btn btn--sm" :to="detailPath(batch)">查看详情</router-link>
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
  </div>
</template>
