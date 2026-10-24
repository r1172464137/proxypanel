<script setup lang="ts">
import { computed, onMounted } from 'vue'
import SetupChecklist from '@/components/SetupChecklist.vue'
import { Loader2 } from 'lucide-vue-next'
import { adminApi } from '@/api/endpoints'
import { useResource } from '@/lib/async'
import { formatBytes, formatDateTime, formatNumber, utcLabel } from '@/lib/format'
import { agentNeedsAttention, agentTarget, eventTarget, revisionState, taskTarget } from '@/lib/workbench'
import AppIcon from '@/components/AppIcon.vue'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

const overview = useResource(() => adminApi.overview())

// Bounded, independent sources: these first pages are not global fault counts.
const failedQuery = { state: 'failed', limit: 20 }
const failedTasks = useResource(() => adminApi.tasks.list(failedQuery))
const agents = useResource(() => adminApi.agents.list({ limit: 20 }))
const attentionAgents = computed(() => (agents.data.value?.items ?? []).filter(agentNeedsAttention))
const unknownRevisions = computed(() => (agents.data.value?.items ?? []).filter(agent => revisionState(agent) === 'unknown').length)
const refreshing = computed(() => overview.loading.value || failedTasks.loading.value || agents.loading.value)
async function refresh() {
  await Promise.all([overview.reload(), failedTasks.reload(), agents.reload()])
}
onMounted(() => void refresh())

