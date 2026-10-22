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
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

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
  <div class="space-y-6">
    <PageHeader title="我的凭据" description="两样东西：一个是订阅链接，一个是每个节点各自的密码。下面分别说明用途和泄露后果。">
      <Button variant="outline" :disabled="subscription.loading.value" @click="subscription.reload()">刷新</Button>
    </PageHeader>

    <div class="space-y-6">
      <LoadingBlock v-if="subscription.loading.value && !subscription.data.value" label="正在读取凭据" />
      <ErrorState v-else-if="subscription.error.value" :error="subscription.error.value" @retry="subscription.reload()" />
      <template v-else-if="subscription.data.value">
        <Card>
          <CardHeader class="flex-row items-center justify-between space-y-0">
            <CardTitle class="text-base font-semibold">订阅链接</CardTitle>
            <Button variant="outline" size="sm" @click="openToken">换一个链接</Button>
          </CardHeader>
          <CardContent class="space-y-2 pt-2">
            <p class="text-sm leading-relaxed">
              这是一个网址。把它粘进客户端（v2rayN / sing-box / Mihomo 等），客户端就会自动拿到你的所有节点。
            </p>
            <p class="text-sm leading-relaxed text-muted-foreground">
              换掉之后旧网址失效，但<strong class="font-medium text-foreground">已经导入客户端的配置照常能用</strong>，正在使用的连接也不会断。
              只在怀疑链接被别人拿到时才需要换。
            </p>
          </CardContent>
          <CardContent class="grid gap-6 pt-4 lg:grid-cols-2">
            <CopyField :value="currentUrl" label="订阅链接" mask warning="这个链接等同于密码，别转发给别人。" />
            <QrPanel :value="currentUrl" />
          </CardContent>
        </Card>

        <Card>
          <CardHeader class="flex-row items-center justify-between space-y-0">
            <CardTitle class="text-base font-semibold">节点密码</CardTitle>
            <Button variant="destructive" size="sm" @click="openCred">全部换掉</Button>
          </CardHeader>
          <CardContent class="space-y-2 pt-2">
            <p class="text-sm leading-relaxed">
              每个节点各有一份密码（通常是 UUID），由面板自动生成并随订阅链接下发，所以这里不显示明文。
            </p>
            <p class="text-sm leading-relaxed text-muted-foreground">
              <strong class="font-medium text-destructive">换掉之后所有设备立刻断开</strong>，已导入的客户端全部失效，必须重新拉取订阅并重新导入才能恢复。
              只在确认密码被别人拿去用、或者要踢掉某台设备时才用。
            </p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader class="border-b">
            <CardTitle class="text-base font-semibold">授权节点</CardTitle>
          </CardHeader>
          <CardContent class="pt-4">
            <LoadingBlock v-if="nodes.loading.value && !nodes.data.value" :rows="2" label="正在读取节点" />
            <ErrorState v-else-if="nodes.error.value" :error="nodes.error.value" @retry="nodes.reload()" />
            <p v-else-if="!nodes.data.value?.length" class="text-sm text-muted-foreground">当前没有授权节点。</p>
            <Table v-else>
              <TableHeader>
                <TableRow>
                  <TableHead>节点</TableHead>
                  <TableHead>协议</TableHead>
                  <TableHead>地址</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow v-for="(node, index) in nodes.data.value" :key="node.id ?? node.node_id ?? index">
                  <TableCell>{{ node.name ?? node.node_id ?? '—' }}</TableCell>
                  <TableCell class="mono">{{ node.protocol ?? '—' }}</TableCell>
                  <TableCell class="mono">
                    {{ node.host ?? '—' }}<template v-if="node.port">:{{ node.port }}</template>
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </CardContent>
        </Card>
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
      <p class="text-sm text-muted-foreground">代理凭据不变，现有连接不会中断。</p>
      <div v-if="rotated" class="space-y-3">
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

      <RadioGroup v-model="scope" class="gap-2">
        <div class="flex items-center gap-2">
          <RadioGroupItem id="cred-scope-all" value="all_nodes" />
          <Label for="cred-scope-all" class="font-normal">全部节点</Label>
        </div>
        <div class="flex items-center gap-2">
          <RadioGroupItem id="cred-scope-single" value="single_node" />
          <Label for="cred-scope-single" class="font-normal">单个节点</Label>
        </div>
      </RadioGroup>

      <div v-if="scope === 'single_node'" class="grid gap-2">
        <Label for="cred-node">节点</Label>
        <!-- 这里刻意保留原生 select：选项值是后端节点 id（可能缺省），
             需要一个空值占位项「选择节点」，reka 的 SelectItem 不接受空字符串值。 -->
        <select
          id="cred-node"
          v-model="nodeId"
          class="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
        >
          <option value="">选择节点</option>
          <option v-for="(node, index) in nodes.data.value ?? []" :key="node.id ?? node.node_id ?? index" :value="node.id ?? node.node_id">
            {{ node.name ?? node.id ?? node.node_id }}
          </option>
        </select>
      </div>

      <Alert v-if="credDone" class="border-sky-200 bg-sky-50 text-sky-800 dark:border-sky-900 dark:bg-sky-950/50 dark:text-sky-200">
        <AlertDescription>轮换已提交；生效以节点回执为准。</AlertDescription>
      </Alert>
    </ConfirmDialog>
  </div>
</template>
