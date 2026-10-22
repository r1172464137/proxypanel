<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { meApi } from '@/api/endpoints'
import type { MeNode, MeSubscription, TokenRotationResult } from '@/api/types'
import { useResource } from '@/lib/async'
import { errorText } from '@/lib/errors'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import CopyField from '@/components/CopyField.vue'
import QrPanel from '@/components/QrPanel.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const subscription = useResource<MeSubscription>(() => meApi.subscription())
const nodes = useResource<MeNode[]>(async () => (await meApi.nodes()).items)

const tokenOpen = ref(false)
const rotated = ref<TokenRotationResult | null>(null)
const tokenError = ref('')
const rotating = ref(false)

const credOpen = ref(false)
const scope = ref<'all_nodes' | 'single_node'>('all_nodes')
const nodeId = ref('')
const credPending = ref(false)
const credError = ref('')
const credDone = ref(false)

onMounted(() => {
  void subscription.reload()
  void nodes.reload()
})

const currentUrl = computed(() => {
  const data = subscription.data.value
  if (!data) return ''
  return data.url.includes('{token}') ? data.url.replace('{token}', encodeURIComponent(data.token)) : data.url
})

async function rotateToken(): Promise<void> {
  rotating.value = true
  tokenError.value = ''
  try {
    rotated.value = await meApi.rotateSubscriptionToken()
    pushToast('success', '订阅令牌已轮换', '旧链接立即失效；代理凭据不变，现有连接不中断。')
    await subscription.reload()
  } catch (caught) {
    tokenError.value = errorText(caught)
  } finally {
    rotating.value = false
  }
}

function openToken(): void {
  rotated.value = null
  tokenError.value = ''
  tokenOpen.value = true
}

function openCred(): void {
  scope.value = 'all_nodes'
  nodeId.value = ''
  credError.value = ''
  credDone.value = false
  credOpen.value = true
}

async function rotateCredentials(): Promise<void> {
  if (scope.value === 'single_node' && !nodeId.value) {
    credError.value = '请选择节点'
    return
  }
  credPending.value = true
  credError.value = ''
  try {
    await meApi.rotateCredentials({
      scope: scope.value,
      ...(scope.value === 'single_node' ? { node_id: nodeId.value } : {}),
    })
    credDone.value = true
    pushToast('success', '凭据轮换已提交', '旧凭据立即失效；请在客户端重新导入订阅（分发为异步）。')
    await subscription.reload()
  } catch (caught) {
    credError.value = errorText(caught)
  } finally {
    credPending.value = false
  }
}
</script>

<template>
  <div>
    <PageHeader title="我的凭据" description="两样东西：一个是订阅链接，一个是每个节点各自的密码。下面分别说明用途和泄露后果。">
      <button type="button" class="btn" :disabled="subscription.loading.value" @click="subscription.reload()">刷新</button>
    </PageHeader>

    <div class="stack">
      <LoadingBlock v-if="subscription.loading.value && !subscription.data.value" label="正在读取凭据" />
      <ErrorState v-else-if="subscription.error.value" :error="subscription.error.value" @retry="subscription.reload()" />
      <template v-else-if="subscription.data.value">
        <section class="card">
          <div class="card__header">
            <h2>订阅链接</h2>
            <div class="card__header-actions">
              <button type="button" class="btn btn--sm" @click="openToken">换一个链接</button>
            </div>
          </div>
          <div class="card__body">
            <p class="text-sm">
              这是一个网址。把它粘进客户端（v2rayN / sing-box / Mihomo 等），客户端就会自动拿到你的所有节点。
            </p>
            <p class="text-sm text-muted">
              换掉之后旧网址失效，但<strong>已经导入客户端的配置照常能用</strong>，正在使用的连接也不会断。
              只在怀疑链接被别人拿到时才需要换。
            </p>
          </div>
          <div class="card__body grid grid--2">
            <div class="stack">
              <CopyField :value="currentUrl" label="订阅链接" mask warning="这个链接等同于密码，别转发给别人。" />
            </div>
            <QrPanel :value="currentUrl" />
          </div>
        </section>

        <section class="card">
          <div class="card__header">
            <h2>节点密码</h2>
            <div class="card__header-actions">
              <button type="button" class="btn btn--sm btn--danger" @click="openCred">全部换掉</button>
            </div>
          </div>
          <div class="card__body">
            <p class="text-sm">
              每个节点各有一份密码（通常是 UUID），由面板自动生成并随订阅链接下发，所以这里不显示明文。
            </p>
            <p class="text-sm text-muted">
              <strong>换掉之后所有设备立刻断开</strong>，已导入的客户端全部失效，必须重新拉取订阅并重新导入才能恢复。
              只在确认密码被别人拿去用、或者要踢掉某台设备时才用。
            </p>
          </div>
        </section>

        <section class="card">
          <div class="card__header"><h2>授权节点</h2></div>
          <LoadingBlock v-if="nodes.loading.value && !nodes.data.value" :rows="2" label="正在读取节点" />
          <ErrorState v-else-if="nodes.error.value" :error="nodes.error.value" @retry="nodes.reload()" />
          <div v-else-if="!nodes.data.value?.length" class="state-block"><p>当前没有授权节点。</p></div>
          <div v-else class="table-wrap">
            <table class="table">
              <thead><tr><th>节点</th><th>协议</th><th>地址</th></tr></thead>
              <tbody>
                <tr v-for="(node, index) in nodes.data.value" :key="node.id ?? node.node_id ?? index">
                  <td>{{ node.name ?? node.node_id ?? '—' }}</td>
                  <td class="mono">{{ node.protocol ?? '—' }}</td>
                  <td class="mono">{{ node.host ?? '—' }}<template v-if="node.port">:{{ node.port }}</template></td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </template>
    </div>

    <ConfirmDialog
      :open="tokenOpen"
      title="轮换订阅链接"
      confirm-label="确认轮换"
      danger
      :pending="rotating"
      :error-text="tokenError"
      @cancel="tokenOpen = false"
      @confirm="rotateToken"
    >
      <p>旧订阅链接会立即失效，需要更新客户端导入链接。</p>
      <p class="text-sm">代理凭据不变，现有连接不会中断。</p>
      <div v-if="rotated" class="stack">
        <CopyField :value="rotated.url" label="新订阅链接" mask />
        <CopyField :value="rotated.token" label="新令牌" mask />
      </div>
    </ConfirmDialog>

    <ConfirmDialog
      :open="credOpen"
      title="轮换代理凭据"
      confirm-label="确认轮换"
      danger
      :pending="credPending"
      :error-text="credError"
      @cancel="credOpen = false"
      @confirm="rotateCredentials"
    >
      <p>旧凭据按硬撤权策略立即失效，受影响节点上的连接会中断，需要重新导入订阅。</p>
      <label class="checkbox"><input v-model="scope" type="radio" value="all_nodes" /> 全部节点</label>
      <label class="checkbox"><input v-model="scope" type="radio" value="single_node" /> 单个节点</label>
      <select v-if="scope === 'single_node'" v-model="nodeId" class="select">
        <option value="">选择节点</option>
        <option v-for="(node, index) in nodes.data.value ?? []" :key="node.id ?? node.node_id ?? index" :value="node.id ?? node.node_id">
          {{ node.name ?? node.id ?? node.node_id }}
        </option>
      </select>
      <p v-if="credDone" class="banner banner--info">轮换已提交；生效以节点回执为准。</p>
    </ConfirmDialog>
  </div>
</template>