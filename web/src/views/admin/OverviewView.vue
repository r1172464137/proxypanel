<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import { adminApi } from '@/api/endpoints'
import { useResource } from '@/lib/async'
import { formatBytes, formatDateTime, formatNumber, utcLabel } from '@/lib/format'
import AppIcon from '@/components/AppIcon.vue'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

const overview = useResource(() => adminApi.overview())

onMounted(() => void overview.reload())

const data = computed(() => overview.data.value)
const counts = computed(() => data.value?.counts)
const isEmpty = computed(() => {
  const c = counts.value
  return !!c && c.members === 0 && c.servers === 0 && c.nodes === 0 && c.subscriptions === 0
})
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="总览" description="一屏看关键数字。这些是面板的即时统计；节点是否真的在跑，请看「服务器」里的运行时页。">
      <Button variant="outline" :disabled="overview.loading.value" @click="overview.reload()">
        <Loader2 v-if="overview.loading.value" class="animate-spin" aria-hidden="true" />
        <AppIcon v-else name="refresh" :size="15" aria-hidden="true" />
        刷新
      </Button>
    </PageHeader>

    <LoadingBlock v-if="overview.loading.value && !data" label="正在读取总览" />
    <ErrorState v-else-if="overview.error.value" :error="overview.error.value" @retry="overview.reload()" />

    <template v-else-if="data">
      <!-- 主要计数：4 项；用量/在线/待办作为次要 3 项，信息层级分开但仍全部保留 -->
      <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Card>
          <CardHeader class="flex-row items-center justify-between gap-2 space-y-0 pb-2">
            <CardTitle class="flex items-center gap-2 text-sm font-medium text-muted-foreground">
              <AppIcon name="members" :size="14" aria-hidden="true" />
              成员
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p class="text-3xl font-semibold tabular">{{ formatNumber(data.counts.members) }}</p>
            <p class="mt-1 text-xs text-muted-foreground">账户总数（含停用/归档）</p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader class="flex-row items-center justify-between gap-2 space-y-0 pb-2">
            <CardTitle class="flex items-center gap-2 text-sm font-medium text-muted-foreground">
              <AppIcon name="server" :size="14" aria-hidden="true" />
              服务器
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p class="text-3xl font-semibold tabular">{{ formatNumber(data.counts.servers) }}</p>
            <p class="mt-1 text-xs text-muted-foreground">机器档案</p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader class="flex-row items-center justify-between gap-2 space-y-0 pb-2">
            <CardTitle class="flex items-center gap-2 text-sm font-medium text-muted-foreground">
              <AppIcon name="node" :size="14" aria-hidden="true" />
              节点
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p class="text-3xl font-semibold tabular">{{ formatNumber(data.counts.nodes) }}</p>
            <p class="mt-1 text-xs text-muted-foreground">代理入口</p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader class="flex-row items-center justify-between gap-2 space-y-0 pb-2">
            <CardTitle class="flex items-center gap-2 text-sm font-medium text-muted-foreground">
              <AppIcon name="subscription" :size="14" aria-hidden="true" />
              订阅规则
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p class="text-3xl font-semibold tabular">{{ formatNumber(data.counts.subscriptions) }}</p>
            <p class="mt-1 text-xs text-muted-foreground">授权规则集合</p>
          </CardContent>
        </Card>
      </div>

      <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <Card>
          <CardHeader class="space-y-0 pb-2">
            <CardTitle class="flex items-center gap-2 text-sm font-medium text-muted-foreground">
              <AppIcon name="usage" :size="14" aria-hidden="true" />
              当期用量合计
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p class="text-2xl font-semibold tabular">{{ formatBytes(data.usage_bytes) }}</p>
            <p class="mt-1 text-xs text-muted-foreground">字节基数 1024，按当前周期归属</p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader class="space-y-0 pb-2">
            <CardTitle class="flex items-center gap-2 text-sm font-medium text-muted-foreground">
              <AppIcon name="status" :size="14" aria-hidden="true" />
              在线 Agent
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p class="text-2xl font-semibold tabular">{{ formatNumber(data.agents_online) }}</p>
            <p class="mt-1 text-xs text-muted-foreground">心跳在窗口内</p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader class="space-y-0 pb-2">
            <CardTitle class="flex items-center gap-2 text-sm font-medium text-muted-foreground">
              <AppIcon name="tasks" :size="14" aria-hidden="true" />
              待处理任务
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p class="text-2xl font-semibold tabular">{{ formatNumber(data.pending_tasks) }}</p>
            <p class="mt-1 text-xs text-muted-foreground">
              <router-link to="/admin/tasks" class="underline-offset-4 hover:underline">查看任务与回执</router-link>
            </p>
          </CardContent>
        </Card>
      </div>

      <EmptyState
        v-if="isEmpty"
        title="尚未添加服务器"
        description="先登记服务器并生成一次性注册码，再添加节点与订阅规则。"
        icon="server"
      >
        <Button as-child>
          <router-link to="/admin/servers">新建服务器</router-link>
        </Button>
      </EmptyState>

      <Card>
        <CardHeader class="flex-row items-center gap-2 space-y-0 border-b border-border py-3">
          <AppIcon name="events" :size="16" class="text-muted-foreground" aria-hidden="true" />
          <CardTitle class="flex-1 text-base font-semibold">近期事件</CardTitle>
          <Button as-child size="sm" variant="outline">
            <router-link to="/admin/events">
              全部事件
              <AppIcon name="chevronRight" :size="14" aria-hidden="true" />
            </router-link>
          </Button>
        </CardHeader>
        <CardContent class="p-0">
          <div v-if="!data.recent_events.length" class="flex flex-col items-center gap-2 px-6 py-10 text-center">
            <AppIcon name="events" :size="22" class="text-muted-foreground" aria-hidden="true" />
            <p class="text-sm text-muted-foreground">暂无事件记录</p>
          </div>
          <ul v-else class="divide-y divide-border">
            <li
              v-for="event in data.recent_events"
              :key="event.id"
              class="flex flex-wrap items-start gap-3 px-6 py-3"
            >
              <StatusBadge :status="event.severity ?? event.kind ?? 'unknown'" :label="event.severity ?? event.kind ?? '事件'" />
              <div class="min-w-0 flex-1">
                <strong class="block text-sm">{{ event.title ?? event.message ?? event.code ?? '事件' }}</strong>
                <p v-if="event.title && event.message" class="text-sm text-muted-foreground">{{ event.message }}</p>
              </div>
              <time class="whitespace-nowrap text-xs text-muted-foreground" :title="utcLabel(event.created_at)">
                {{ formatDateTime(event.created_at) }}
              </time>
            </li>
          </ul>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
