<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import { useRoute } from 'vue-router'
import { adminApi } from '@/api/endpoints'
import { allPages } from '@/lib/allPages'
import type { AgentRecord, CoreInstanceRecord, RegistrationCode, ServerRecord } from '@/api/types'
import { useResource, usePolling } from '@/lib/async'
import { errorText } from '@/lib/errors'
import { formatDateTime, formatRelative, utcLabel } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import AppModal from '@/components/AppModal.vue'
import CopyField from '@/components/CopyField.vue'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Loader2 } from 'lucide-vue-next'

const route = useRoute()
const serverId = computed(() => String(route.params.id))

// 分层判定需要“现在”，定时刷新让它不会停在页面打开那一刻。
const now = ref(Date.now())
const poll = usePolling(async () => {
  if (document.hidden) return
  now.value = Date.now()
  await Promise.all([agents.reload(), instances.reload()])
}, 5_000)

const server = useResource<ServerRecord>(() => adminApi.servers.get(serverId.value))
const agents = useResource<AgentRecord[]>(async () => allPages(cursor => adminApi.agents.list({ limit: 200, cursor })))
const instances = useResource<CoreInstanceRecord[]>(async () => allPages(cursor => adminApi.coreInstances.list({ limit: 200, cursor })))

onMounted(() => {
  void server.reload()
  void agents.reload()
  void instances.reload()
  poll.start()
})
onBeforeUnmount(poll.stop)

const scopedAgents = computed(() => {
  const all = agents.data.value ?? []
  const scoped = all.filter((agent) => agent.server_id === serverId.value)
  return scoped
})
const agentsUnscoped = computed(() => (agents.data.value ?? []).every((agent) => agent.server_id === undefined))

const scopedInstances = computed(() => {
  const all = instances.data.value ?? []
  return all.filter((instance) => instance.server_id === serverId.value)
})
const instancesUnscoped = computed(() => (instances.data.value ?? []).every((instance) => instance.server_id === undefined))

const codeOpen = ref(false)
const code = ref<RegistrationCode | null>(null)
const codeError = ref('')
const codeLoading = ref(false)

async function generateCode(): Promise<void> {
  codeOpen.value = true
  code.value = null
  codeError.value = ''
  codeLoading.value = true
  try {
    code.value = await adminApi.servers.registrationCode(serverId.value)
  } catch (caught) {
    codeError.value = errorText(caught)
  } finally {
    codeLoading.value = false
  }
}

function heartbeatOf(agent: AgentRecord): string | null {
  const value = agent.last_heartbeat_at ?? agent.heartbeat_at
  return typeof value === 'string' ? value : null
}

function onlineOf(agent: AgentRecord): boolean | null {
  if (typeof agent.online === 'boolean') return agent.online
  if (typeof agent.status === 'string') return agent.status === 'online' ? true : agent.status === 'offline' ? false : null
  return null
}

/* ------------------------------------------------------------------ *
 * 运行时分层：三层都用接口返回的真实数据计算，没有一行是写死的说明。
 * ④「代理握手」已删除：前端没有任何握手探测手段，不能摆一个假层。
 * ------------------------------------------------------------------ */

// 浏览器时钟可能比服务器慢几秒（实测 WSL 比面板慢 1.3~2s）。
// 心跳时间落在"未来"时 age 为负，但那是刚心跳，不能判离线。
const CLOCK_SKEW_TOLERANCE_MS = 10_000
// 心跳阈值：60 秒内算在线（与维护任务判定离线的口径一致）。
const HEARTBEAT_ONLINE_MS = 60_000
// 实例状态里算“进程正常”的集合。
const RUNNING_STATES = new Set(['applied', 'rollback_applied'])

type LayerTone = 'ok' | 'warn' | 'danger'

interface LayerCard {
  title: string
  status: string
  summary: string
  details: string[]
  tone: LayerTone
}

const DOT_CLASS: Record<LayerTone, string> = {
  ok: 'bg-emerald-500',
  warn: 'bg-amber-500',
  danger: 'bg-red-500',
}

const TONE_TEXT_CLASS: Record<LayerTone, string> = {
  ok: 'text-emerald-600 dark:text-emerald-400',
  warn: 'text-amber-600 dark:text-amber-400',
  danger: 'text-red-600 dark:text-red-400',
}

