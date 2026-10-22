<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import { meApi } from '@/api/endpoints'
import type { MeNode, MeStatus, MeSubscription, SubscriptionFormat, TokenRotationResult } from '@/api/types'
import { SUBSCRIPTION_FORMATS, SUBSCRIPTION_FORMAT_LABELS } from '@/api/types'
import { useResource } from '@/lib/async'
import { errorText } from '@/lib/errors'
import { formatBytes, formatDateTime, formatQuota } from '@/lib/format'
import { pushToast } from '@/lib/toast'
import { accessReasonDetails } from '@/lib/accessReasons'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import AppModal from '@/components/AppModal.vue'
import CopyField from '@/components/CopyField.vue'
import QrPanel from '@/components/QrPanel.vue'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const subscription = useResource<MeSubscription>(() => meApi.subscription())
const status = useResource<MeStatus>(() => meApi.status())
const nodes = useResource<MeNode[]>(async () => (await meApi.nodes()).items)

const format = ref<SubscriptionFormat>('xray')
const checkState = ref<'idle' | 'checking' | 'ok' | 'failed'>('idle')
const checkMessage = ref('')

onMounted(async () => {
  await Promise.all([subscription.reload(), status.reload(), nodes.reload()])
  const declared = subscription.data.value?.formats
  if (declared?.length && !declared.includes(format.value)) format.value = declared[0]!
})

const declaredFormats = computed<SubscriptionFormat[]>(() => {
  const formats = subscription.data.value?.formats
  return Array.isArray(formats) ? formats : []
})

const subscriptionUrl = computed(() => {
  const data = subscription.data.value
  if (!data?.url) return ''
  const base = data.url.includes('{token}') ? data.url.replace('{token}', encodeURIComponent(data.token)) : data.url
  try {
    const url = new URL(base, window.location.origin)
    url.searchParams.set('format', format.value)
    return url.toString()
  } catch {
    return base
  }
})

const quota = computed(() => {
  const data = status.data.value
  const member = data?.member
  const access = data?.access
  const effective = access?.effective_quota_bytes ?? member?.effective_quota_bytes ?? null
  return {
    used: access?.used_bytes ?? member?.used_bytes,
    quota: effective,
    unlimited: effective === null,
    start: member?.period_start,
    end: member?.period_end,
    timezone: member?.period_timezone,
  }
})

// 无代理权限时，订阅链接本身仍然存在，但拉取必然返回
// 403 {"code":"FORBIDDEN","message":"no proxy access: no_binding"}。
// 界面上继续展示"复制 / 二维码 / 打开"会让用户复制一个必然失败的链接，
// 所以这种情况下只保留信息展示与原因说明。
//
// 判定只依赖 /me/status 明确返回 access.access === false：
// 请求失败或字段缺失时保持原行为，避免把网络故障误报成"无权限"。
const accessDenied = computed(() => {
  const access = status.data.value?.access
  return typeof access?.access === 'boolean' && access.access === false
})

// 原因码 → 人话。映射放在共享模块里，全站统一，不在各视图各写一份。
const deniedReasons = computed(() => {
  const access = status.data.value?.access
  const codes = access?.access_reasons ?? []
  const raw = codes.length ? codes : access?.proxy_access ? [access.proxy_access] : []
  return accessReasonDetails(raw)
})

watch(format, () => {
  checkState.value = 'idle'
  checkMessage.value = ''
})

