<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { ChevronDown } from 'lucide-vue-next'
import { meApi } from '@/api/endpoints'
import type { MeStatus } from '@/api/types'
import { useResource } from '@/lib/async'
import { formatBytes, formatDateTime, formatQuota, prettyJson } from '@/lib/format'
import { describeAccessReason, describeAccessStep, describeQuotaSource } from '@/lib/accessReasons'
import { memberSummary } from '@/lib/memberService'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

const status = useResource<MeStatus>(() => meApi.status())

onMounted(() => void status.reload())

const data = computed(() => status.data.value)
const member = computed(() => data.value?.member)
const access = computed(() => data.value?.access)
const summary = computed(() => memberSummary(data.value))

const accountStatus = computed(() => {
  if (member.value?.archived) return 'archived'
  if (member.value && member.value.active === false) return 'suspended'
  return member.value?.active === true ? 'active' : 'unknown'
})

// 判定链的 step 是内部标识（account_status / proxy_access / binding_…），
// 列头直接显示这些词对用户没意义，翻成一句话。
const chain = computed(() => (Array.isArray(access.value?.access_chain) ? access.value!.access_chain! : []))
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="账户状态" description="账户状态、代理访问、订阅链接是三个独立状态；无代理权限不代表无法登录查看。">
      <Button variant="outline" :disabled="status.loading.value" @click="status.reload()">刷新</Button>
    </PageHeader>

    <LoadingBlock v-if="status.loading.value && !data" label="正在读取状态" />
    <ErrorState v-else-if="status.error.value" :error="status.error.value" @retry="status.reload()" />
    <div v-else-if="data" class="space-y-6">
      <p v-if="summary.incomplete" role="status" class="text-sm">部分服务资料需核对；保留已知数值，未知字段显示「—」，不表示零或不限量。</p>
      <Card>
        <CardHeader class="border-b">
          <CardTitle class="text-base font-semibold">状态</CardTitle>
        </CardHeader>
        <CardContent class="grid gap-6 pt-6 lg:grid-cols-2">
          <dl class="space-y-3">
            <div class="flex flex-wrap items-center gap-2">
              <dt class="w-24 shrink-0 text-sm text-muted-foreground">账户</dt>
              <dd><StatusBadge :status="accountStatus" /></dd>
            </div>
            <div class="flex flex-wrap items-center gap-2">
              <dt class="w-24 shrink-0 text-sm text-muted-foreground">代理访问</dt>
              <dd><StatusBadge :status="summary.state === 'enabled' ? 'enabled' : summary.state === 'unknown' ? 'unknown' : access?.proxy_access ?? 'disabled'" /></dd>
            </div>
            <div class="flex flex-wrap items-center gap-2">
              <dt class="w-24 shrink-0 text-sm text-muted-foreground">订阅</dt>
              <dd class="mono break-all text-sm">{{ member?.subscription_id ?? '未绑定' }}</dd>
            </div>
            <div class="flex flex-wrap items-center gap-2">
              <dt class="w-24 shrink-0 text-sm text-muted-foreground">到期时间</dt>
              <dd class="text-sm">{{ summary.expires === null ? '未设置到期时间' : formatDateTime(summary.expires, member?.period_timezone) }}</dd>
            </div>
            <div class="flex flex-wrap items-center gap-2">
              <dt class="w-24 shrink-0 text-sm text-muted-foreground">授权节点</dt>
              <dd class="tabular text-sm">{{ access?.allowed_node_ids?.length ?? data.nodes_allowed ?? '—' }} 个</dd>
            </div>
          </dl>

          <dl class="space-y-3">
            <div class="flex flex-wrap items-start gap-2">
              <dt class="w-24 shrink-0 text-sm text-muted-foreground">已用 / 额度</dt>
              <dd class="tabular text-sm">
                {{ formatBytes(summary.used) }} /
                {{ summary.total === undefined ? '—' : formatQuota(summary.total) }}
                <span v-if="access?.quota_source" class="mt-1 block text-xs text-muted-foreground">
                  额度来源：{{ describeQuotaSource(access.quota_source) }}
                </span>
              </dd>
            </div>
            <div class="flex flex-wrap items-start gap-2">
              <dt class="w-24 shrink-0 text-sm text-muted-foreground">周期</dt>
              <dd class="text-sm">
                {{ formatDateTime(member?.period_start, member?.period_timezone) }} →
                {{ formatDateTime(member?.period_end, member?.period_timezone) }}
              </dd>
            </div>
            <div class="flex flex-wrap items-center gap-2">
              <dt class="w-24 shrink-0 text-sm text-muted-foreground">最后登录</dt>
              <dd class="text-sm">{{ formatDateTime(member?.last_login_at) }}</dd>
            </div>
            <div class="flex flex-wrap items-center gap-2">
              <dt class="w-24 shrink-0 text-sm text-muted-foreground">判定算法</dt>
              <dd class="mono break-all text-xs">{{ access?.algorithm_version ?? '未提供' }}</dd>
            </div>
          </dl>
        </CardContent>
        <CardContent class="pt-0">
          <Alert
            v-if="summary.reasons.length"
            class="border-amber-300/70 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950/50 dark:text-amber-200"
          >
            <AlertTitle>{{ summary.title }}</AlertTitle>
            <AlertDescription>
              <ul class="list-disc space-y-1 pl-4">
                <li v-for="reason in summary.reasons" :key="reason.label">
                  {{ reason.label }}。{{ reason.action }}
                </li>
              </ul>
            </AlertDescription>
          </Alert>
          <p
            v-else-if="summary.state === 'enabled'"
            class="rounded-lg border border-sky-200 bg-sky-50 px-4 py-3 text-sm text-sky-800 dark:border-sky-900 dark:bg-sky-950/50 dark:text-sky-200"
          >
            权限正常，没有限制。
          </p>
          <p v-else role="status">{{ summary.title }}</p>
        </CardContent>
      </Card>

      <!-- 判定链与原始返回只对排障有用，普通成员看到的是原因码和 JSON。
           默认折叠，且链路里的原因码已翻成中文。 -->
      <Card v-if="chain.length">
        <CardHeader class="border-b">
          <CardTitle class="text-base font-semibold">权限是怎么判定的</CardTitle>
        </CardHeader>
        <CardContent class="space-y-4 pt-6">
          <p class="text-sm leading-relaxed text-muted-foreground">
            系统按顺序检查几项条件，任何一项不通过就没有代理权限。这张表是给管理员排障用的。
          </p>

          <Collapsible>
            <CollapsibleTrigger as-child>
              <Button variant="outline" size="sm" class="group">
                <ChevronDown class="transition-transform group-data-[state=open]:rotate-180" aria-hidden="true" />
                展开判定详情
              </Button>
            </CollapsibleTrigger>
            <CollapsibleContent class="pt-3">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>检查项</TableHead>
                    <TableHead>结果</TableHead>
                    <TableHead>说明</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <TableRow v-for="(step, index) in chain" :key="index">
                    <TableCell>{{ describeAccessStep(step.step) }}</TableCell>
                    <TableCell>
                      <StatusBadge
                        :status="step.result === 'pass' ? 'enabled' : 'failed'"
                        :label="step.result === 'pass' ? '通过' : '未通过'"
                      />
                    </TableCell>
                    <TableCell class="text-sm">
                      {{ step.reason ? describeAccessReason(step.reason).label : (step.detail ?? '—') }}
                    </TableCell>
                  </TableRow>
                </TableBody>
              </Table>
            </CollapsibleContent>
          </Collapsible>

          <Collapsible>
            <CollapsibleTrigger as-child>
              <Button variant="ghost" size="sm" class="group px-0 text-muted-foreground">
                <ChevronDown class="transition-transform group-data-[state=open]:rotate-180" aria-hidden="true" />
                展开服务端原始返回（排障用）
              </Button>
            </CollapsibleTrigger>
            <CollapsibleContent class="pt-2">
              <pre class="mono max-h-96 overflow-auto rounded-md border border-border bg-muted/40 p-3 text-xs">{{ prettyJson(data) }}</pre>
            </CollapsibleContent>
          </Collapsible>
        </CardContent>
      </Card>
    </div>
  </div>
</template>
