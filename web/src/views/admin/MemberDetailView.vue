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
import { accessReasonDetails } from '@/lib/accessReasons'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import AppModal from '@/components/AppModal.vue'
import CopyField from '@/components/CopyField.vue'
import QrPanel from '@/components/QrPanel.vue'

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
  <div>
    <PageHeader
      :title="data ? `成员 · ${data.username}` : '成员详情'"
      description="生效值与来源、访问原因、用量与周期；密码/哈希永远不在响应中。"
    >
      <router-link class="btn btn--sm" to="/admin/members">返回成员列表</router-link>
      <button type="button" class="btn btn--sm" :disabled="member.loading.value" @click="member.reload()">刷新</button>
      <template v-if="data">
        <button type="button" class="btn btn--sm" :disabled="pending" @click="toggleActive">{{ data.active ? '停用账户' : '启用账户' }}</button>
        <button type="button" class="btn btn--sm" :disabled="pending" @click="toggleProxy">
          {{ data.proxy_enabled ? '暂停代理访问' : '恢复代理访问' }}
        </button>
        <button type="button" class="btn btn--sm" @click="openAdjust">额度调整</button>
        <button type="button" class="btn btn--sm" @click="credOpen = true">轮换凭据</button>
        <button type="button" class="btn btn--sm" @click="rotateToken">轮换订阅链接</button>
      </template>
    </PageHeader>

    <LoadingBlock v-if="member.loading.value && !data" label="正在读取成员" />
    <ErrorState v-else-if="member.error.value" :error="member.error.value" @retry="member.reload()" />
    <div v-else-if="data" class="stack">
      <p v-if="actionError" class="banner banner--danger" role="alert">{{ actionError }}</p>

      <section class="card">
        <div class="card__header"><h2>状态</h2></div>
        <div class="card__body grid grid--2">
          <dl class="dl">
            <dt>用户名</dt>
            <dd>{{ data.username }} <span class="text-xs text-muted mono">{{ data.id }}</span></dd>
            <dt>角色</dt>
            <dd>{{ data.role === 'admin' ? '管理员' : '成员' }}</dd>
            <dt>账户状态</dt>
            <dd><StatusBadge :status="data.active ? 'active' : 'suspended'" /></dd>
            <dt>代理访问</dt>
            <dd>
              <StatusBadge :status="data.proxy_access ?? (data.proxy_enabled ? 'enabled' : 'paused_by_admin')" />
              <span v-if="data.over_quota" class="badge badge--danger">已超额</span>
            </dd>
            <dt>访问原因</dt>
            <dd>
              <template v-if="accessReasonDetails(data.access_reasons).length">
                <div v-for="reason in accessReasonDetails(data.access_reasons)" :key="reason.label" class="stack" style="gap: 2px">
                  <strong>{{ reason.label }}</strong>
                  <span class="text-sm text-muted">{{ reason.action }}</span>
                </div>
              </template>
              <span v-else class="text-muted">没有限制，权限正常</span>
            </dd>
            <dt>最后更新</dt>
            <dd>{{ formatDateTime(data.updated_at) }}</dd>
          </dl>
          <dl class="dl">
            <dt>订阅</dt>
            <dd class="mono">{{ data.subscription_id ?? '未绑定' }}</dd>
            <dt>有效期</dt>
            <dd>
              <span :title="utcLabel(data.expires_at)">{{ formatDateTime(data.expires_at) }}</span>
            </dd>
            <dt>已用 / 额度</dt>
            <dd class="num">
              {{ formatBytes(data.used_bytes) }} / {{ formatQuota(data.effective_quota_bytes) }}
              <span class="cell-sub">来源：{{ quotaSource(data) }}</span>
            </dd>
            <dt>配额覆盖</dt>
            <dd>
              <template v-if="data.quota_override_enabled">
                已启用（{{ formatQuota(data.quota_override_bytes) }}）· 原订阅默认 {{ formatQuota(data.effective_quota_bytes) }}
              </template>
              <template v-else>未启用（继承订阅默认）</template>
            </dd>
            <dt>当前周期</dt>
            <dd>
              <template v-if="data.period_start || data.period_end">
                {{ formatDateTime(data.period_start, data.period_timezone) }} → {{ formatDateTime(data.period_end, data.period_timezone) }}
                <span class="cell-sub">UTC 存储；业务时区由订阅决定，接口未返回时不做推断</span>
              </template>
              <span v-else class="text-muted">未提供</span>
            </dd>
          </dl>
        </div>
      </section>

      <section class="card">
        <div class="card__header"><h2>原始响应（排障用）</h2></div>
        <div class="card__body">
          <details>
            <summary class="text-sm text-muted">展开服务端返回的字段</summary>
            <pre class="code-block">{{ prettyJson(data) }}</pre>
          </details>
        </div>
      </section>
    </div>

    <AppModal :open="adjustOpen" title="额度调整（审计化）" size="sm" @close="adjustOpen = false">
      <div class="stack">
        <p class="text-sm text-muted">调整当期额度，不修改原始用量，也不提供“立即改周期并清零”。正数增加额度，负数扣减。</p>
        <div class="field">
          <label for="adjust-gib">调整量（GiB）</label>
          <input id="adjust-gib" v-model="adjustGib" class="input" inputmode="decimal" placeholder="如 50 或 -20" />
        </div>
        <div class="field">
          <label for="adjust-reason">原因（必填）</label>
          <textarea id="adjust-reason" v-model="adjustReason" class="textarea" style="min-height: 70px" />
        </div>
        <p v-if="adjustError" class="banner banner--danger" role="alert">{{ adjustError }}</p>
      </div>
      <template #footer>
        <button type="button" class="btn" :disabled="adjusting" @click="adjustOpen = false">取消</button>
        <button type="button" class="btn btn--primary" :disabled="adjusting" @click="submitAdjust">
          <span v-if="adjusting" class="spinner" aria-hidden="true" />提交
        </button>
      </template>
    </AppModal>

    <AppModal :open="credOpen" title="代理凭据轮换" size="sm" @close="credOpen = false">
      <div class="stack">
        <p class="text-sm">旧凭据立即失效（硬撤权），可能中断连接。</p>
        <label class="checkbox"><input v-model="credScope" type="radio" value="all_nodes" /> 全部节点</label>
        <label class="checkbox"><input v-model="credScope" type="radio" value="single_node" /> 单个节点</label>
        <select v-if="credScope === 'single_node'" v-model="credNodeId" class="select">
          <option value="">选择节点</option>
          <option v-for="node in nodes" :key="node.id" :value="node.id">{{ node.name }}</option>
        </select>
        <p v-if="credError" class="banner banner--danger" role="alert">{{ credError }}</p>
      </div>
      <template #footer>
        <button type="button" class="btn" :disabled="credPending" @click="credOpen = false">取消</button>
        <button type="button" class="btn btn--primary" :disabled="credPending" @click="submitCredential">确认轮换</button>
      </template>
    </AppModal>

    <AppModal :open="tokenOpen" title="订阅令牌轮换" size="md" @close="tokenOpen = false">
      <div class="stack">
        <p v-if="tokenLoading" class="text-sm"><span class="spinner" aria-hidden="true" /> 轮换中…</p>
        <p v-if="tokenError" class="banner banner--danger" role="alert">{{ tokenError }}</p>
        <template v-if="tokenResult">
          <CopyField :value="tokenResult.url" label="订阅链接" mask warning="此链接等同于密码。" />
          <CopyField :value="tokenResult.token" label="令牌" mask />
          <QrPanel :value="tokenResult.url" />
        </template>
      </div>
      <template #footer><button type="button" class="btn" @click="tokenOpen = false">关闭</button></template>
    </AppModal>
  </div>
</template>