const stateOf = (instance: CoreInstanceRecord): string =>
  String(instance.state ?? instance.status ?? '').toLowerCase()

const nodeNameOf = (instance: CoreInstanceRecord): string =>
  instance.node_name ?? instance.node_id ?? instance.id ?? '未命名节点'

const agentLayer = computed<LayerCard>(() => {
  if (agents.error.value || !agents.data.value || agentsUnscoped.value && agents.data.value.length) return { title: '① 管理程序', status: '未知', summary: '暂时无法核对该服务器的管理程序状态', details: ['请重试读取或核对服务器归属'], tone: 'warn' }
  const rows = scopedAgents.value.map((agent) => {
    const heartbeat = heartbeatOf(agent)
    const hbTime = heartbeat ? new Date(heartbeat).getTime() : Number.NaN
    const age = Number.isNaN(hbTime) ? null : now.value - hbTime
    const online = age !== null && age > -CLOCK_SKEW_TOLERANCE_MS && age < HEARTBEAT_ONLINE_MS
    return {
      name: agent.name ?? agent.id ?? agent.agent_id ?? '未命名 Agent',
      heartbeat,
      age,
      online,
    }
  })

  if (!rows.length) {
    return {
      title: '① Agent 在线',
      status: '未接入',
      summary: '尚未接入管理程序',
      details: ['该服务器还没有 Agent 上报心跳'],
      tone: 'warn',
    }
  }

  if (rows.every(row => !row.heartbeat)) return { title: '① 管理程序', status: '未接入', summary: '已注册但尚未上报心跳', details: ['等待管理程序首次上线；可重新生成注册码交给部署人员'], tone: 'warn' }
  const online = rows.filter((row) => row.online)
  const tone: LayerTone = online.length === rows.length ? 'ok' : online.length === 0 ? 'danger' : 'warn'
  // 离线时给出最近一次心跳，用于判断是刚掉线还是长期失联。
  const heartbeats = rows
    .map((row) => row.heartbeat)
    .filter((value): value is string => typeof value === 'string')
    .sort()
  const latestHeartbeat = heartbeats.at(-1) ?? null
  const summary =
    tone === 'ok'
      ? rows.length === 1
        ? '在线'
        : `${online.length}/${rows.length} 在线`
      : tone === 'danger'
        ? latestHeartbeat
          ? `离线（最后心跳 ${formatRelative(latestHeartbeat, now.value)}）`
          : '离线（没有心跳记录）'
        : `${online.length}/${rows.length} 在线`

  return {
    title: '① Agent 在线',
    status: tone === 'ok' ? '在线' : tone === 'danger' ? '离线' : '部分离线',
    summary,
    details: rows.map((row) =>
      row.online
        ? `${row.name}：在线（心跳 ${row.heartbeat ? formatRelative(row.heartbeat, now.value) : '未提供'}）`
        : `${row.name}：离线（${row.heartbeat ? `最后心跳 ${formatRelative(row.heartbeat, now.value)}` : '没有心跳记录'}）`,
    ),
    tone,
  }
})

