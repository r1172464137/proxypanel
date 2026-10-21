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
  <div>
    <PageHeader
      title="Agent 与核心实例"
      :description="server.data.value ? `${server.data.value.name} · ${server.data.value.address}` : '运行时状态分层展示：Agent 在线、进程运行、端口可达、代理握手分别确认。'"
    >
      <router-link class="btn btn--sm" to="/admin/servers">返回服务器</router-link>
      <button type="button" class="btn btn--primary btn--sm" @click="generateCode">生成注册码</button>
    </PageHeader>

    <ErrorState v-if="server.error.value" :error="server.error.value" @retry="server.reload()" />
    <div v-else class="stack">
      <section class="card">
        <div class="card__header"><h2>Agent</h2></div>
        <LoadingBlock v-if="agents.loading.value && !agents.data.value" :rows="2" label="正在读取 Agent" />
        <ErrorState v-else-if="agents.error.value" :error="agents.error.value" @retry="agents.reload()" />
        <div v-else-if="!scopedAgents.length" class="card__body">
          <EmptyState title="该服务器未注册 Agent" description="使用一次性注册码在该机器上启动 Agent；注册码只会展示一次。">
            <button type="button" class="btn btn--primary" @click="generateCode">生成一次性注册码</button>
          </EmptyState>
        </div>
        <template v-else>
          <p v-if="agentsUnscoped" class="banner banner--info" style="margin: var(--space-4)">
            该 Agent 列表未返回 server_id，无法确认归属；以下内容为服务端返回的全部 Agent 条目。
          </p>
          <div class="table-wrap">
            <table class="table">
              <thead>
                <tr>
                  <th>Agent</th>
                  <th>在线</th>
                  <th>最后心跳</th>
                  <th>授权版本</th>
                  <th class="hide-sm">版本</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(agent, index) in scopedAgents" :key="agent.id ?? agent.agent_id ?? index">
                  <td>
                    <span class="mono">{{ agent.name ?? agent.id ?? agent.agent_id ?? '未命名' }}</span>
                    <span class="cell-sub mono">{{ agent.server_id ?? 'server_id 未提供' }}</span>
                  </td>
                  <td>
                    <StatusBadge v-if="onlineOf(agent) !== null" :status="onlineOf(agent) ? 'online' : 'offline'" />
                    <span v-else class="text-muted">服务端未提供</span>
                  </td>
                  <td>
                    <template v-if="heartbeatOf(agent)">
                      <span :title="utcLabel(heartbeatOf(agent))">{{ formatDateTime(heartbeatOf(agent)) }}</span>
                      <span class="cell-sub">{{ formatRelative(heartbeatOf(agent)) }}<template v-if="typeof agent.offline_seconds === 'number' && agent.offline_seconds > 0"> · 离线 {{ Math.round(agent.offline_seconds) }}s</template></span>
                    </template>
                    <span v-else class="text-muted">未提供</span>
                  </td>
                  <td class="num">
                    <template v-if="typeof agent.applied_revision === 'number'">
                      已应用 {{ agent.applied_revision }}<template v-if="typeof agent.desired_revision === 'number'"> / 期望 {{ agent.desired_revision }}</template>
                    </template>
                    <span v-else class="text-muted">未提供</span>
                  </td>
                  <td class="hide-sm mono">{{ agent.version ?? '—' }}<span class="cell-sub">xray {{ agent.xray_version ?? '—' }} · 队列 {{ agent.queue_depth ?? 0 }}</span></td>
                </tr>
              </tbody>
            </table>
          </div>
          <p class="text-xs text-muted" style="padding: var(--space-3) var(--space-4)">
            “Agent 在线”不等于“代理可用”；进程运行、端口可达、真实握手需结合核心实例与探测事件确认。
          </p>
        </template>
      </section>

      <section class="card">
        <div class="card__header"><h2>核心实例</h2></div>
        <LoadingBlock v-if="instances.loading.value && !instances.data.value" :rows="2" label="正在读取核心实例" />
        <ErrorState v-else-if="instances.error.value" :error="instances.error.value" @retry="instances.reload()" />
        <div v-else-if="!scopedInstances.length" class="card__body">
          <EmptyState title="没有核心实例" description="节点启用并下发后，Agent 会在本机启动对应实例；此处显示服务端返回的实例与回执状态。" />
        </div>
        <template v-else>
          <p v-if="instancesUnscoped" class="banner banner--info" style="margin: var(--space-4)">
            核心实例列表未返回 server_id，无法确认归属；以下为服务端返回的全部实例条目。
          </p>
          <div class="table-wrap">
            <table class="table">
              <thead>
                <tr>
                  <th>实例</th>
                  <th>状态</th>
                  <th>世代</th>
                  <th>授权版本</th>
                  <th class="hide-sm">错误</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(instance, index) in scopedInstances" :key="instance.id ?? instance.node_id ?? index">
                  <td><span class="mono">{{ instance.node_id ?? instance.id ?? '—' }}</span><span class="cell-sub">{{ instance.node_name ?? '' }}</span></td>
                  <td><StatusBadge :status="instance.state ?? instance.status ?? 'unknown'" /></td>
                  <td class="num">{{ instance.generation ?? '—' }}</td>
                  <td class="num">
                    {{ instance.applied_revision ?? instance.config_revision ?? '—' }}
                  </td>
                  <td class="hide-sm text-sm break-all">{{ instance.last_error ?? instance.error ?? '—' }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>
      </section>
    </div>

    <AppModal :open="codeOpen" title="一次性 Agent 注册码" size="sm" @close="codeOpen = false">
      <div class="stack">
        <p v-if="codeLoading" class="text-sm"><span class="spinner" aria-hidden="true" /> 正在生成…</p>
        <p v-if="codeError" class="banner banner--danger" role="alert">{{ codeError }}</p>
        <template v-if="code">
          <CopyField :value="code.code" label="注册码" warning="注册码等同于 Agent 身份凭据，仅可使用一次。" />
          <p class="text-xs text-muted">过期时间：{{ formatDateTime(code.expires_at) }}</p>
        </template>
      </div>
      <template #footer>
        <button type="button" class="btn" @click="codeOpen = false">关闭</button>
      </template>
    </AppModal>
  </div>
</template>