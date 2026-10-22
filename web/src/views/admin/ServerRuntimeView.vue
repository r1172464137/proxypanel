<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { adminApi } from '@/api/endpoints'
import type { AgentRecord, CoreInstanceRecord, RegistrationCode, ServerRecord } from '@/api/types'
import { useResource } from '@/lib/async'
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

const server = useResource<ServerRecord>(() => adminApi.servers.get(serverId.value))
const agents = useResource<AgentRecord[]>(async () => (await adminApi.agents.list({ limit: 200 })).items)
const instances = useResource<CoreInstanceRecord[]>(async () => (await adminApi.coreInstances.list({ limit: 200 })).items)

onMounted(() => {
  void server.reload()
  void agents.reload()
  void instances.reload()
})

const scopedAgents = computed(() => {
  const all = agents.data.value ?? []
  const scoped = all.filter((agent) => agent.server_id === undefined || agent.server_id === serverId.value)
  return scoped
})
const agentsUnscoped = computed(() => (agents.data.value ?? []).every((agent) => agent.server_id === undefined))

const scopedInstances = computed(() => {
  const all = instances.data.value ?? []
  return all.filter((instance) => instance.server_id === undefined || instance.server_id === serverId.value)
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
</script>

<template>
  <div class="space-y-6">
    <PageHeader
      title="Agent 与核心实例"
      :description="server.data.value ? `${server.data.value.name} · ${server.data.value.address}` : '运行时状态分层展示：Agent 在线、进程运行、端口可达、代理握手分别确认。'"
    >
      <Button as-child variant="outline">
        <router-link to="/admin/servers">返回服务器</router-link>
      </Button>
      <Button @click="generateCode">生成注册码</Button>
    </PageHeader>

    <ErrorState v-if="server.error.value" :error="server.error.value" @retry="server.reload()" />
    <div v-else class="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle class="text-base">运行时分层</CardTitle>
        </CardHeader>
        <CardContent>
          <ol class="grid gap-2 text-sm sm:grid-cols-2 xl:grid-cols-4">
            <li class="rounded-md border border-border px-3 py-2">
              <span class="block font-medium">① Agent 在线</span>
              <span class="text-xs text-muted-foreground">下方 Agent 表的心跳与在线状态</span>
            </li>
            <li class="rounded-md border border-border px-3 py-2">
              <span class="block font-medium">② 进程运行</span>
              <span class="text-xs text-muted-foreground">核心实例是否启动成功（看实例状态与错误）</span>
            </li>
            <li class="rounded-md border border-border px-3 py-2">
              <span class="block font-medium">③ 端口可达</span>
              <span class="text-xs text-muted-foreground">节点监听端口是否被外部访问到</span>
            </li>
            <li class="rounded-md border border-border px-3 py-2">
              <span class="block font-medium">④ 代理握手</span>
              <span class="text-xs text-muted-foreground">真实客户端的握手结果（需结合探测事件确认）</span>
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
                        xray {{ agent.xray_version ?? '—' }} · 队列 {{ agent.queue_depth ?? 0 }}
                      </span>
                    </TableCell>
                  </TableRow>
                </TableBody>
              </Table>
            </div>
            <p class="mt-3 text-xs text-muted-foreground">
              “Agent 在线”不等于“代理可用”；进程运行、端口可达、真实握手需结合核心实例与探测事件确认。
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
          <CopyField :value="code.code" label="注册码" warning="注册码等同于 Agent 身份凭据，仅可使用一次。" />
          <p class="text-xs text-muted-foreground">过期时间：{{ formatDateTime(code.expires_at) }}</p>
        </template>
      </div>
      <template #footer>
        <Button variant="outline" @click="codeOpen = false">关闭</Button>
      </template>
    </AppModal>
  </div>
</template>
