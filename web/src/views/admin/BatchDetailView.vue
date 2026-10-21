<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { adminApi } from '@/api/endpoints'
import type { BatchDetail, BatchResultItem } from '@/api/types'
import { useResource } from '@/lib/async'
import { formatDateTime, prettyJson, utcLabel } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import StatusBadge from '@/components/StatusBadge.vue'

const route = useRoute()
const batchId = computed(() => String(route.params.id))
const batch = useResource<BatchDetail>(() => adminApi.batches.get(batchId.value))

onMounted(() => void batch.reload())

const results = computed<BatchResultItem[]>(() => (Array.isArray(batch.data.value?.results) ? batch.data.value!.results : []))

const summary = computed(() => {
  const counts = new Map<string, number>()
  for (const item of results.value) {
    const status = String(item.status ?? 'unknown')
    counts.set(status, (counts.get(status) ?? 0) + 1)
  }
  return [...counts.entries()]
})

const achieved = computed(() => {
  const succeeded = results.value.filter((item) => item.status === 'succeeded').length
  return `${succeeded}/${results.value.length}`
})

function errorTextOf(item: BatchResultItem): string {
  const error = item.error
  if (!error) return item.note ?? '—'
  if (typeof error === 'string') return error
  return `${error.code ?? ''} ${error.message ?? ''}`.trim() || '—'
}
</script>

<template>
  <div>
    <PageHeader
      :title="`批量任务 · ${batchId}`"
      description="“规则已提交”与“节点已生效”是两件事：只有 succeeded 且回执版本匹配才算生效；失败/超时可在任务页重试。"
    >
      <router-link class="btn btn--sm" to="/admin/tasks">任务与同步</router-link>
      <router-link class="btn btn--sm" to="/admin/members">成员</router-link>
      <button type="button" class="btn btn--sm" :disabled="batch.loading.value" @click="batch.reload()">刷新</button>
    </PageHeader>

    <LoadingBlock v-if="batch.loading.value && !batch.data.value" label="正在读取批量任务" />
    <ErrorState v-else-if="batch.error.value" :error="batch.error.value" @retry="batch.reload()" />
    <div v-else-if="batch.data.value" class="stack">
      <section class="card">
        <div class="card__header"><h2>汇总</h2></div>
        <div class="card__body stack">
          <dl class="dl">
            <dt>任务 ID</dt>
            <dd class="mono">{{ batch.data.value.id }}</dd>
            <dt>状态</dt>
            <dd><StatusBadge :status="batch.data.value.state ?? 'unknown'" /></dd>
            <dt>操作</dt>
            <dd>{{ batch.data.value.operation ?? '未提供' }}</dd>
            <dt>创建时间</dt>
            <dd>{{ formatDateTime(batch.data.value.created_at) }}</dd>
            <dt>逐对象结果</dt>
            <dd class="num">{{ achieved }} 已生效</dd>
          </dl>
          <div class="row">
            <span v-for="[status, count] in summary" :key="status" class="badge">
              {{ status }}：{{ count }}
            </span>
          </div>
          <p class="text-xs text-muted">
            汇总不代表全部生效：未收到匹配版本回执的对象仍可能处于 pending/dispatched/applying。
          </p>
        </div>
      </section>

      <section class="card">
        <div class="card__header"><h2>逐对象结果</h2></div>
        <div v-if="!results.length" class="card__body state-block">
          <p>服务端尚未返回逐对象结果，请稍后刷新。</p>
        </div>
        <div v-else class="table-wrap">
          <table class="table">
            <thead>
              <tr>
                <th>成员</th>
                <th>节点</th>
                <th>Agent</th>
                <th>状态</th>
                <th>错误 / 说明</th>
                <th class="hide-sm">回执时间</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(item, index) in results" :key="`${item.member_id}-${item.node_id}-${index}`">
                <td class="mono">{{ item.member_id ?? '—' }}</td>
                <td class="mono">{{ item.node_id ?? '—' }}</td>
                <td class="mono">{{ item.agent_id ?? '—' }}</td>
                <td><StatusBadge :status="item.status ?? 'unknown'" /></td>
                <td class="text-sm break-all">{{ errorTextOf(item) }}</td>
                <td class="hide-sm">
                  <span :title="utcLabel(item.applied_at as string)">{{ formatDateTime(item.applied_at as string) }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="card">
        <div class="card__header"><h2>原始响应</h2></div>
        <div class="card__body">
          <details>
            <summary class="text-sm text-muted">展开完整字段</summary>
            <pre class="code-block">{{ prettyJson(batch.data.value) }}</pre>
          </details>
        </div>
      </section>
    </div>
  </div>
</template>