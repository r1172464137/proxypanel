<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { meApi } from '@/api/endpoints'
import type { MeNode, MeStatus, MeSubscription, SubscriptionFormat, TokenRotationResult } from '@/api/types'
import { SUBSCRIPTION_FORMATS, SUBSCRIPTION_FORMAT_LABELS } from '@/api/types'
import { useResource } from '@/lib/async'
import { errorText } from '@/lib/errors'
import { formatBytes, formatDateTime, formatQuota } from '@/lib/format'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import AppModal from '@/components/AppModal.vue'
import CopyField from '@/components/CopyField.vue'
import QrPanel from '@/components/QrPanel.vue'

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

const REASON_TEXT: Record<string, string> = {
  no_binding: '账户还没有绑定订阅',
  binding_expired: '绑定的订阅已到期',
  subscription_access_paused: '订阅的访问开关已关闭',
  subscription_archived: '订阅已归档',
  proxy_paused: '代理访问已被暂停',
  account_suspended: '账户已停用',
  account_archived: '账户已归档',
  over_quota: '已超出配额',
  no_nodes_authorized: '订阅未授权任何可用节点',
  node_disabled: '授权节点全部被停用',
  node_not_authorized: '该节点未授权给此账户',
}

const deniedReasons = computed(() => {
  const access = status.data.value?.access
  const codes = access?.access_reasons ?? []
  const raw = codes.length ? codes : access?.proxy_access ? [access.proxy_access] : []
  return raw.map((code) => REASON_TEXT[code] ?? `未识别的限制原因：${code}`)
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
  <div>
    <PageHeader title="我的订阅" description="订阅链接是个人凭据：可复制、扫码或按客户端格式获取；链接与代理凭据相互独立。">
      <button type="button" class="btn" :disabled="subscription.loading.value" @click="subscription.reload()">刷新</button>
      <button type="button" class="btn btn--danger" @click="openRotate">轮换订阅链接</button>
    </PageHeader>

    <div class="stack">
      <LoadingBlock v-if="subscription.loading.value && !subscription.data.value" label="正在读取订阅" />
      <ErrorState v-else-if="subscription.error.value" :error="subscription.error.value" @retry="subscription.reload()" />

      <template v-else-if="subscription.data.value">
        <section class="card">
          <div class="card__header"><h2>订阅链接</h2></div>

          <!-- 无代理权限：链接仍然存在，但拉取会 403。不展示复制/二维码，
               避免用户拿到一个必然失败的地址。 -->
          <div v-if="accessDenied" class="card__body">
            <p class="banner banner--danger" role="alert">
              <strong>当前无代理权限，订阅链接暂不可用。</strong>
            </p>
            <ul v-if="deniedReasons.length" class="stack" style="margin-top: var(--space-3)">
              <li v-for="reason in deniedReasons" :key="reason" class="text-sm">· {{ reason }}</li>
            </ul>
            <p class="text-sm" style="margin-top: var(--space-3)">
              现在打开该链接会被服务端拒绝（403 no proxy access），客户端也无法拉取配置。
              请联系管理员恢复权限后再使用。
            </p>
            <div class="row" style="margin-top: var(--space-4)">
              <router-link class="btn" to="/me/status">查看账户状态</router-link>
              <router-link class="btn" to="/me/usage">查看用量</router-link>
            </div>
          </div>

          <div v-else class="card__body grid grid--2">
            <div class="stack">
              <div class="field">
                <label for="format">客户端格式</label>
                <select id="format" v-model="format" class="select">
                  <option v-for="option in SUBSCRIPTION_FORMATS" :key="option" :value="option" :disabled="!declaredFormats.includes(option)">
                    {{ SUBSCRIPTION_FORMAT_LABELS[option] }}{{ declaredFormats.includes(option) ? '' : '（服务端未声明）' }}
                  </option>
                </select>
                <p class="field__help">
                  仅可选择服务端声明的格式。若某节点参数无法被所选客户端表示，订阅输出会返回 422 并指明不兼容节点与字段，不会静默删参。
                </p>
              </div>
              <CopyField :value="subscriptionUrl" label="订阅链接" mask warning="此链接等同于密码；泄露后请立即轮换。" />
              <div class="row">
                <a class="btn" :href="subscriptionUrl" target="_blank" rel="noopener noreferrer">打开 / 下载</a>
                <button type="button" class="btn" :disabled="checkState === 'checking'" @click="checkFormat">
                  <span v-if="checkState === 'checking'" class="spinner" aria-hidden="true" />
                  {{ checkState === 'checking' ? '校验中…' : '校验该格式' }}
                </button>
              </div>
              <p v-if="checkState === 'ok'" class="banner banner--info" role="status">{{ checkMessage }}</p>
              <p v-if="checkState === 'failed'" class="banner banner--danger" role="alert">{{ checkMessage }}</p>
              <p v-if="!declaredFormats.length" class="banner banner--warn">
                服务端未声明任何格式，格式选择可能不可用；请联系管理员确认订阅输出能力。
              </p>
            </div>
            <QrPanel :value="subscriptionUrl" />
          </div>
        </section>

        <section class="card">
          <div class="card__header">
            <h2>我的额度</h2>
            <div class="card__header-actions">
              <router-link class="btn btn--sm" to="/me/usage">查看用量</router-link>
            </div>
          </div>
          <LoadingBlock v-if="status.loading.value && !status.data.value" :rows="2" label="正在读取额度" />
          <ErrorState v-else-if="status.error.value" :error="status.error.value" @retry="status.reload()" />
          <div v-else class="card__body">
            <dl class="dl">
              <dt>账户 / 代理</dt>
              <dd class="row" style="gap: var(--space-2)">
                <StatusBadge :status="status.data.value?.member?.active === false ? 'suspended' : 'active'" />
                <StatusBadge :status="status.data.value?.access?.proxy_access ?? 'unknown'" />
              </dd>
              <dt>已用 / 额度</dt>
              <dd class="num">
                {{ formatBytes(quota.used) }} /
                {{ quota.unlimited ? '不限量（仍受节点与规则限制）' : formatQuota(quota.quota) }}
              </dd>
              <dt>当前周期</dt>
              <dd>
                <template v-if="quota.start || quota.end">
                  {{ formatDateTime(quota.start, quota.timezone) }} → {{ formatDateTime(quota.end, quota.timezone) }}
                </template>
                <span v-else class="text-muted">未提供</span>
              </dd>
              <dt>到期时间</dt>
              <dd>{{ formatDateTime(status.data.value?.access?.expires_at ?? status.data.value?.member?.expires_at) }}</dd>
            </dl>
            <p v-if="status.data.value?.access?.access_reasons?.length" class="banner banner--warn" style="margin-top: var(--space-3)">
              当前访问限制原因：{{ status.data.value?.access?.access_reasons?.join('、') }}
            </p>
          </div>
        </section>
      </template>

      <EmptyState
        v-else
        title="当前没有可用订阅"
        description="你的账户尚未绑定订阅（或订阅被暂停）。可以正常登录查看状态；如需代理权限，请联系管理员绑定订阅。"
      >
        <router-link class="btn" to="/me/status">查看账户状态</router-link>
      </EmptyState>

      <section class="card">
        <div class="card__header">
          <h2>授权节点</h2>
          <div class="card__header-actions">
            <button type="button" class="btn btn--sm" :disabled="nodes.loading.value" @click="nodes.reload()">刷新</button>
          </div>
        </div>
        <LoadingBlock v-if="nodes.loading.value && !nodes.data.value" :rows="2" label="正在读取节点" />
        <ErrorState v-else-if="nodes.error.value" :error="nodes.error.value" @retry="nodes.reload()" />
        <div v-else-if="!nodes.data.value?.length" class="state-block">
          <p>没有可用节点。常见原因：尚未绑定订阅、订阅暂停、节点维护，或账户过期/超额。</p>
        </div>
        <div v-else class="table-wrap">
          <table class="table">
            <thead><tr><th>节点</th><th>协议</th><th>地址</th><th>传输 / 安全</th></tr></thead>
            <tbody>
              <tr v-for="(node, index) in nodes.data.value" :key="node.id ?? node.node_id ?? index">
                <td>{{ node.name ?? node.node_id ?? '—' }}</td>
                <td class="mono">{{ node.protocol ?? '—' }}</td>
                <td class="mono">{{ node.host ?? '—' }}<template v-if="node.port">:{{ node.port }}</template></td>
                <td class="text-xs">{{ node.network ?? '—' }}<span class="cell-sub">{{ node.security ?? '—' }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>

    <AppModal :open="tokenOpen" title="轮换订阅链接" size="md" @close="tokenOpen = false">
      <div class="stack">
        <p class="text-sm">轮换后旧订阅链接立即失效；代理凭据不变，现有连接不中断。</p>
        <p v-if="rotateError" class="banner banner--danger" role="alert">{{ rotateError }}</p>
        <template v-if="rotated">
          <CopyField :value="rotated.url" label="新订阅链接" mask warning="此链接等同于密码，请更新到你的客户端。" />
          <CopyField :value="rotated.token" label="新令牌" mask />
          <QrPanel v-if="!accessDenied" :value="rotated.url" />
          <p v-else class="banner banner--warn">
            该账户当前无代理权限，新链接同样会返回 403；请先联系管理员恢复权限。
          </p>
        </template>
      </div>
      <template #footer>
        <button type="button" class="btn" :disabled="rotating" @click="tokenOpen = false">关闭</button>
        <button type="button" class="btn btn--danger" :disabled="rotating" @click="rotate">
          <span v-if="rotating" class="spinner" aria-hidden="true" />
          {{ rotating ? '轮换中…' : '确认轮换' }}
        </button>
      </template>
    </AppModal>
  </div>
</template>