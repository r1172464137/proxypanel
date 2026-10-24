<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { meApi } from '@/api/endpoints'
import { useResource } from '@/lib/async'
import { formatBytes, formatDateTime, formatQuota } from '@/lib/format'
import { memberSummary } from '@/lib/memberService'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import ClientImportGuide from '@/components/ClientImportGuide.vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
const route = useRoute()
const status = useResource(() => meApi.status())
const subscription = useResource(() => meApi.subscription())
const nodes = useResource(() => meApi.nodes())
const summary = computed(() => memberSummary(status.data.value))
const timezone = computed(() => summary.value.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone)
async function refresh() { await Promise.all([status.reload(), subscription.reload(), nodes.reload()]) }
onMounted(refresh)
</script>

<template>
  <div class="min-w-0 space-y-6">
    <PageHeader title="我的服务" description="查看状态、额度与节点，再按客户端指引导入。">
      <Button data-testid="service-refresh-button" class="min-h-11" variant="outline" :disabled="status.loading.value || subscription.loading.value" @click="refresh">刷新服务资料</Button>
    </PageHeader>
    <p v-if="route.query.denied" role="alert" class="text-sm">该页面仅管理员可用。你仍可查看本人的服务和账号。</p>
    <p v-if="route.query.welcome" role="status" data-testid="redeem-result-message">账号已创建。{{ status.data.value?.member?.subscription_id === null ? '尚未分配服务方案，请联系邀请你的管理员开通。' : status.data.value?.member?.subscription_id ? '已分配服务方案，状态与周期以下方资料为准。' : '正在核对服务分配结果，请以下方状态为准。' }}</p>
    <section aria-label="服务状态与额度" data-testid="service-summary-card">
      <LoadingBlock v-if="status.loading.value && !status.data.value" label="正在读取服务状态" />
      <ErrorState v-else-if="status.error.value" :error="status.error.value" @retry="status.reload()" />
      <Card v-else>
        <CardHeader><CardTitle>{{ summary.title }}</CardTitle></CardHeader>
        <CardContent class="space-y-4">
          <p v-if="summary.incomplete" role="status" class="text-sm text-amber-700 dark:text-amber-300">部分服务资料需核对，以下保留已知数据。未知字段显示为「—」，不代表零或不限量。</p>
          <p v-if="summary.state === 'enabled'" class="text-sm text-muted-foreground">当前授权允许使用；真实连接需在客户端确认。</p>
          <ul v-if="summary.reasons.length" data-testid="me-unavailable-reason" class="space-y-2 text-sm"><li v-for="reason in summary.reasons" :key="reason.label"><strong>{{ reason.label }}</strong>。{{ reason.action }}</li></ul>
          <dl data-testid="me-quota-meter" class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <div><dt class="text-sm text-muted-foreground">已用</dt><dd class="text-xl font-semibold">{{ formatBytes(summary.used) }}</dd></div>
            <div><dt class="text-sm text-muted-foreground">总额度</dt><dd class="text-xl font-semibold">{{ summary.total === undefined ? '—' : formatQuota(summary.total) }}</dd></div>
            <div><dt class="text-sm text-muted-foreground">剩余</dt><dd class="text-xl font-semibold">{{ summary.unlimited ? '不限量' : formatBytes(summary.remaining) }}</dd></div>
            <div><dt class="text-sm text-muted-foreground">有效期至</dt><dd>{{ summary.expires === null ? '未设置到期时间' : formatDateTime(summary.expires, timezone) }}</dd></div>
            <div><dt class="text-sm text-muted-foreground">下次重置</dt><dd>{{ formatDateTime(summary.end, timezone) }}</dd></div>
            <div><dt class="text-sm text-muted-foreground">当前周期开始</dt><dd>{{ formatDateTime(summary.start, timezone) }}</dd></div>
          </dl>
          <p class="text-xs text-muted-foreground">数据更新时间：暂未提供。时间按 {{ timezone }} 显示；未知数据不是零，不限量仍受有效期与授权范围限制。</p>
          <div class="flex flex-wrap gap-3 text-sm underline"><router-link data-testid="nav-service-status" class="inline-flex min-h-11 items-center" to="/me/status">查看服务状态与原因</router-link><router-link data-testid="nav-service-usage" class="inline-flex min-h-11 items-center" to="/me/usage">查看用量明细</router-link></div>
        </CardContent>
      </Card>
    </section>
    <Card data-testid="service-nodes-card">
      <CardHeader><CardTitle>我的节点</CardTitle></CardHeader>
      <CardContent>
        <LoadingBlock v-if="nodes.loading.value" label="正在读取节点" />
        <ErrorState v-else-if="nodes.error.value" :error="nodes.error.value" @retry="nodes.reload()" />
        <ul v-else-if="nodes.data.value?.items.length" class="space-y-2 text-sm"><li v-for="(node, index) in nodes.data.value.items" :key="node.id ?? index" class="break-words">{{ node.name ?? '未命名节点' }} · {{ node.protocol ?? '协议未提供' }}</li></ul>
        <p v-else class="text-sm text-muted-foreground">暂无可用节点。请查看服务状态，或联系为你开通服务的管理员。</p>
        <p class="mt-2 text-sm text-muted-foreground">导入后，在客户端选择这里的同名节点。{{ nodes.data.value?.next_cursor ? '这里只显示部分节点，完整列表以导入配置为准。' : '' }}</p>
      </CardContent>
    </Card>
    <Card data-testid="client-import-card">
      <CardHeader><CardTitle>导入客户端</CardTitle></CardHeader>
      <CardContent class="space-y-4">
        <LoadingBlock v-if="subscription.loading.value" label="正在读取导入资料" />
        <div v-else-if="subscription.error.value?.code === 'RECOVERY_MODE'" data-testid="import-maintenance-message" role="status" class="space-y-3 text-sm">
          <p>恢复核对进行中，暂时无法获取订阅链接。上方已读取的状态与额度仍可查看，请稍后重试。</p>
          <Button class="min-h-11" variant="outline" @click="subscription.reload()">重试获取导入资料</Button>
        </div>
        <ErrorState v-else-if="subscription.error.value" :error="subscription.error.value" @retry="subscription.reload()" />
        <p v-else-if="summary.state !== 'enabled' || !!status.error.value" role="status">{{ summary.state === 'denied' ? '当前服务不可用，暂不能导入。' : '服务状态尚未确认，请先刷新服务资料。' }}请联系为你开通服务的管理员，账号与用量页面仍可访问。</p>
        <ClientImportGuide v-else :subscription="subscription.data.value" :allowed="summary.state === 'enabled'" />
      </CardContent>
    </Card>
    <div class="flex flex-wrap gap-4 text-sm underline">
      <router-link class="inline-flex min-h-11 items-center" data-testid="nav-member-security" to="/me/security">账号安全</router-link>
      <router-link class="inline-flex min-h-11 items-center" data-testid="nav-member-connect" to="/me/connect">订阅链接与二维码</router-link>
      <router-link class="inline-flex min-h-11 items-center" data-testid="nav-member-credentials" to="/me/credentials">重置连接凭据</router-link>
    </div>
  </div>
</template>