async function checkFormat(): Promise<void> {
  checkState.value = 'checking'
  checkMessage.value = ''
  try {
    const response = await fetch(subscriptionUrl.value, { credentials: 'same-origin', headers: { Accept: 'text/plain' } })
    if (!response.ok) {
      let message = `该格式不可用（HTTP ${response.status}）`
      const text = await response.text()
      try {
        const parsed = JSON.parse(text) as { error?: { message?: string; code?: string } }
        if (parsed.error?.message) message = `${parsed.error.message}${parsed.error.code ? `（${parsed.error.code}）` : ''}`
      } catch {
        if (text && !text.trim().startsWith('<')) message = `${message}：${text.slice(0, 160)}`
      }
      checkState.value = 'failed'
      checkMessage.value = message
      return
    }
    const body = await response.text()
    checkState.value = 'ok'
    checkMessage.value = `该格式可获取，共 ${formatBytes(new Blob([body]).size)}（内容包含你的凭据，本页不展示）。`
  } catch {
    checkState.value = 'failed'
    checkMessage.value = '无法获取订阅输出：网络错误或面板不可达。'
  }
}

const tokenOpen = ref(false)
const rotating = ref(false)
const rotateError = ref('')
const rotated = ref<TokenRotationResult | null>(null)

async function rotate(): Promise<void> {
  rotating.value = true
  rotateError.value = ''
  try {
    rotated.value = await meApi.rotateSubscriptionToken()
    pushToast('success', '订阅链接已轮换', '旧链接立即失效；已导入的客户端配置（代理凭据）不受影响。')
    await subscription.reload()
  } catch (caught) {
    rotateError.value = errorText(caught)
  } finally {
    rotating.value = false
  }
}

