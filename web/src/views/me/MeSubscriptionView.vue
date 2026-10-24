<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { meApi } from '@/api/endpoints'
import type { SubscriptionFormat } from '@/api/types'
import { useResource } from '@/lib/async'
import { formatBytes, formatDateTime, formatQuota } from '@/lib/format'
import { MEMBER_CLIENTS, formatAvailability, memberSubscriptionUrl, memberSummary } from '@/lib/memberService'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import CopyField from '@/components/CopyField.vue'
import QrPanel from '@/components/QrPanel.vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

const status = useResource(() => meApi.status())
const subscription = useResource(() => meApi.subscription())
const nodes = useResource(() => meApi.nodes())
const summary = computed(() => memberSummary(status.data.value))
const format = ref<SubscriptionFormat>('v2rayn')
const client = computed(() => MEMBER_CLIENTS.find(item => item.format === format.value)!)
const availability = computed(() => formatAvailability(subscription.data.value, format.value))
const url = computed(() => memberSubscriptionUrl(subscription.data.value, format.value, window.location.origin))
const checking = ref(false)
const checkedUrl = ref('')
const rejectedUrl = ref('')
const checkMessage = ref('')
const canCheck = computed(() => status.data.value?.access?.access === true && availability.value.available !== false && !!url.value)
const canImport = computed(() => canCheck.value && rejectedUrl.value !== url.value && (availability.value.available === true || checkedUrl.value === url.value))
const timezone = computed(() => summary.value.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone)
let checkGeneration = 0
watch([url, () => status.data.value, () => subscription.data.value], () => {
  checkGeneration++
  checkedUrl.value = ''
  rejectedUrl.value = ''
  checkMessage.value = ''
  checking.value = false
})

async function refresh() {
  await Promise.all([status.reload(), subscription.reload(), nodes.reload()])
}
onMounted(async () => {
  await refresh()
  const first = MEMBER_CLIENTS.find(item => formatAvailability(subscription.data.value, item.format).available === true)
    ?? MEMBER_CLIENTS.find(item => subscription.data.value?.formats.includes(item.format))
  if (first) format.value = first.format
})

