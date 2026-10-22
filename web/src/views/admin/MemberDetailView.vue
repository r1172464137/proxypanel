<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { adminApi } from '@/api/endpoints'
import type { MemberRecord, NodeRecord, TokenRotationResult } from '@/api/types'
import { useResource } from '@/lib/async'
import { errorText, toApiError } from '@/lib/errors'
import { formatBytes, formatDateTime, formatQuota, gibToBytes, prettyJson, utcLabel } from '@/lib/format'
import { quotaSource } from '@/lib/memberDraft'
import { pushToast } from '@/lib/toast'
import { accessReasonDetails, describeQuotaSource } from '@/lib/accessReasons'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import AppModal from '@/components/AppModal.vue'
import CopyField from '@/components/CopyField.vue'
import QrPanel from '@/components/QrPanel.vue'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Textarea } from '@/components/ui/textarea'
import { Loader2 } from 'lucide-vue-next'

const route = useRoute()
const memberId = computed(() => String(route.params.id))
const member = useResource<MemberRecord>(() => adminApi.members.get(memberId.value))
const nodes = ref<NodeRecord[]>([])

onMounted(async () => {
  void member.reload()
  try {
    nodes.value = (await adminApi.nodes.list({ limit: 200 })).items
  } catch {
    nodes.value = []
  }
})

const data = computed(() => member.data.value)
const actionError = ref('')
const pending = ref(false)

async function patch(body: Record<string, unknown>, message: string): Promise<void> {
  if (!data.value) return
  actionError.value = ''
  pending.value = true
  try {
    await adminApi.members.update(data.value.id, { ...body, version: data.value.version })
    pushToast('success', message, '规则在面板立即生效；节点分发异步，见任务与同步结果。')
    await member.reload()
  } catch (caught) {
    actionError.value = errorText(caught)
  } finally {
    pending.value = false
  }
}

function toggleActive(): void {
  if (!data.value) return
  void patch({ active: !data.value.active }, data.value.active ? '成员已停用' : '成员已启用')
}

function toggleProxy(): void {
  if (!data.value) return
  void patch({ proxy_enabled: !data.value.proxy_enabled }, data.value.proxy_enabled ? '代理访问已暂停' : '代理访问已恢复')
}

/* 额度调整 */
const adjustOpen = ref(false)
const adjustGib = ref('')
const adjustReason = ref('')
const adjustError = ref('')
const adjusting = ref(false)

function openAdjust(): void {
  adjustGib.value = ''
  adjustReason.value = ''
  adjustError.value = ''
  adjustOpen.value = true
}

async function submitAdjust(): Promise<void> {
  if (!data.value) return
  const gib = Number(adjustGib.value)
  if (!Number.isFinite(gib) || gib === 0) {
    adjustError.value = '请填写非零的调整量（GiB，可为负数）'
    return
  }
  if (!adjustReason.value.trim()) {
    adjustError.value = '必须填写原因（审计要求）'
    return
  }
  const bytes = gibToBytes(Math.abs(gib))
  if (bytes === null) {
    adjustError.value = '调整量无效'
    return
  }
  adjusting.value = true
  adjustError.value = ''
  try {
    await adminApi.members.adjustQuota(data.value.id, { bytes: gib > 0 ? bytes : -bytes, reason: adjustReason.value.trim() })
    pushToast('success', '额度调整已记录', '调整不修改原始用量；超额判定按账本重新计算。')
    adjustOpen.value = false
    await member.reload()
  } catch (caught) {
    adjustError.value = errorText(caught)
  } finally {
    adjusting.value = false
  }
}

/* 凭据轮换 */
const credOpen = ref(false)
const credScope = ref<'all_nodes' | 'single_node'>('all_nodes')
const credNodeId = ref('')
const credError = ref('')
const credPending = ref(false)

async function submitCredential(): Promise<void> {
  if (!data.value) return
  if (credScope.value === 'single_node' && !credNodeId.value) {
    credError.value = '请选择节点'
    return
  }
  credPending.value = true
  credError.value = ''
  try {
    await adminApi.members.rotateCredentials(data.value.id, {
      scope: credScope.value,
      ...(credScope.value === 'single_node' ? { node_id: credNodeId.value } : {}),
    })
    pushToast('success', '凭据轮换已提交', '旧凭据按硬撤权策略失效；回执见任务页。')
    credOpen.value = false
  } catch (caught) {
    credError.value = errorText(caught)
  } finally {
    credPending.value = false
  }
}

/* 订阅令牌 */
const tokenOpen = ref(false)
const tokenResult = ref<TokenRotationResult | null>(null)
const tokenError = ref('')
const tokenLoading = ref(false)