function openRotate(): void {
  rotated.value = null
  rotateError.value = ''
  tokenOpen.value = true
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="我的订阅" description="订阅链接是个人凭据：可复制、扫码或按客户端格式获取；链接与代理凭据相互独立。">
      <Button variant="outline" :disabled="subscription.loading.value" @click="subscription.reload()">刷新</Button>
      <Button variant="destructive" @click="openRotate">轮换订阅链接</Button>
    </PageHeader>

    <div class="space-y-6">
      <LoadingBlock v-if="subscription.loading.value && !subscription.data.value" label="正在读取订阅" />
      <ErrorState v-else-if="subscription.error.value" :error="subscription.error.value" @retry="subscription.reload()" />

      <template v-else-if="subscription.data.value">
        <Card>
          <CardHeader class="border-b">
            <CardTitle class="text-base font-semibold">订阅链接</CardTitle>
          </CardHeader>

          <!-- 无代理权限：链接仍然存在，但拉取会 403。不展示复制/二维码，
               避免用户拿到一个必然失败的地址。 -->
          <CardContent v-if="accessDenied" class="space-y-4 pt-6">
            <Alert variant="destructive">
              <AlertTitle>当前无代理权限，订阅链接暂不可用。</AlertTitle>
              <AlertDescription>
                现在打开该链接会被服务端拒绝（403 no proxy access），客户端也无法拉取配置。
                请联系管理员恢复权限后再使用。
              </AlertDescription>
            </Alert>

            <ul v-if="deniedReasons.length" class="space-y-2">
              <li v-for="reason in deniedReasons" :key="reason.label" class="text-sm">
                <strong class="font-medium">{{ reason.label }}</strong>。{{ reason.action }}
              </li>
            </ul>

            <div class="flex flex-wrap items-center gap-2">
              <Button as-child variant="outline">
                <router-link to="/me/status">查看账户状态</router-link>
              </Button>
              <Button as-child variant="outline">
                <router-link to="/me/usage">查看用量</router-link>
              </Button>
            </div>
          </CardContent>

          <CardContent v-else class="grid gap-6 pt-6 lg:grid-cols-2">
            <div class="space-y-4">
              <div class="grid gap-2">
                <Label for="format">客户端格式</Label>
                <Select v-model="format">
                  <SelectTrigger id="format" class="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem
                      v-for="option in SUBSCRIPTION_FORMATS"
                      :key="option"
                      :value="option"
                      :disabled="!declaredFormats.includes(option)"
                    >
                      {{ SUBSCRIPTION_FORMAT_LABELS[option] }}{{ declaredFormats.includes(option) ? '' : '（服务端未声明）' }}
                    </SelectItem>
                  </SelectContent>
                </Select>
                <p class="text-xs leading-relaxed text-muted-foreground">
                  仅可选择服务端声明的格式。若某节点参数无法被所选客户端表示，订阅输出会返回 422 并指明不兼容节点与字段，不会静默删参。
                </p>
              </div>

              <CopyField :value="subscriptionUrl" label="订阅链接" mask warning="此链接等同于密码；泄露后请立即轮换。" />

              <div class="flex flex-wrap items-center gap-2">
                <Button as-child variant="outline">
                  <a :href="subscriptionUrl" target="_blank" rel="noopener noreferrer">打开 / 下载</a>
                </Button>
                <Button type="button" variant="outline" :disabled="checkState === 'checking'" @click="checkFormat">
                  <Loader2 v-if="checkState === 'checking'" class="animate-spin" aria-hidden="true" />
                  {{ checkState === 'checking' ? '校验中…' : '校验该格式' }}
                </Button>
              </div>

              <p
                v-if="checkState === 'ok'"
                role="status"
                class="rounded-md border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950/50 dark:text-emerald-200"
              >
                {{ checkMessage }}
              </p>
              <Alert v-if="checkState === 'failed'" variant="destructive">
                <AlertDescription>{{ checkMessage }}</AlertDescription>
              </Alert>
              <p
                v-if="!declaredFormats.length"
                class="rounded-md border border-amber-300/70 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950/50 dark:text-amber-200"
              >
                服务端未声明任何格式，格式选择可能不可用；请联系管理员确认订阅输出能力。
              </p>
            </div>

            <QrPanel :value="subscriptionUrl" />
          </CardContent>
        </Card>

        <Card>
          <CardHeader class="flex-row items-center justify-between space-y-0">
            <CardTitle class="text-base font-semibold">我的额度</CardTitle>
            <Button as-child variant="outline" size="sm">
              <router-link to="/me/usage">查看用量</router-link>
            </Button>
          </CardHeader>
          <CardContent class="space-y-4 pt-2">
            <LoadingBlock v-if="status.loading.value && !status.data.value" :rows="2" label="正在读取额度" />
            <ErrorState v-else-if="status.error.value" :error="status.error.value" @retry="status.reload()" />
            <div v-else class="space-y-4">
              <dl class="grid gap-4 sm:grid-cols-2">
                <div class="space-y-1">
                  <dt class="text-xs font-medium text-muted-foreground">账户 / 代理</dt>
                  <dd class="flex flex-wrap items-center gap-2">
                    <StatusBadge :status="status.data.value?.member?.active === false ? 'suspended' : 'active'" />
                    <StatusBadge :status="status.data.value?.access?.proxy_access ?? 'unknown'" />
                  </dd>
                </div>
                <div class="space-y-1">
                  <dt class="text-xs font-medium text-muted-foreground">已用 / 额度</dt>
                  <dd class="tabular text-sm">
                    {{ formatBytes(quota.used) }} /
                    {{ quota.unlimited ? '不限量（仍受节点与规则限制）' : formatQuota(quota.quota) }}
                  </dd>
                </div>
                <div class="space-y-1">
                  <dt class="text-xs font-medium text-muted-foreground">当前周期</dt>
                  <dd class="text-sm">
                    <template v-if="quota.start || quota.end">
                      {{ formatDateTime(quota.start, quota.timezone) }} → {{ formatDateTime(quota.end, quota.timezone) }}
                    </template>
                    <span v-else class="text-muted-foreground">未提供</span>
                  </dd>
                </div>
                <div class="space-y-1">
                  <dt class="text-xs font-medium text-muted-foreground">到期时间</dt>
                  <dd class="text-sm">
                    {{ formatDateTime(status.data.value?.access?.expires_at ?? status.data.value?.member?.expires_at) }}
                  </dd>
                </div>
              </dl>

              <Alert
                v-if="accessReasonDetails(status.data.value?.access?.access_reasons).length"
                class="border-amber-300/70 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950/50 dark:text-amber-200"
              >
                <AlertTitle>现在还不能使用代理</AlertTitle>
                <AlertDescription>
                  <ul class="list-disc space-y-1 pl-4">
                    <li
                      v-for="reason in accessReasonDetails(status.data.value?.access?.access_reasons)"
                      :key="reason.label"
                    >
                      {{ reason.label }}。{{ reason.action }}
                    </li>
                  </ul>
                </AlertDescription>
              </Alert>
            </div>
          </CardContent>
        </Card>
      </template>

      <EmptyState
        v-else
        title="当前没有可用订阅"
        description="你的账户尚未绑定订阅（或订阅被暂停）。可以正常登录查看状态；如需代理权限，请联系管理员绑定订阅。"
      >
        <Button as-child variant="outline">
          <router-link to="/me/status">查看账户状态</router-link>
        </Button>
      </EmptyState>

      <Card>
        <CardHeader class="flex-row items-center justify-between space-y-0">
          <CardTitle class="text-base font-semibold">授权节点</CardTitle>
          <Button variant="outline" size="sm" :disabled="nodes.loading.value" @click="nodes.reload()">刷新</Button>
        </CardHeader>
        <CardContent class="pt-2">
          <LoadingBlock v-if="nodes.loading.value && !nodes.data.value" :rows="2" label="正在读取节点" />
          <ErrorState v-else-if="nodes.error.value" :error="nodes.error.value" @retry="nodes.reload()" />
          <p v-else-if="!nodes.data.value?.length" class="text-sm text-muted-foreground">
            没有可用节点。常见原因：尚未绑定订阅、订阅暂停、节点维护，或账户过期/超额。
          </p>
          <Table v-else>
            <TableHeader>
              <TableRow>
                <TableHead>节点</TableHead>
                <TableHead>协议</TableHead>
                <TableHead>地址</TableHead>
                <TableHead>传输 / 安全</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="(node, index) in nodes.data.value" :key="node.id ?? node.node_id ?? index">
                <TableCell>{{ node.name ?? node.node_id ?? '—' }}</TableCell>
                <TableCell class="mono">{{ node.protocol ?? '—' }}</TableCell>
                <TableCell class="mono">
                  {{ node.host ?? '—' }}<template v-if="node.port">:{{ node.port }}</template>
                </TableCell>
                <TableCell>
                  <span class="block text-sm">{{ node.network ?? '—' }}</span>
                  <span class="block text-xs text-muted-foreground">{{ node.security ?? '—' }}</span>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>

    <AppModal :open="tokenOpen" title="轮换订阅链接" size="md" @close="tokenOpen = false">
      <div class="space-y-3">
        <p class="text-sm">轮换后旧订阅链接立即失效；代理凭据不变，现有连接不中断。</p>
        <Alert v-if="rotateError" variant="destructive">
          <AlertDescription class="break-all">{{ rotateError }}</AlertDescription>
        </Alert>
        <template v-if="rotated">
          <CopyField :value="rotated.url" label="新订阅链接" mask warning="此链接等同于密码，请更新到你的客户端。" />
          <CopyField :value="rotated.token" label="新令牌" mask />
          <QrPanel v-if="!accessDenied" :value="rotated.url" />
          <p
            v-else
            class="rounded-md border border-amber-300/70 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950/50 dark:text-amber-200"
          >
            该账户当前无代理权限，新链接同样会返回 403；请先联系管理员恢复权限。
          </p>
        </template>
      </div>
      <template #footer>
        <Button variant="outline" :disabled="rotating" @click="tokenOpen = false">关闭</Button>
        <Button variant="destructive" :disabled="rotating" @click="rotate">
          <Loader2 v-if="rotating" class="animate-spin" aria-hidden="true" />
          {{ rotating ? '轮换中…' : '确认轮换' }}
        </Button>
      </template>
    </AppModal>
  </div>
</template>
