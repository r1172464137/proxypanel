<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { meApi } from '@/api/endpoints'
import type { MeStatus, MeUsageResult } from '@/api/types'
import { useResource } from '@/lib/async'
import { formatBytes, formatDateTime, formatQuota } from '@/lib/format'
import { loadMeUsage } from '@/lib/meUsage'
import { memberSummary } from '@/lib/memberService'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const cursors = ref<(string | null)[]>([null])
const page = ref(0)
const usage = useResource<MeUsageResult>((signal) => loadMeUsage(cursors.value[page.value], signal))

/**
 * 单次读取 + 失效游标收口。CURSOR_INVALID 时旧游标范围只能作废：
 * 立即回到首页、清空游标历史（下一次读取 cursor=null），并保留可见错误等用户显式重新加载。
 * 这里不自动重试，避免无界请求循环。
 */
async function load() {
  await usage.reload()
  if (usage.error.value?.code === 'CURSOR_INVALID') {
    page.value = 0
    cursors.value = [null]
  }
}
async function changePage(next: boolean) {
  if (usage.loading.value) return
  if (next) {
    const cursor = usage.data.value?.next_cursor
    if (!cursor) return
    cursors.value[page.value + 1] = cursor
    page.value++
  } else if (page.value > 0) page.value--
  await load()
}
async function refresh() {
  page.value = 0
  cursors.value = [null]
  await Promise.all([load(), status.reload()])
}
const status = useResource<MeStatus>(() => meApi.status())

onMounted(() => {
  void load()
  void status.reload()
})

const items = computed(() => usage.data.value?.items ?? [])
const adjustments = computed(() => page.value === 0 ? usage.data.value?.adjustments ?? [] : [])

const totals = computed(() => {
  if (!usage.data.value || usage.loading.value || usage.error.value || !items.value.length) return null
  const acc = { up: 0, down: 0 }
  for (const item of items.value) {
    if (typeof item.up_bytes !== 'number' || typeof item.down_bytes !== 'number') return null
    acc.up += item.up_bytes
    acc.down += item.down_bytes
  }
  return acc
})

const quota = computed(() => memberSummary(status.data.value))
const timezone = computed(() => quota.value.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone)
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="我的用量" description="查看当前周期额度、用量明细与额度调整。历史记录保留各自的归属周期。">
      <Button data-testid="usage-refresh-button" variant="outline" :disabled="usage.loading.value || status.loading.value" @click="refresh">刷新</Button>
    </PageHeader>

    <div class="space-y-6">
      <LoadingBlock v-if="status.loading.value && !status.data.value" :rows="2" label="正在读取额度" />
      <ErrorState v-else-if="status.error.value" :error="status.error.value" @retry="status.reload()" />
      <Card v-else data-testid="usage-summary-card">
        <CardHeader class="border-b">
          <CardTitle class="text-base font-semibold">{{ quota.title }} · 本周期</CardTitle>
          <p v-if="quota.incomplete" role="status" class="text-sm">部分服务资料需核对，已知数据继续显示；未知字段不表示零。</p>
          <p class="text-xs text-muted-foreground">数据更新时间：暂未提供。时间按 {{ timezone }} 显示。</p>
        </CardHeader>
        <CardContent class="grid gap-4 pt-6 sm:grid-cols-2 lg:grid-cols-4">
          <div class="rounded-lg border border-border bg-muted/30 px-4 py-3">
            <p class="text-xs font-medium text-muted-foreground">已用</p>
            <p class="tabular mt-1 text-xl font-semibold">{{ formatBytes(quota.used) }}</p>
            <p class="mt-1 text-xs text-muted-foreground">
              本页明细 上 {{ formatBytes(totals?.up) }} · 下 {{ formatBytes(totals?.down) }}（不等于本周期总量）
            </p>
          </div>
          <div class="rounded-lg border border-border bg-muted/30 px-4 py-3">
            <p class="text-xs font-medium text-muted-foreground">额度</p>
            <p class="tabular mt-1 text-xl font-semibold">{{ quota.total === undefined ? '—' : formatQuota(quota.total) }}</p>
            <p class="mt-1 text-xs text-muted-foreground">
              {{ quota.unlimited ? '仍受节点与规则限制' : '超出将暂停代理访问' }}
            </p>
          </div>
          <div class="rounded-lg border border-border bg-muted/30 px-4 py-3">
            <p class="text-xs font-medium text-muted-foreground">剩余</p>
            <p class="tabular mt-1 text-xl font-semibold">{{ quota.unlimited ? '不限量' : formatBytes(quota.remaining) }}</p>
            <p class="mt-1 text-xs text-muted-foreground">按字节账本计算</p>
          </div>
          <div class="rounded-lg border border-border bg-muted/30 px-4 py-3">
            <p class="text-xs font-medium text-muted-foreground">重置时间</p>
            <p class="mt-1 text-base font-semibold">{{ formatDateTime(quota.end, timezone) }}</p>
            <p class="mt-1 text-xs text-muted-foreground">周期开始 {{ formatDateTime(quota.start, timezone) }}</p>
          </div>
        </CardContent>
      </Card>

      <LoadingBlock v-if="usage.loading.value" label="正在读取用量" />
      <ErrorState
        v-else-if="usage.error.value"
        :error="usage.error.value"
        :retry-label="usage.error.value.code === 'CURSOR_INVALID' ? '重新加载第一页' : '重试'"
        @retry="load()"
      />
      <Card v-else data-testid="usage-detail-card">
        <CardContent class="pt-6">
          <EmptyState
            v-if="!items.length"
            data-testid="usage-empty-message"
            title="本页暂无用量记录"
            description="暂无记录不等于用量为零。当前周期已用量请以上方摘要为准；历史按发生周期归属。"
          />
          <Table v-else data-testid="me-usage-table">
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
                  {{ formatDateTime(item.period_start ?? item.period?.start, item.period?.timezone || timezone) }} →
                  {{ formatDateTime(item.period_end ?? item.period?.end, item.period?.timezone || timezone) }}
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

      <nav aria-label="用量明细分页" class="flex flex-wrap items-center gap-3">
        <Button data-testid="usage-previous-button" variant="outline" :disabled="page === 0 || usage.loading.value" @click="changePage(false)">上一页</Button>
        <span class="text-sm">第 {{ page + 1 }} 页</span>
        <Button data-testid="usage-next-button" variant="outline" :disabled="!usage.data.value?.next_cursor || usage.loading.value || !!usage.error.value" @click="changePage(true)">下一页</Button>
      </nav>
      <p v-if="page > 0" class="text-sm text-muted-foreground">额度调整记录仅在第一页展示。</p>
      <Card v-if="adjustments.length && !usage.loading.value && !usage.error.value" data-testid="usage-adjustments-card">
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
                <TableCell>{{ formatDateTime(adjustment.created_at, timezone) }}</TableCell>
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
