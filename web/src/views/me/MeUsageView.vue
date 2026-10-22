<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { meApi } from '@/api/endpoints'
import type { MeStatus, MeUsageResult } from '@/api/types'
import { useResource } from '@/lib/async'
import { formatBytes, formatDateTime, formatQuota } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'

const usage = useResource<MeUsageResult>(() => meApi.usage())
const status = useResource<MeStatus>(() => meApi.status())

onMounted(() => {
  void usage.reload()
  void status.reload()
})

const items = computed(() => usage.data.value?.items ?? [])
const adjustments = computed(() => usage.data.value?.adjustments ?? [])

const totals = computed(() => {
  const acc = { up: 0, down: 0 }
  for (const item of items.value) {
    acc.up += item.up_bytes ?? 0
    acc.down += item.down_bytes ?? 0
  }
  return acc
})

const member = computed(() => status.data.value?.member)
const access = computed(() => status.data.value?.access)
const quota = computed(() => ({
  used: access.value?.used_bytes ?? member.value?.used_bytes,
  total: access.value?.effective_quota_bytes ?? member.value?.effective_quota_bytes,
  unlimited: (access.value?.effective_quota_bytes ?? member.value?.effective_quota_bytes) === null,
  start: member.value?.period_start,
  end: member.value?.period_end,
  timezone: member.value?.period_timezone,
}))
const remaining = computed(() => {
  if (quota.value.unlimited || quota.value.total === undefined || quota.value.total === null) return null
  return Math.max(0, quota.value.total - (quota.value.used ?? 0))
})
</script>

<template>
  <div>
    <PageHeader title="我的用量" description="查看本期用了多少流量。数字来自实际计费记录；补报的历史数据会标注「估算」。">
      <button type="button" class="btn" :disabled="usage.loading.value" @click="usage.reload()">刷新</button>
    </PageHeader>

    <div class="stack">
      <LoadingBlock v-if="status.loading.value && !status.data.value" :rows="2" label="正在读取额度" />
      <ErrorState v-else-if="status.error.value" :error="status.error.value" @retry="status.reload()" />
      <section v-else class="card">
        <div class="card__header"><h2>本周期</h2></div>
        <div class="card__body grid grid--kpi">
          <div class="kpi">
            <span class="kpi__label">已用</span>
            <span class="kpi__value">{{ formatBytes(quota.used) }}</span>
            <span class="kpi__meta">明细合计 上 {{ formatBytes(totals.up) }} · 下 {{ formatBytes(totals.down) }}</span>
          </div>
          <div class="kpi">
            <span class="kpi__label">额度</span>
            <span class="kpi__value">{{ quota.unlimited ? '不限量' : formatQuota(quota.total) }}</span>
            <span class="kpi__meta">{{ quota.unlimited ? '仍受节点与规则限制' : '超出将暂停代理访问' }}</span>
          </div>
          <div class="kpi">
            <span class="kpi__label">剩余</span>
            <span class="kpi__value">{{ remaining === null ? '—' : formatBytes(remaining) }}</span>
            <span class="kpi__meta">按字节账本计算</span>
          </div>
          <div class="kpi">
            <span class="kpi__label">重置时间</span>
            <span class="kpi__value" style="font-size: var(--text-base)">{{ formatDateTime(quota.end, quota.timezone) }}</span>
            <span class="kpi__meta">周期开始 {{ formatDateTime(quota.start, quota.timezone) }}</span>
          </div>
        </div>
      </section>

      <LoadingBlock v-if="usage.loading.value && !usage.data.value" label="正在读取用量" />
      <ErrorState v-else-if="usage.error.value" :error="usage.error.value" @retry="usage.reload()" />
      <div v-else class="card card__body--flush">
        <div v-if="!items.length" class="card__body">
          <EmptyState title="本周期尚无流量记录" description="Agent 每 5 秒采样；刚开始使用或刚绑定订阅时可能还没有数据。" />
        </div>
        <div v-else class="table-wrap">
          <table class="table">
            <thead>
              <tr><th>节点</th><th>周期</th><th>上行</th><th>下行</th><th>标记</th></tr>
            </thead>
            <tbody>
              <tr v-for="(item, index) in items" :key="index">
                <td class="mono">{{ item.node_id ?? '—' }}</td>
                <td class="text-xs">
                  {{ formatDateTime(item.period_start ?? item.period?.start, item.period?.timezone) }} →
                  {{ formatDateTime(item.period_end ?? item.period?.end, item.period?.timezone) }}
                </td>
                <td class="num">{{ formatBytes(item.up_bytes) }}</td>
                <td class="num">{{ formatBytes(item.down_bytes) }}</td>
                <td>
                  <div class="row" style="gap: var(--space-1)">
                    <StatusBadge v-if="item.estimated" status="estimated" />
                    <StatusBadge v-if="item.boundary_split" status="estimated" label="跨周期分摊" />
                    <span v-if="item.correction || item.corrections?.length" class="badge badge--info">历史更正</span>
                    <span v-if="!item.estimated && !item.boundary_split && !item.correction && !item.corrections?.length" class="text-muted text-xs">—</span>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <section v-if="adjustments.length" class="card">
        <div class="card__header"><h2>额度调整记录</h2></div>
        <div class="table-wrap">
          <table class="table">
            <thead><tr><th>时间</th><th>调整量</th><th>原因</th></tr></thead>
            <tbody>
              <tr v-for="(adjustment, index) in adjustments" :key="adjustment.id ?? index">
                <td>{{ formatDateTime(adjustment.created_at) }}</td>
                <td class="num">{{ formatBytes(adjustment.bytes) }}</td>
                <td class="text-sm">{{ adjustment.reason ?? '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>
  </div>
</template>