const instanceLayer = computed<LayerCard>(() => {
  if (instances.error.value || !instances.data.value || instancesUnscoped.value && instances.data.value.length) return { title: '② 进程运行', status: '未知', summary: '暂时无法读取该服务器的实例', details: ['请重试读取，不应将未知视为已停止或可用'], tone: 'warn' }
  const list = scopedInstances.value
  if (!list.length) {
    return {
      title: '② 进程运行',
      status: '未知',
      summary: '没有核心实例',
      details: ['节点启用并下发后，Agent 才会在这台机器上启动实例'],
      tone: 'warn',
    }
  }

  const rollback = list.filter((instance) => stateOf(instance) === 'rollback_applied')
  if (rollback.length) return {
    title: '② 进程运行', status: '回滚运行 · 目标失败',
    summary: `${rollback.length} 个实例正在使用上一份可运行配置，目标变更未完成`,
    details: rollback.map(instance => `${nodeNameOf(instance)}：${instance.last_error || instance.error || '请修复节点配置后核对新任务'}`), tone: 'warn',
  }
  const running = list.filter((instance) => RUNNING_STATES.has(stateOf(instance)))
  const failed = list.filter((instance) => stateOf(instance) === 'failed')

  if (failed.length) {
    return {
      title: '② 进程运行',
      status: '失败',
      summary: `${failed.length} 个实例失败`,
      details: failed.map(
        (instance) =>
          `${nodeNameOf(instance)}：${instance.last_error || instance.error || '服务端未返回错误详情'}`,
      ),
      tone: 'danger',
    }
  }

  const names = list.map(nodeNameOf)
  const shown = names.slice(0, 4).join('、') + (names.length > 4 ? ` 等 ${names.length} 个` : '')
  if (running.length === list.length) {
    return {
      title: '② 进程运行',
      status: '运行中',
      summary: `${running.length}/${list.length} 运行中`,
      details: [`节点：${shown}`],
      tone: 'ok',
    }
  }

  const pending = list.filter((instance) => !RUNNING_STATES.has(stateOf(instance)))
  return {
    title: '② 进程运行',
    status: '未全部生效',
    summary: `${running.length}/${list.length} 运行中`,
    details: pending.map((instance) => `${nodeNameOf(instance)}：${stateOf(instance) || '状态未提供'}`),
    tone: 'warn',
  }
})

const portLayer = computed<LayerCard>(() => {
  if (instances.error.value || !instances.data.value || instancesUnscoped.value && instances.data.value.length) return { title: '③ 配置同步', status: '未知', summary: '同步状态暂不可获取', details: ['真实连接尚未验证'], tone: 'warn' }
  const list = scopedInstances.value
  if (!list.length) {
    return {
      title: '③ 配置同步',
      status: '未确认',
      summary: '没有核心实例',
      details: ['没有实例，就没有可确认的下发记录'],
      tone: 'warn',
    }
  }

  const applied = list.filter(
    (instance) => stateOf(instance) === 'applied' && Boolean(instance.applied_at)
      && typeof instance.applied_revision === 'number' && typeof instance.desired_revision === 'number'
      && instance.applied_revision === instance.desired_revision,
  )
  const nodeText = list
    .filter((instance) => !applied.includes(instance))
    .map(nodeNameOf)
    .join('、')
  const note = '浏览器无法直连节点端口，这里只确认下发结果，不代替真实连通性测试。'

  if (applied.length === list.length) {
    const latest = applied
      .map((instance) => String(instance.applied_at))
      .sort()
      .at(-1) as string
    return {
      title: '③ 配置同步',
      status: '最近配置已应用 · 连接未验证',
      summary: '已收到与目标修订一致的配置回执；端口可达性未验证',
      details: [`生效时间：${formatDateTime(latest)}`, note],
      tone: 'ok',
    }
  }

  if (!applied.length) {
    return {
      title: '③ 配置同步',
      status: '未确认',
      summary: '未确认',
      details: ['应用回执、应用时间或目标修订尚未完全核对；回滚运行不算目标已应用', note],
      tone: 'warn',
    }
  }

  const latest = applied
    .map((instance) => String(instance.applied_at))
    .sort()
    .at(-1) as string
  return {
    title: '③ 配置同步',
    status: '部分确认',
    summary: `${applied.length}/${list.length} 目标配置已应用，连接未验证`,
    details: [`未确认：${nodeText}`, `最近生效时间：${formatDateTime(latest)}`, note],
    tone: 'warn',
  }
})

const layers = computed<LayerCard[]>(() => [agentLayer.value, instanceLayer.value, portLayer.value])
</script>