const data = computed(() => overview.data.value)
const counts = computed(() => data.value?.counts)
const isEmpty = computed(() => {
  const c = counts.value
  return !!c && c.members === 0 && c.servers === 0 && c.nodes === 0 && c.subscriptions === 0
})
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="工作台" description="完成服务交付，跟踪同步进度并处理异常。">
      <Button data-testid="workbench-refresh" class="min-h-11" variant="outline" :disabled="refreshing" @click="refresh()">
        <Loader2 v-if="refreshing" class="animate-spin" aria-hidden="true" />
        <AppIcon v-else name="refresh" :size="15" aria-hidden="true" />
        刷新
      </Button>
    </PageHeader>

    <section data-testid="workbench-pending-list" class="space-y-4" aria-labelledby="pending-title">
      <h2 id="pending-title" class="font-semibold">需要处理</h2>
      <p class="text-sm text-muted-foreground">独立读取失败任务与管理程序各首批最多 20 项；下列条数不是全部故障总数。处理后刷新，以真实来源重新核对。没有待处理任务不代表连接已验证。</p>
      <div class="grid gap-4 lg:grid-cols-2">
        <Card data-testid="workbench-failed-tasks">
          <CardHeader><CardTitle class="text-base">失败任务</CardTitle></CardHeader>
          <CardContent class="space-y-3">
            <LoadingBlock v-if="failedTasks.loading.value && !failedTasks.data.value" label="正在读取失败任务" />
            <ErrorState v-if="failedTasks.error.value" :error="failedTasks.error.value" @retry="failedTasks.reload()" />
            <template v-else-if="failedTasks.data.value">
              <p class="text-sm text-muted-foreground">失败筛选首批 {{ failedTasks.data.value.items.length }} 项{{ failedTasks.data.value.next_cursor ? '，还有更多' : '' }}；不含排队或执行中任务。</p>
              <ul class="divide-y">
                <li v-for="task in failedTasks.data.value.items" :key="task.id" class="space-y-1 py-3 break-words" data-testid="workbench-failed-task">
                  <p><StatusBadge status="failed" label="任务失败" /> <span class="text-sm">任务 {{ task.id }} · {{ task.kind ?? '类型未知' }}</span></p>
                  <p class="text-sm">{{ task.last_error || (typeof task.error === 'string' ? task.error : task.error?.message) || '未提供失败原因，请核对任务回执。' }}</p>
                  <router-link class="inline-flex min-h-11 items-center underline" :to="taskTarget(task, agents.data.value?.items).to">{{ taskTarget(task, agents.data.value?.items).label }}</router-link>
                </li>
              </ul>
              <p v-if="!failedTasks.data.value.items.length">本次失败筛选首批未返回任务；不代表其他运行状态正常。</p>
            </template>
            <router-link class="inline-flex min-h-11 items-center underline" to="/admin/tasks">查看全部任务与同步、核对回执及重试</router-link>
          </CardContent>
        </Card>
        <Card data-testid="workbench-agent-attention">
          <CardHeader><CardTitle class="text-base">管理程序与应用修订</CardTitle></CardHeader>
          <CardContent class="space-y-3">
            <LoadingBlock v-if="agents.loading.value && !agents.data.value" label="正在读取管理程序" />
            <ErrorState v-if="agents.error.value" :error="agents.error.value" @retry="agents.reload()" />
            <template v-else-if="agents.data.value">
              <p class="text-sm text-muted-foreground">管理程序首批 {{ agents.data.value.items.length }} 项中，{{ attentionAgents.length }} 项有离线或修订落后证据{{ agents.data.value.next_cursor ? '；还有更多管理程序未展示' : '' }}。</p>
              <ul class="divide-y">
                <li v-for="(agent, index) in attentionAgents" :key="agent.id ?? agent.agent_id ?? index" class="space-y-1 py-3 break-words" data-testid="workbench-agent-issue">
                  <p class="text-sm">管理程序 {{ agent.name ?? agent.id ?? agent.agent_id ?? '身份未知' }}</p>
                  <p v-if="agent.online === false"><StatusBadge status="offline" label="管理程序离线" /> 当前无法确认配置继续同步，请查看心跳及运行证据。</p>
                  <p v-if="revisionState(agent) === 'lagging'"><StatusBadge status="pending" label="修订落后" /> 节点尚未应用最新配置：已应用 {{ agent.applied_revision }} / 目标 {{ agent.desired_revision }}。</p>
                  <p v-else-if="revisionState(agent) === 'unknown'">应用修订未知，不能判断是否落后。</p>
                  <router-link class="inline-flex min-h-11 items-center underline" :to="agentTarget(agent).to">{{ agentTarget(agent).label }}</router-link>
                </li>
              </ul>
              <p v-if="unknownRevisions" data-testid="workbench-revision-unknown">本批 {{ unknownRevisions }} 项修订资料不完整，未算作落后或已应用。</p>
              <p v-if="!attentionAgents.length">本批未发现明确离线或修订落后；不代表连接已验证。</p>
            </template>
            <router-link class="inline-flex min-h-11 items-center underline" to="/admin/runtime">查看更多管理程序与运行诊断</router-link>
          </CardContent>
        </Card>
      </div>
      <div v-if="data" class="rounded border p-4 space-y-2">
        <p v-if="!data.setup">设置进度未知，请重新读取或前往首次设置核对。</p>
        <p v-if="data.setup?.blocking_reason">{{ data.setup.blocking_reason }}</p>
        <p v-if="data.pending_tasks > 0"><router-link class="inline-flex min-h-11 items-center underline" to="/admin/tasks">{{ data.pending_tasks }} 项任务待处理：核对同步与回执（不等于失败任务数）</router-link></p>
        <p v-if="data.setup?.steps.some(step => step.key === 'servers' && step.state !== 'completed')"><router-link class="inline-flex min-h-11 items-center underline" to="/admin/servers">核对服务器档案与管理程序接入（有档案不代表在线）</router-link></p>
        <p v-if="data.setup?.steps.some(step => step.key === 'traffic_observed' && step.state !== 'completed')"><router-link class="inline-flex min-h-11 items-center underline" to="/admin/usage">尚未观察到真实流量：请成员连接后核对用量</router-link></p>
      </div>
    </section>

    <LoadingBlock v-if="overview.loading.value && !data" label="正在读取总览" />
    <ErrorState v-else-if="overview.error.value" :error="overview.error.value" @retry="overview.reload()" />

    <template v-else-if="data">
      <SetupChecklist :setup="data.setup" />
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
              服务方案
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
              <router-link to="/admin/tasks" data-testid="overview-tasks-link" class="inline-flex min-h-11 items-center underline-offset-4 hover:underline">查看任务与回执</router-link>
            </p>
          </CardContent>
        </Card>
      </div>

      <EmptyState
        v-if="isEmpty"
        title="尚未添加服务器"
        description="先登记服务器并生成一次性注册码，再添加节点与服务方案。"
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
                <router-link data-testid="workbench-event-target" class="inline-flex min-h-11 items-center underline text-sm" :to="eventTarget(event).to">{{ eventTarget(event).label }}</router-link>
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
