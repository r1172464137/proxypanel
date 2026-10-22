<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { meApi } from '@/api/endpoints'
import type { MeStatus } from '@/api/types'
import { useResource } from '@/lib/async'
import { formatBytes, formatDateTime, formatQuota, prettyJson } from '@/lib/format'
import { accessReasonDetails } from '@/lib/accessReasons'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import StatusBadge from '@/components/StatusBadge.vue'

const status = useResource<MeStatus>(() => meApi.status())

onMounted(() => void status.reload())

const data = computed(() => status.data.value)
const member = computed(() => data.value?.member)
const access = computed(() => data.value?.access)

const accountStatus = computed(() => {
  if (member.value?.archived) return 'archived'
  if (member.value && member.value.active === false) return 'suspended'
  return 'active'
})

const chain = computed(() => (Array.isArray(access.value?.access_chain) ? access.value!.access_chain! : []))
</script>

<template>
  <div>
    <PageHeader title="账户状态" description="账户状态、代理访问、订阅链接是三个独立状态；无代理权限不代表无法登录查看。">
      <button type="button" class="btn" :disabled="status.loading.value" @click="status.reload()">刷新</button>
    </PageHeader>

    <LoadingBlock v-if="status.loading.value && !data" label="正在读取状态" />
    <ErrorState v-else-if="status.error.value" :error="status.error.value" @retry="status.reload()" />
    <div v-else-if="data" class="stack">
      <section class="card">
        <div class="card__header"><h2>状态</h2></div>
        <div class="card__body grid grid--2">
          <dl class="dl">
            <dt>账户</dt>
            <dd><StatusBadge :status="accountStatus" /></dd>
            <dt>代理访问</dt>
            <dd><StatusBadge :status="access?.proxy_access ?? 'unknown'" /></dd>
            <dt>订阅</dt>
            <dd>{{ member?.subscription_id ?? '未绑定' }}</dd>
            <dt>到期时间</dt>
            <dd>{{ formatDateTime(access?.expires_at ?? member?.expires_at) }}</dd>
            <dt>授权节点</dt>
            <dd class="num">{{ access?.allowed_node_ids?.length ?? data.nodes_allowed ?? '—' }} 个</dd>
          </dl>
          <dl class="dl">
            <dt>已用 / 额度</dt>
            <dd class="num">
              {{ formatBytes(access?.used_bytes ?? member?.used_bytes) }} /
              {{ formatQuota(access?.effective_quota_bytes ?? member?.effective_quota_bytes) }}
              <span v-if="access?.quota_source" class="cell-sub">额度来源：{{ access.quota_source }}</span>
            </dd>
            <dt>周期</dt>
            <dd>
              {{ formatDateTime(member?.period_start, member?.period_timezone) }} →
              {{ formatDateTime(member?.period_end, member?.period_timezone) }}
            </dd>
            <dt>最后登录</dt>
            <dd>{{ formatDateTime(member?.last_login_at) }}</dd>
            <dt>判定算法</dt>
            <dd class="mono text-xs">{{ access?.algorithm_version ?? '未提供' }}</dd>
          </dl>
        </div>
        <div class="card__body" style="padding-top: 0">
          <div v-if="accessReasonDetails(access?.access_reasons).length" class="banner banner--warn">
            <div class="banner__title">现在还不能使用代理</div>
            <ul>
              <li v-for="reason in accessReasonDetails(access?.access_reasons)" :key="reason.label">
                {{ reason.label }}。{{ reason.action }}
              </li>
            </ul>
          </div>
          <p v-else-if="access" class="banner banner--info">权限正常，没有限制。</p>
        </div>
      </section>

      <section v-if="chain.length" class="card">
        <div class="card__header"><h2>授权判定链</h2></div>
        <div class="table-wrap">
          <table class="table">
            <thead><tr><th>步骤</th><th>结果</th><th>说明</th></tr></thead>
            <tbody>
              <tr v-for="(step, index) in chain" :key="index">
                <td class="mono">{{ step.step ?? '—' }}</td>
                <td><StatusBadge :status="step.result === 'pass' ? 'enabled' : 'failed'" :label="step.result ?? 'unknown'" /></td>
                <td class="text-sm">{{ step.reason ?? step.detail ?? '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="card">
        <div class="card__header"><h2>原始字段（排障用）</h2></div>
        <div class="card__body">
          <details>
            <summary class="text-sm text-muted">展开服务端返回</summary>
            <pre class="code-block">{{ prettyJson(data) }}</pre>
          </details>
        </div>
      </section>
    </div>
  </div>
</template>