<template>
  <div class="space-y-6">
    <PageHeader
      title="Agent 与核心实例"
      :description="server.data.value ? `${server.data.value.name} · ${server.data.value.address}` : '运行时状态分层展示：Agent 在线、进程运行、配置同步各自核对；真实连接需在客户端验证。'"
    >
      <Button as-child variant="outline">
        <router-link to="/admin/servers">返回服务器</router-link>
      </Button>
      <Button as-child variant="outline"><router-link to="/admin/runtime">返回运行总览</router-link></Button>
      <Button data-testid="server-registration-code-button" @click="generateCode">生成接入注册码</Button>
    </PageHeader>

    <ErrorState v-if="server.error.value" :error="server.error.value" @retry="server.reload()" />
    <div v-else class="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle class="text-base">运行时分层</CardTitle>
        </CardHeader>
        <CardContent>
          <p class="mb-3 text-xs text-muted-foreground">
            最近读取：{{ formatDateTime(new Date(now).toISOString()) }}。管理程序、核心进程与目标配置分别核对；端口可达性及真实客户端连接未验证。
          </p>
          <ol class="grid gap-3 sm:grid-cols-3">
            <li
              v-for="layer in layers"
              :key="layer.title"
              class="rounded-md border border-border px-3 py-3"
            >
              <div class="flex items-center gap-2">
                <span class="size-2 shrink-0 rounded-full" :class="DOT_CLASS[layer.tone]" aria-hidden="true" />
                <span class="font-medium">{{ layer.title }}</span>
                <span class="ml-auto text-xs font-medium" :class="TONE_TEXT_CLASS[layer.tone]">
                  {{ layer.status }}
                </span>
              </div>
              <p class="mt-1 text-sm font-medium">{{ layer.summary }}</p>
              <ul class="mt-1 space-y-0.5 text-xs text-muted-foreground">
                <li v-for="(detail, index) in layer.details" :key="index" class="break-words [overflow-wrap:anywhere]">
                  {{ detail }}
                </li>
              </ul>
            </li>
          </ol>
        </CardContent>
      </Card>

      <Card>
        <CardHeader class="flex-row flex-wrap items-center justify-between gap-2 space-y-0">
          <CardTitle class="text-base">Agent</CardTitle>
          <Button variant="outline" size="sm" :disabled="agents.loading.value" @click="agents.reload()">刷新 Agent</Button>
        </CardHeader>
        <CardContent>
          <LoadingBlock v-if="agents.loading.value && !agents.data.value" :rows="2" label="正在读取 Agent" />
          <ErrorState v-else-if="agents.error.value" :error="agents.error.value" @retry="agents.reload()" />
          <EmptyState
            v-else-if="!scopedAgents.length"
            title="该服务器未注册 Agent"
            description="使用一次性注册码在该机器上启动 Agent；注册码只会展示一次。"
          >
            <Button @click="generateCode">生成一次性注册码</Button>
          </EmptyState>
          <template v-else>
            <Alert v-if="agentsUnscoped" class="mb-3">
              <AlertDescription>
                该 Agent 列表未返回 server_id，无法确认归属；以下内容为服务端返回的全部 Agent 条目。
              </AlertDescription>
            </Alert>
            <div class="overflow-hidden rounded-md border border-border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Agent</TableHead>
                    <TableHead>在线</TableHead>
                    <TableHead>最后心跳</TableHead>
                    <TableHead class="text-right">授权版本</TableHead>
                    <TableHead class="hidden md:table-cell">版本</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <TableRow v-for="(agent, index) in scopedAgents" :key="agent.id ?? agent.agent_id ?? index">
                    <TableCell>
                      <span class="font-mono text-sm">{{ agent.name ?? agent.id ?? agent.agent_id ?? '未命名' }}</span>
                      <span class="mt-0.5 block font-mono text-xs text-muted-foreground">{{ agent.server_id ?? 'server_id 未提供' }}</span>
                    </TableCell>
                    <TableCell>
                      <StatusBadge v-if="onlineOf(agent) !== null" :status="onlineOf(agent) ? 'online' : 'offline'" />
                      <span v-else class="text-muted-foreground">服务端未提供</span>
                    </TableCell>
                    <TableCell>
                      <template v-if="heartbeatOf(agent)">
                        <span :title="utcLabel(heartbeatOf(agent))">{{ formatDateTime(heartbeatOf(agent)) }}</span>
                        <span class="mt-0.5 block text-xs text-muted-foreground">
                          {{ formatRelative(heartbeatOf(agent)) }}<template v-if="typeof agent.offline_seconds === 'number' && agent.offline_seconds > 0"> · 离线 {{ Math.round(agent.offline_seconds) }}s</template>
                        </span>
                      </template>
                      <span v-else class="text-muted-foreground">未提供</span>
                    </TableCell>
                    <TableCell class="text-right tabular-nums">
                      <template v-if="typeof agent.applied_revision === 'number'">
                        已应用 {{ agent.applied_revision }}<template v-if="typeof agent.desired_revision === 'number'"> / 期望 {{ agent.desired_revision }}</template>
                      </template>
                      <span v-else class="text-muted-foreground">未提供</span>
                    </TableCell>
                    <TableCell class="hidden font-mono md:table-cell">
                      {{ agent.version ?? '—' }}
                      <span class="mt-0.5 block text-xs text-muted-foreground">
                        xray {{ agent.xray_version ?? '—' }} · 队列 {{ agent.queue_depth ?? '未知' }}
                      </span>
                    </TableCell>
                  </TableRow>
                </TableBody>
              </Table>
            </div>
            <p class="mt-3 text-xs text-muted-foreground">
              “Agent 在线”不等于“代理可用”：进程是否启动、端口是否可用，对照上方分层与下方核心实例表。
            </p>
          </template>
        </CardContent>
      </Card>

      <Card>
        <CardHeader class="flex-row flex-wrap items-center justify-between gap-2 space-y-0">
          <CardTitle class="text-base">核心实例</CardTitle>
          <Button variant="outline" size="sm" :disabled="instances.loading.value" @click="instances.reload()">刷新实例</Button>
        </CardHeader>
        <CardContent>
          <LoadingBlock v-if="instances.loading.value && !instances.data.value" :rows="2" label="正在读取核心实例" />
          <ErrorState v-else-if="instances.error.value" :error="instances.error.value" @retry="instances.reload()" />
          <EmptyState
            v-else-if="!scopedInstances.length"
            title="没有核心实例"
            description="节点启用并下发后，Agent 会在本机启动对应实例；此处显示服务端返回的实例与回执状态。"
          />
          <template v-else>
            <Alert v-if="instancesUnscoped" class="mb-3">
              <AlertDescription>
                核心实例列表未返回 server_id，无法确认归属；以下为服务端返回的全部实例条目。
              </AlertDescription>
            </Alert>
            <div class="overflow-hidden rounded-md border border-border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>实例</TableHead>
                    <TableHead>状态</TableHead>
                    <TableHead class="text-right">世代</TableHead>
                    <TableHead class="text-right">授权版本</TableHead>
                    <TableHead class="hidden md:table-cell">错误</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <TableRow v-for="(instance, index) in scopedInstances" :key="instance.id ?? instance.node_id ?? index">
                    <TableCell>
                      <span class="font-mono text-sm">{{ instance.node_id ?? instance.id ?? '—' }}</span>
                      <span class="mt-0.5 block text-xs text-muted-foreground">{{ instance.node_name ?? '' }}</span>
                    </TableCell>
                    <TableCell><StatusBadge :status="instance.state ?? instance.status ?? 'unknown'" /></TableCell>
                    <TableCell class="text-right tabular-nums">{{ instance.generation ?? '—' }}</TableCell>
                    <TableCell class="text-right tabular-nums">
                      {{ instance.applied_revision ?? instance.config_revision ?? '—' }}
                    </TableCell>
                    <TableCell class="hidden break-all text-sm md:table-cell">{{ instance.last_error ?? instance.error ?? '—' }}</TableCell>
                  </TableRow>
                </TableBody>
              </Table>
            </div>
          </template>
        </CardContent>
      </Card>
    </div>

    <AppModal :open="codeOpen" title="一次性 Agent 注册码" size="sm" @close="codeOpen = false">
      <div class="space-y-4">
        <p v-if="codeLoading" class="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 class="animate-spin" aria-hidden="true" /> 正在生成…
        </p>
        <Alert v-if="codeError" variant="destructive">
          <AlertDescription class="break-all">{{ codeError }}</AlertDescription>
        </Alert>
        <template v-if="code">
          <CopyField data-testid="registration-code-value" :value="code.code" label="注册码" warning="仅显示一次，仅可使用一次。复制不等于接入成功，请等待管理程序心跳。" />
          <p class="text-xs text-muted-foreground">过期时间：{{ formatDateTime(code.expires_at) }}</p>
        </template>
      </div>
      <template #footer>
        <Button variant="outline" @click="codeOpen = false">关闭</Button>
      </template>
    </AppModal>
  </div>
</template>
