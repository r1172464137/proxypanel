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
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

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
  <div class="space-y-6">
    <PageHeader title="我的用量" description="查看本期用了多少流量。数字来自实际计费记录；补报的历史数据会标注「估算」。">
      <Button variant="outline" :disabled="usage.loading.value" @click="usage.reload()">刷新</Button>
    </PageHeader>

    <div class="space-y-6">
      <LoadingBlock v-if="status.loading.value && !status.data.value" :rows="2" label="正在读取额度" />
      <ErrorState v-else-if="status.error.value" :error="status.error.value" @retry="status.reload()" />
      <Card v-else>
        <CardHeader class="border-b">
          <CardTitle class="text-base font-semibold">本周期</CardTitle>
        </CardHeader>
        <CardContent class="grid gap-4 pt-6 sm:grid-cols-2 lg:grid-cols-4">
          <div class="rounded-lg border border-border bg-muted/30 px-4 py-3">
            <p class="text-xs font-medium text-muted-foreground">已用</p>
            <p class="tabular mt-1 text-xl font-semibold">{{ formatBytes(quota.used) }}</p>
            <p class="mt-1 text-xs text-muted-foreground">
              明细合计 上 {{ formatBytes(totals.up) }} · 下 {{ formatBytes(totals.down) }}
            </p>
          </div>
          <div class="rounded-lg border border-border bg-muted/30 px-4 py-3">
            <p class="text-xs font-medium text-muted-foreground">额度</p>
            <p class="tabular mt-1 text-xl font-semibold">{{ quota.unlimited ? '不限量' : formatQuota(quota.total) }}</p>
            <p class="mt-1 text-xs text-muted-foreground">
              {{ quota.unlimited ? '仍受节点与规则限制' : '超出将暂停代理访问' }}
            </p>
          </div>
          <div class="rounded-lg border border-border bg-muted/30 px-4 py-3">
            <p class="text-xs font-medium text-muted-foreground">剩余</p>
            <p class="tabular mt-1 text-xl font-semibold">{{ remaining === null ? '—' : formatBytes(remaining) }}</p>
            <p class="mt-1 text-xs text-muted-foreground">按字节账本计算</p>
          </div>
          <div class="rounded-lg border border-border bg-muted/30 px-4 py-3">
            <p class="text-xs font-medium text-muted-foreground">重置时间</p>
            <p class="mt-1 text-base font-semibold">{{ formatDateTime(quota.end, quota.timezone) }}</p>
            <p class="mt-1 text-xs text-muted-foreground">周期开始 {{ formatDateTime(quota.start, quota.timezone) }}</p>
          </div>
        </CardContent>
      </Card>

      <LoadingBlock v-if="usage.loading.value && !usage.data.value" label="正在读取用量" />
      <ErrorState v-else-if="usage.error.value" :error="usage.error.value" @retry="usage.reload()" />
      <Card v-else>
        <CardContent class="pt-6">
          <EmptyState
            v-if="!items.length"
            title="本周期尚无流量记录"
            description="Agent 每 5 秒采样；刚开始使用或刚绑定订阅时可能还没有数据。"
          />
          <Table v-else>
            <TableHeader>
              <TableRow>
                <TableHead>节点</TableHead>
                <TableHead>周期</TableHead>
                <TableHead>上行</TableHead>
                <TableHead>下行</TableHead>
                <TableHead>标记</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="(item, index) in items" :key="index">
                <TableCell class="mono">{{ item.node_id ?? '—' }}</TableCell>
                <TableCell class="text-xs">
                  {{ formatDateTime(item.period_start ?? item.period?.start, item.period?.timezone) }} →
                  {{ formatDateTime(item.period_end ?? item.period?.end, item.period?.timezone) }}
                </TableCell>
                <TableCell class="tabular">{{ formatBytes(item.up_bytes) }}</TableCell>
                <TableCell class="tabular">{{ formatBytes(item.down_bytes) }}</TableCell>
                <TableCell>
                  <div class="flex flex-wrap items-center gap-1">
                    <StatusBadge v-if="item.estimated" status="estimated" />
                    <StatusBadge v-if="item.boundary_split" status="estimated" label="跨周期分摊" />
                    <StatusBadge
                      v-if="item.correction || item.corrections?.length"
                      label="历史更正"
                      tone="info"
                    />
                    <span
                      v-if="!item.estimated && !item.boundary_split && !item.correction && !item.corrections?.length"
                      class="text-xs text-muted-foreground"
                    >
                      —
                    </span>
                  </div>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Card v-if="adjustments.length">
        <CardHeader class="border-b">
          <CardTitle class="text-base font-semibold">额度调整记录</CardTitle>
        </CardHeader>
        <CardContent class="pt-4">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>时间</TableHead>
                <TableHead>调整量</TableHead>
                <TableHead>原因</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="(adjustment, index) in adjustments" :key="adjustment.id ?? index">
                <TableCell>{{ formatDateTime(adjustment.created_at) }}</TableCell>
                <TableCell class="tabular">{{ formatBytes(adjustment.bytes) }}</TableCell>
                <TableCell class="text-sm">{{ adjustment.reason ?? '—' }}</TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  </div>
</template>