async function rotateToken(): Promise<void> {
  if (!data.value) return
  tokenOpen.value = true
  tokenResult.value = null
  tokenError.value = ''
  tokenLoading.value = true
  try {
    tokenResult.value = await adminApi.members.rotateSubscriptionToken(data.value.id)
    pushToast('success', '订阅令牌已轮换', '旧链接失效；代理凭据与现有连接不受影响。')
  } catch (caught) {
    tokenError.value = toApiError(caught).message
  } finally {
    tokenLoading.value = false
  }
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader
      :title="data ? `成员 · ${data.username}` : '成员详情'"
      description="生效值与来源、访问原因、用量与周期；密码/哈希永远不在响应中。"
    >
      <Button as-child variant="outline">
        <router-link to="/admin/members">返回成员列表</router-link>
      </Button>
      <Button variant="outline" :disabled="member.loading.value" @click="member.reload()">刷新</Button>
      <template v-if="data">
        <Button variant="outline" :disabled="pending" @click="toggleActive">{{ data.active ? '停用账户' : '启用账户' }}</Button>
        <Button variant="outline" :disabled="pending" @click="toggleProxy">
          {{ data.proxy_enabled ? '暂停代理访问' : '恢复代理访问' }}
        </Button>
        <Button variant="outline" @click="openAdjust">额度调整</Button>
        <Button variant="outline" @click="credOpen = true">轮换凭据</Button>
        <Button variant="outline" @click="rotateToken">轮换订阅链接</Button>
      </template>
    </PageHeader>

    <LoadingBlock v-if="member.loading.value && !data" label="正在读取成员" />
    <ErrorState v-else-if="member.error.value" :error="member.error.value" @retry="member.reload()" />
    <div v-else-if="data" class="space-y-4">
      <Alert v-if="actionError" variant="destructive">
        <AlertTitle>操作失败</AlertTitle>
        <AlertDescription class="break-all">{{ actionError }}</AlertDescription>
      </Alert>

      <div class="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle class="text-base">状态</CardTitle>
          </CardHeader>
          <CardContent>
            <dl class="grid grid-cols-[6.5rem_1fr] gap-x-4 gap-y-3 text-sm">
              <dt class="text-muted-foreground">用户名</dt>
              <dd>
                {{ data.username }}
                <span class="ml-1 font-mono text-xs text-muted-foreground">{{ data.id }}</span>
              </dd>
              <dt class="text-muted-foreground">角色</dt>
              <dd>{{ data.role === 'admin' ? '管理员' : '成员' }}</dd>
              <dt class="text-muted-foreground">账户状态</dt>
              <dd><StatusBadge :status="data.active ? 'active' : 'suspended'" /></dd>
              <dt class="text-muted-foreground">代理访问</dt>
              <dd class="flex flex-wrap items-center gap-2">
                <StatusBadge :status="data.proxy_access ?? (data.proxy_enabled ? 'enabled' : 'paused_by_admin')" />
                <Badge v-if="data.over_quota" variant="destructive">已超额</Badge>
              </dd>
              <dt class="text-muted-foreground">访问原因</dt>
              <dd>
                <template v-if="accessReasonDetails(data.access_reasons).length">
                  <div
                    v-for="reason in accessReasonDetails(data.access_reasons)"
                    :key="reason.label"
                    class="space-y-0.5 py-1 first:pt-0 last:pb-0"
                  >
                    <strong class="block font-medium">{{ reason.label }}</strong>
                    <span class="text-sm text-muted-foreground">{{ reason.action }}</span>
                  </div>
                </template>
                <span v-else class="text-muted-foreground">没有限制，权限正常</span>
              </dd>
              <dt class="text-muted-foreground">最后更新</dt>
              <dd>{{ formatDateTime(data.updated_at) }}</dd>
            </dl>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle class="text-base">用量与周期</CardTitle>
          </CardHeader>
          <CardContent>
            <dl class="grid grid-cols-[6.5rem_1fr] gap-x-4 gap-y-3 text-sm">
              <dt class="text-muted-foreground">订阅</dt>
              <dd class="font-mono">{{ data.subscription_id ?? '未绑定' }}</dd>
              <dt class="text-muted-foreground">有效期</dt>
              <dd>
                <span :title="utcLabel(data.expires_at)">{{ formatDateTime(data.expires_at) }}</span>
              </dd>
              <dt class="text-muted-foreground">已用 / 额度</dt>
              <dd class="tabular-nums">
                {{ formatBytes(data.used_bytes) }} / {{ formatQuota(data.effective_quota_bytes) }}
                <span class="mt-0.5 block text-xs text-muted-foreground">额度来源：{{ describeQuotaSource(quotaSource(data)) }}</span>
              </dd>
              <dt class="text-muted-foreground">配额覆盖</dt>
              <dd>
                <template v-if="data.quota_override_enabled">
                  已启用（{{ formatQuota(data.quota_override_bytes) }}）· 原订阅默认 {{ formatQuota(data.effective_quota_bytes) }}
                </template>
                <template v-else>未启用（继承订阅默认）</template>
              </dd>
              <dt class="text-muted-foreground">当前周期</dt>
              <dd>
                <template v-if="data.period_start || data.period_end">
                  {{ formatDateTime(data.period_start, data.period_timezone) }} → {{ formatDateTime(data.period_end, data.period_timezone) }}
                  <span class="mt-0.5 block text-xs text-muted-foreground">
                    UTC 存储；业务时区由订阅决定，接口未返回时不做推断
                  </span>
                </template>
                <span v-else class="text-muted-foreground">未提供</span>
              </dd>
            </dl>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle class="text-base">原始响应（排障用）</CardTitle>
        </CardHeader>
        <CardContent>
          <details>
            <summary class="cursor-pointer text-sm text-muted-foreground">展开服务端返回的字段</summary>
            <pre class="mt-2 overflow-x-auto rounded-md border border-border bg-muted p-3 font-mono text-xs">{{ prettyJson(data) }}</pre>
          </details>
        </CardContent>
      </Card>
    </div>

    <AppModal :open="adjustOpen" title="额度调整（审计化）" size="sm" @close="adjustOpen = false">
      <div class="space-y-4">
        <p class="text-sm text-muted-foreground">
          调整当期额度，不修改原始用量，也不提供“立即改周期并清零”。正数增加额度，负数扣减。
        </p>
        <div class="space-y-1.5">
          <Label for="adjust-gib">调整量（GiB）</Label>
          <Input id="adjust-gib" v-model="adjustGib" inputmode="decimal" placeholder="如 50 或 -20" />
        </div>
        <div class="space-y-1.5">
          <Label for="adjust-reason">原因（必填）</Label>
          <Textarea id="adjust-reason" v-model="adjustReason" class="min-h-[70px]" />
        </div>
        <Alert v-if="adjustError" variant="destructive">
          <AlertDescription>{{ adjustError }}</AlertDescription>
        </Alert>
      </div>
      <template #footer>
        <Button variant="outline" :disabled="adjusting" @click="adjustOpen = false">取消</Button>
        <Button :disabled="adjusting" @click="submitAdjust">
          <Loader2 v-if="adjusting" class="animate-spin" aria-hidden="true" />
          提交
        </Button>
      </template>
    </AppModal>

    <AppModal :open="credOpen" title="代理凭据轮换" size="sm" @close="credOpen = false">
      <div class="space-y-4">
        <p class="text-sm text-muted-foreground">旧凭据立即失效（硬撤权），可能中断连接。</p>
        <RadioGroup
          :model-value="credScope"
          @update:model-value="(value) => (credScope = value === 'single_node' ? 'single_node' : 'all_nodes')"
        >
          <div class="flex items-center gap-2">
            <RadioGroupItem id="cred-scope-all" value="all_nodes" />
            <Label for="cred-scope-all" class="font-normal">全部节点</Label>
          </div>
          <div class="flex items-center gap-2">
            <RadioGroupItem id="cred-scope-single" value="single_node" />
            <Label for="cred-scope-single" class="font-normal">单个节点</Label>
          </div>
        </RadioGroup>
        <div v-if="credScope === 'single_node'" class="space-y-1.5">
          <Label for="cred-node">节点</Label>
          <select
            id="cred-node"
            v-model="credNodeId"
            class="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          >
            <option value="">选择节点</option>
            <option v-for="node in nodes" :key="node.id" :value="node.id">{{ node.name }}</option>
          </select>
        </div>
        <Alert v-if="credError" variant="destructive">
          <AlertDescription>{{ credError }}</AlertDescription>
        </Alert>
      </div>
      <template #footer>
        <Button variant="outline" :disabled="credPending" @click="credOpen = false">取消</Button>
        <Button :disabled="credPending" @click="submitCredential">
          <Loader2 v-if="credPending" class="animate-spin" aria-hidden="true" />
          确认轮换
        </Button>
      </template>
    </AppModal>

    <AppModal :open="tokenOpen" title="订阅令牌轮换" size="md" @close="tokenOpen = false">
      <div class="space-y-4">
        <p v-if="tokenLoading" class="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 class="animate-spin" aria-hidden="true" /> 轮换中…
        </p>
        <Alert v-if="tokenError" variant="destructive">
          <AlertDescription class="break-all">{{ tokenError }}</AlertDescription>
        </Alert>
        <template v-if="tokenResult">
          <CopyField :value="tokenResult.url" label="订阅链接" mask warning="此链接等同于密码。" />
          <CopyField :value="tokenResult.token" label="令牌" mask />
          <QrPanel :value="tokenResult.url" />
        </template>
      </div>
      <template #footer>
        <Button variant="outline" @click="tokenOpen = false">关闭</Button>
      </template>
    </AppModal>
  </div>
</template>