async function retrieve(download = false) {
  if (!canCheck.value || checking.value) return
  const generation = ++checkGeneration
  const target = url.value
  const selected = format.value
  checking.value = true
  checkMessage.value = ''
  try {
    const response = await fetch(target, { credentials: 'omit', referrerPolicy: 'no-referrer', headers: { Accept: 'text/plain, application/json, application/yaml' } })
    if (!response.ok) {
      const message = response.status === 422 ? '此格式无法表达当前节点配置，请更换客户端或联系管理员。'
        : response.status === 403 ? '当前授权不允许获取配置，请刷新服务状态或联系管理员。'
        : response.status === 409 ? '配置暂不可获取，请稍后重试或联系管理员核对维护状态。'
        : '暂时无法获取配置，请稍后重试。'
      throw new Error(message)
    }
    const body = await response.text()
    if (!body.trim() || /^\s*</.test(body) || response.headers.get('content-type')?.includes('text/html')) throw new Error('未获取到有效配置，请联系管理员核对服务地址。')
    if (generation !== checkGeneration) return
    checkedUrl.value = target
    rejectedUrl.value = ''
    checkMessage.value = '配置已取回；授权状态请以上方摘要为准。尚未验证真实连接，请在客户端选择节点并连接后检查访问。'
    if (download) {
      const blobUrl = URL.createObjectURL(new Blob([body], { type: 'application/octet-stream' }))
      const anchor = document.createElement('a')
      anchor.href = blobUrl
      anchor.download = `my-service.${selected === 'mihomo' ? 'yaml' : selected === 'v2rayn' ? 'txt' : 'json'}`
      anchor.click()
      window.setTimeout(() => URL.revokeObjectURL(blobUrl), 1000)
      checkMessage.value = '配置已取回并发起下载，请在客户端载入。尚未验证真实连接；配置文件包含个人连接信息，请勿转发。'
    }
  } catch (error) {
    if (generation !== checkGeneration) return
    checkedUrl.value = ''
    rejectedUrl.value = target
    checkMessage.value = error instanceof TypeError ? '网络连接失败，请稍后重试。' : error instanceof Error ? error.message : '无法获取配置，请重试。'
  } finally {
    if (generation === checkGeneration) checking.value = false
  }
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="我的服务" description="查看服务状态与剩余额度，然后将配置导入你使用的客户端。">
      <Button data-testid="service-refresh-button" variant="outline" :disabled="status.loading.value || subscription.loading.value" @click="refresh">刷新服务资料</Button>
    </PageHeader>

    <section aria-label="服务状态与额度" data-testid="service-summary-card">
      <LoadingBlock v-if="status.loading.value && !status.data.value" label="正在读取服务状态" />
      <ErrorState v-else-if="status.error.value" :error="status.error.value" @retry="status.reload()" />
      <Card v-else>
        <CardHeader><CardTitle>{{ summary.title }}</CardTitle></CardHeader>
        <CardContent class="space-y-4">
          <p v-if="summary.incomplete" role="status" class="text-sm text-amber-700 dark:text-amber-300">部分服务资料需核对，以下保留已知数据。未知字段显示为「—」，不代表零或不限量。</p>
          <p v-if="status.data.value?.access?.access === true" class="text-sm text-muted-foreground">当前授权允许使用；真实连接需在客户端确认。</p>
          <ul v-if="summary.reasons.length" class="space-y-2 text-sm"><li v-for="reason in summary.reasons" :key="reason.label"><strong>{{ reason.label }}</strong>。{{ reason.action }}</li></ul>
          <dl class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <div><dt class="text-sm text-muted-foreground">已用</dt><dd class="text-xl font-semibold">{{ formatBytes(summary.used) }}</dd></div>
            <div><dt class="text-sm text-muted-foreground">总额度</dt><dd class="text-xl font-semibold">{{ summary.total === undefined ? '—' : formatQuota(summary.total) }}</dd></div>
            <div><dt class="text-sm text-muted-foreground">剩余</dt><dd class="text-xl font-semibold">{{ summary.unlimited ? '不限量' : formatBytes(summary.remaining) }}</dd></div>
            <div><dt class="text-sm text-muted-foreground">有效期至</dt><dd>{{ summary.expires === null ? '未设置到期时间' : formatDateTime(summary.expires, timezone) }}</dd></div>
            <div><dt class="text-sm text-muted-foreground">下次重置</dt><dd>{{ formatDateTime(summary.end, timezone) }}</dd></div>
            <div><dt class="text-sm text-muted-foreground">当前周期开始</dt><dd>{{ formatDateTime(summary.start, timezone) }}</dd></div>
          </dl>
          <p class="text-xs text-muted-foreground">数据更新时间：暂未提供。时间按 {{ timezone }} 显示；未知数据不是零，不限量仍受有效期与授权范围限制。</p>
          <div class="flex flex-wrap gap-3 text-sm underline"><router-link data-testid="nav-service-status" to="/me/status">查看服务状态与原因</router-link><router-link data-testid="nav-service-usage" to="/me/usage">查看用量明细</router-link></div>
        </CardContent>
      </Card>
    </section>

    <Card data-testid="client-import-card">
      <CardHeader><CardTitle>导入客户端</CardTitle></CardHeader>
      <CardContent class="space-y-4">
        <LoadingBlock v-if="subscription.loading.value" label="正在读取导入资料" />
        <div v-else-if="subscription.error.value?.code === 'RECOVERY_MODE'" data-testid="import-maintenance-message" role="status" class="space-y-3 text-sm">
          <p>恢复核对进行中，暂时无法获取订阅链接。上方已读取的状态与额度仍可查看，请稍后重试。</p>
          <Button data-testid="import-retry-button" variant="outline" @click="subscription.reload()">重试获取导入资料</Button>
        </div>
        <ErrorState v-else-if="subscription.error.value" :error="subscription.error.value" @retry="subscription.reload()" />
        <p v-else-if="status.data.value?.access?.access !== true" role="status">{{ status.data.value?.access?.access === false ? '当前服务不可用，暂不能导入。' : '服务状态尚未确认，请先刷新服务资料。' }}请联系为你开通服务的管理员，账号与用量页面仍可访问。</p>
        <template v-else>
          <div class="space-y-2">
            <label for="member-client" class="text-sm font-medium">你使用的客户端</label>
            <select id="member-client" v-model="format" data-testid="client-select" class="block w-full rounded-md border border-border bg-background p-2 text-sm">
              <option v-for="item in MEMBER_CLIENTS" :key="item.format" :value="item.format">{{ item.name }}</option>
            </select>
            <p role="status" class="text-sm text-muted-foreground">{{ availability.message }}</p>
            <p class="text-xs text-muted-foreground">以下为手动导入指引，不代表客户端版本已通过兼容认证。不提供未经验证的一键唤起。</p>
          </div>
          <template v-if="canImport">
            <CopyField :value="url" label="个人订阅链接" mask warning="仅供本人使用。复制不会重置链接，也不会更改连接凭据。" />
            <details data-testid="import-qrcode-panel"><summary class="cursor-pointer text-sm">显示二维码（仅供本人使用）</summary><QrPanel :value="url" /></details>
          </template>
          <div class="flex flex-wrap gap-2">
            <Button data-testid="import-check-button" variant="outline" :disabled="!canCheck || checking" @click="retrieve()">{{ checking ? '正在获取配置…' : '检查配置能否获取' }}</Button>
            <Button v-if="canImport" data-testid="import-download-button" variant="outline" :disabled="checking" @click="retrieve(true)">下载配置文件</Button>
          </div>
          <p v-if="checkMessage" data-testid="import-result-message" role="status" class="text-sm">{{ checkMessage }}</p>
          <ol class="list-decimal space-y-2 pl-5 text-sm">
            <li>安装并打开所选客户端；确认其支持当前配置格式。</li>
            <li>{{ client.instruction }}</li>
            <li>更新订阅，在客户端选择下方同名节点，然后开启连接。</li>
            <li>在客户端查看连接日志并检查实际访问。取回配置、复制或扫码均不代表连接已验证。</li>
          </ol>
        </template>
      </CardContent>
    </Card>

    <Card data-testid="service-nodes-card">
      <CardHeader><CardTitle>我的节点</CardTitle></CardHeader>
      <CardContent>
        <LoadingBlock v-if="nodes.loading.value" label="正在读取节点" />
        <ErrorState v-else-if="nodes.error.value" :error="nodes.error.value" @retry="nodes.reload()" />
        <ul v-else-if="nodes.data.value?.items.length" class="space-y-2 text-sm"><li v-for="(node, index) in nodes.data.value.items" :key="node.id ?? index" class="break-words">{{ node.name ?? '未命名节点' }} · {{ node.protocol ?? '协议未提供' }}</li></ul>
        <p v-else class="text-sm text-muted-foreground">暂无可用节点。请先查看服务状态，或联系为你开通服务的管理员。</p>
        <p v-if="nodes.data.value?.next_cursor" class="mt-2 text-sm">这里只显示部分节点，导入配置后请在客户端查看完整列表。</p>
      </CardContent>
    </Card>
    <p class="text-xs text-muted-foreground">重置订阅链接会使旧链接无法继续下载配置，不等于立即切断现有连接；重置连接凭据会改变所选节点的连接信息，需要重新导入并等待节点更新。请在高级连接信息中核对影响后操作。</p>
    <div class="flex flex-wrap gap-4 text-sm underline">
      <router-link data-testid="nav-member-security" to="/me/security">账号安全</router-link>
      <router-link data-testid="nav-member-credentials" to="/me/credentials">高级连接信息与重置</router-link>
    </div>
  </div>
</template>
