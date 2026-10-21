<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { adminApi } from '@/api/endpoints'
import type { BatchOperation, MemberRecord, NodeRecord, SubscriptionRecord, TokenRotationResult } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText } from '@/lib/errors'
import { formatBytes, formatDateTime, formatQuota, fromLocalInput, utcLabel } from '@/lib/format'
import { buildMemberWrite, draftFromMember, emptyMemberDraft, quotaSource, type MemberDraft } from '@/lib/memberDraft'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import CopyField from '@/components/CopyField.vue'
import QrPanel from '@/components/QrPanel.vue'
import ImpactPreviewDialog from '@/components/ImpactPreviewDialog.vue'

const list = usePagedList<MemberRecord>((cursor) => adminApi.members.list({ limit: 50, cursor }))
const subscriptions = ref<SubscriptionRecord[]>([])
const nodes = ref<NodeRecord[]>([])
const referenceError = ref<unknown>(null)
const filter = ref('')
const selected = reactive(new Set<string>())

onMounted(async () => {
  void list.refresh()
  try {
    const [subPage, nodePage] = await Promise.all([
      adminApi.subscriptions.list({ limit: 200 }),
      adminApi.nodes.list({ limit: 200 }),
    ])
    subscriptions.value = subPage.items
    nodes.value = nodePage.items
  } catch (caught) {
    referenceError.value = caught
  }
})

const subscriptionNames = computed(() => {
  const map = new Map<string, string>()
  for (const sub of subscriptions.value) map.set(sub.id, sub.name)
  return map
})

const visibleMembers = computed(() => {
  const keyword = filter.value.trim().toLowerCase()
  if (!keyword) return list.items.value
  return list.items.value.filter(
    (member) => member.username.toLowerCase().includes(keyword) || member.id.toLowerCase().includes(keyword),
  )
})

const selectedMembers = computed(() => list.items.value.filter((member) => selected.has(member.id)))
const selectedOnOtherPages = computed(() => Math.max(0, selected.size - selectedMembers.value.length))

function toggleSelect(id: string): void {
  if (selected.has(id)) selected.delete(id)
  else selected.add(id)
}

function toggleSelectAll(): void {
  const visible = visibleMembers.value
  const allSelected = visible.length > 0 && visible.every((member) => selected.has(member.id))
  for (const member of visible) {
    if (allSelected) selected.delete(member.id)
    else selected.add(member.id)
  }
}

const allVisibleSelected = computed(
  () => visibleMembers.value.length > 0 && visibleMembers.value.every((member) => selected.has(member.id)),
)

/* ---------- 创建 / 编辑 ---------- */
const formOpen = ref(false)
const form = reactive<MemberDraft>(emptyMemberDraft())
const editing = ref<MemberRecord | null>(null)
const saving = ref(false)
const formError = ref('')
const formErrors = ref<string[]>([])
const formWarnings = ref<string[]>([])

const formTitle = computed(() => (editing.value ? `编辑成员 · ${editing.value.username}` : '新建成员'))

function openCreate(): void {
  editing.value = null
  Object.assign(form, emptyMemberDraft())
  formError.value = ''
  formErrors.value = []
  formWarnings.value = []
  formOpen.value = true
}

function openEdit(member: MemberRecord): void {
  editing.value = member
  Object.assign(form, draftFromMember(member))
  formError.value = ''
  formErrors.value = []
  formWarnings.value = []
  formOpen.value = true
}

async function saveMember(): Promise<void> {
  formError.value = ''
  const mode = editing.value ? 'update' : 'create'
  const built = buildMemberWrite(form, mode)
  formErrors.value = built.errors
  formWarnings.value = built.warnings
  if (!built.payload) return
  saving.value = true
  try {
    if (editing.value) {
      await adminApi.members.update(editing.value.id, { ...built.payload, version: editing.value.version })
      pushToast('success', '成员已更新', '账户规则立即生效；代理权限变更由节点异步应用。')
    } else {
      await adminApi.members.create(built.payload)
      pushToast('success', '成员已创建', '如未绑定订阅，成员可登录但暂无代理权限。')
    }
    formOpen.value = false
    await list.refresh()
  } catch (caught) {
    formError.value = errorText(caught)
  } finally {
    saving.value = false
  }
}

/* ---------- 状态切换 ---------- */
async function toggleActive(member: MemberRecord): Promise<void> {
  try {
    await adminApi.members.update(member.id, { active: !member.active, version: member.version })
    pushToast('success', member.active ? '成员已停用' : '成员已启用', '停用会撤销会话并硬撤权；节点生效为异步。')
    await list.refresh()
  } catch (caught) {
    pushToast('error', '操作失败', errorText(caught))
  }
}

/* ---------- 订阅令牌轮换 ---------- */
const tokenOpen = ref(false)
const tokenResult = ref<TokenRotationResult | null>(null)
const tokenError = ref('')
const tokenLoading = ref(false)
const tokenTarget = ref<MemberRecord | null>(null)

async function rotateToken(member: MemberRecord): Promise<void> {
  tokenTarget.value = member
  tokenOpen.value = true
  tokenResult.value = null
  tokenError.value = ''
  tokenLoading.value = true
  try {
    tokenResult.value = await adminApi.members.rotateSubscriptionToken(member.id)
    pushToast('success', '订阅令牌已轮换', '旧链接立即失效；代理凭据不受影响，现有连接不中断。')
    await list.refresh()
  } catch (caught) {
    tokenError.value = errorText(caught)
  } finally {
    tokenLoading.value = false
  }
}

/* ---------- 凭据轮换 ---------- */
const credOpen = ref(false)
const credTarget = ref<MemberRecord | null>(null)
const credScope = ref<'all_nodes' | 'single_node'>('all_nodes')
const credNodeId = ref('')
const credPending = ref(false)
const credError = ref('')
const credDone = ref(false)

function openCredential(member: MemberRecord): void {
  credTarget.value = member
  credScope.value = 'all_nodes'
  credNodeId.value = ''
  credError.value = ''
  credDone.value = false
  credOpen.value = true
}

async function submitCredential(): Promise<void> {
  const target = credTarget.value
  if (!target) return
  if (credScope.value === 'single_node' && !credNodeId.value) {
    credError.value = '单节点轮换必须选择一个节点'
    return
  }
  credPending.value = true
  credError.value = ''
  try {
    await adminApi.members.rotateCredentials(target.id, {
      scope: credScope.value,
      ...(credScope.value === 'single_node' ? { node_id: credNodeId.value } : {}),
    })
    credDone.value = true
    pushToast('success', '凭据轮换已提交', '旧凭据按硬撤权策略失效；节点分发为异步，请查看任务回执。')
    await list.refresh()
  } catch (caught) {
    credError.value = errorText(caught)
  } finally {
    credPending.value = false
  }
}

/* ---------- 删除（归档） ---------- */
const deleteTarget = ref<MemberRecord | null>(null)
const deleting = ref(false)
const deleteError = ref('')

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target) return
  deleting.value = true
  deleteError.value = ''
  try {
    await adminApi.members.remove(target.id, target.version)
    deleteTarget.value = null
    await list.refresh()
  } catch (caught) {
    deleteError.value = errorText(caught)
  } finally {
    deleting.value = false
  }
}

/* ---------- 批量 ---------- */
const batchOpen = ref(false)
const batchOperation = ref<BatchOperation>('assign_subscription')
const batchValues = reactive<{ subscription_id: string; active: string; expires_at_local: string }>({
  subscription_id: '',
  active: 'false',
  expires_at_local: '',
})
const batchError = ref('')
const impact = reactive<{ open: boolean; operation: BatchOperation; values: Record<string, unknown> }>({
  open: false,
  operation: 'assign_subscription',
  values: {},
})

function openBatch(): void {
  batchError.value = ''
  batchValues.subscription_id = ''
  batchValues.active = 'false'
  batchValues.expires_at_local = ''
  batchOpen.value = true
}

function startBatchImpact(): void {
  batchError.value = ''
  const values: Record<string, unknown> = {}
  if (batchOperation.value === 'assign_subscription') {
    if (!batchValues.subscription_id) {
      batchError.value = '请选择目标订阅'
      return
    }
    values.subscription_id = batchValues.subscription_id
  }
  if (batchOperation.value === 'set_active') {
    values.active = batchValues.active === 'true'
  }
  if (batchOperation.value === 'set_expiry') {
    const iso = fromLocalInput(batchValues.expires_at_local)
    if (!iso) {
      batchError.value = '请选择新的到期时间'
      return
    }
    values.expires_at = iso
  }
  impact.operation = batchOperation.value
  impact.values = values
  impact.open = true
  batchOpen.value = false
}
</script>

<template>
  <div>
    <PageHeader title="成员" description="账户状态、代理访问、订阅绑定与额度覆盖互相独立；批量操作必须先预览影响再提交。">
      <button type="button" class="btn" :disabled="list.loading.value" @click="list.refresh()">刷新</button>
      <button type="button" class="btn btn--primary" @click="openCreate">新建成员</button>
    </PageHeader>

    <ErrorState v-if="referenceError" :error="referenceError" @retry="list.refresh()" />
    <div class="toolbar">
      <input v-model="filter" class="input" style="max-width: 280px" placeholder="按用户名或 ID 过滤当前页" aria-label="过滤成员" />
      <span class="text-xs text-muted">过滤仅作用于当前页数据；跨页请用批量操作的显式成员集合。</span>
      <div class="toolbar__spacer" />
      <button type="button" class="btn" :disabled="!selected.size" @click="openBatch">
        批量操作（已选 {{ selected.size }}）
      </button>
      <button v-if="selected.size" type="button" class="btn btn--ghost" @click="selected.clear()">清空选择</button>
    </div>

    <ErrorState v-if="list.error.value" :error="list.error.value" @retry="list.refresh()" />
    <LoadingBlock v-else-if="list.loading.value && !list.items.value.length" label="正在读取成员" />
    <div v-else class="card card__body--flush">
      <div v-if="!list.items.value.length" class="card__body">
        <EmptyState title="还没有成员" description="创建成员或生成邀请码；成员可登录查看状态，但代理权限取决于订阅与授权判定。">
          <button type="button" class="btn btn--primary" @click="openCreate">新建成员</button>
          <router-link class="btn" to="/admin/invitations">生成邀请</router-link>
        </EmptyState>
      </div>
      <div v-else-if="!visibleMembers.length" class="card__body">
        <EmptyState title="当前页没有匹配的成员" description="过滤只作用于当前页数据；清空过滤词即可恢复。跨页请使用批量操作的显式成员集合。">
          <button type="button" class="btn" @click="filter = ''">清空过滤</button>
        </EmptyState>
      </div>
      <template v-else>
        <div class="table-wrap">
          <table class="table">
            <thead>
              <tr>
                <th style="width: 34px">
                  <input
                    type="checkbox"
                    :checked="allVisibleSelected"
                    aria-label="选择当前页全部成员"
                    @change="toggleSelectAll"
                  />
                </th>
                <th>成员</th>
                <th>账户 / 代理</th>
                <th>订阅</th>
                <th class="hide-sm">有效期</th>
                <th>用量 / 额度</th>
                <th class="hide-sm">周期</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="member in visibleMembers" :key="member.id">
                <td>
                  <input
                    type="checkbox"
                    :checked="selected.has(member.id)"
                    :aria-label="`选择 ${member.username}`"
                    @change="toggleSelect(member.id)"
                  />
                </td>
                <td>
                  <router-link :to="`/admin/members/${member.id}`">{{ member.username }}</router-link>
                  <span class="cell-sub">
                    {{ member.role === 'admin' ? '管理员' : '成员' }}
                    <span v-if="member.quota_override_enabled" class="badge badge--accent" style="margin-left: 4px">配额覆盖</span>
                  </span>
                </td>
                <td>
                  <div class="row" style="gap: var(--space-1)">
                    <StatusBadge :status="member.active ? 'active' : 'suspended'" :label="member.active ? '正常' : '已停用'" />
                    <StatusBadge :status="member.proxy_access ?? (member.proxy_enabled ? 'enabled' : 'paused_by_admin')" />
                  </div>
                  <span v-if="member.over_quota" class="badge badge--danger" style="margin-left: 4px">已超额</span>
                  <span v-if="member.access_reasons?.length" class="cell-sub">原因：{{ member.access_reasons.join('、') }}</span>
                </td>
                <td>
                  {{ member.subscription_id ? subscriptionNames.get(member.subscription_id) ?? member.subscription_id : '未绑定' }}
                </td>
                <td class="hide-sm">
                  <span :title="utcLabel(member.expires_at)">{{ formatDateTime(member.expires_at) }}</span>
                </td>
                <td class="num">
                  {{ formatBytes(member.used_bytes) }} / {{ formatQuota(member.effective_quota_bytes) }}
                  <span class="cell-sub">来源：{{ quotaSource(member) === 'override' ? '个人覆盖' : quotaSource(member) === 'unlimited' ? '不限量' : quotaSource(member) === 'unknown' ? '列表未提供' : '订阅默认' }}</span>
                </td>
                <td class="hide-sm text-xs">
                  <div v-if="member.period_start || member.period_end">
                    {{ formatDateTime(member.period_start, member.period_timezone) }} →<br />{{ formatDateTime(member.period_end, member.period_timezone) }}
                  </div>
                  <span v-else class="text-muted">未提供</span>
                </td>
                <td class="actions">
                  <button type="button" class="btn btn--sm" @click="openEdit(member)">编辑</button>
                  <button type="button" class="btn btn--sm" @click="toggleActive(member)">
                    {{ member.active ? '停用' : '启用' }}
                  </button>
                  <button type="button" class="btn btn--sm" @click="openCredential(member)">凭据</button>
                  <button type="button" class="btn btn--sm" @click="rotateToken(member)">订阅链接</button>
                  <button type="button" class="btn btn--sm btn--danger" @click="deleteTarget = member">删除</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="row row--between" style="padding: var(--space-3) var(--space-4)">
          <span class="text-xs text-muted">
            第 {{ list.page.value }} 页 · 本页 {{ visibleMembers.length }} 名
            <template v-if="selectedOnOtherPages"> · 已选择 {{ selected.size }} 名（含其它页 {{ selectedOnOtherPages }} 名）</template>
          </span>
          <div class="row">
            <button type="button" class="btn btn--sm" :disabled="list.page.value <= 1 || list.loading.value" @click="list.prev()">上一页</button>
            <button type="button" class="btn btn--sm" :disabled="!list.nextCursor.value || list.loading.value" @click="list.next()">下一页</button>
          </div>
        </div>
      </template>
    </div>

    <div v-if="selected.size" class="selection-bar">
      <span>已选择 {{ selected.size }} 名成员</span>
      <span class="text-xs">{{ selectedMembers.map((m) => m.username).join('、') }}{{ selectedOnOtherPages ? ` 等 ${selected.size} 名` : '' }}</span>
      <div class="toolbar__spacer" />
      <button type="button" class="btn btn--sm" @click="openBatch">批量操作</button>
      <button type="button" class="btn btn--sm" @click="selected.clear()">清空</button>
    </div>

    <!-- 创建 / 编辑 -->
    <AppModal :open="formOpen" :title="formTitle" size="md" @close="formOpen = false">
      <div class="stack">
        <p v-if="formError" class="banner banner--danger" role="alert">{{ formError }}</p>
        <p v-for="error in formErrors" :key="error" class="banner banner--warn">{{ error }}</p>
        <div class="grid grid--form">
          <div class="field">
            <label for="member-username">用户名</label>
            <input id="member-username" v-model="form.username" class="input" autocomplete="off" />
          </div>
          <div class="field">
            <label for="member-password">{{ editing ? '重置密码（留空不修改）' : '初始密码' }}</label>
            <input id="member-password" v-model="form.password" class="input" type="password" autocomplete="new-password" />
            <p class="field__help">至少 10 位；成员输出永不包含密码或哈希</p>
          </div>
          <div class="field">
            <label for="member-role">角色</label>
            <select id="member-role" v-model="form.role" class="select">
              <option value="member">成员</option>
              <option value="admin">管理员</option>
            </select>
          </div>
          <div class="field">
            <label for="member-sub">订阅</label>
            <select id="member-sub" v-model="form.subscription_id" class="select">
              <option value="">不绑定</option>
              <option v-for="sub in subscriptions" :key="sub.id" :value="sub.id">{{ sub.name }}</option>
            </select>
          </div>
          <div class="field">
            <label for="member-expires">有效期（到期时间）</label>
            <input id="member-expires" v-model="form.expires_at_local" class="input" type="datetime-local" />
            <p class="field__help">留空表示跟随订阅默认有效期</p>
          </div>
          <div class="field">
            <label for="member-override">配额覆盖（GiB）</label>
            <input id="member-override" v-model="form.quota_override_gib" class="input" inputmode="decimal" :disabled="!form.quota_override_enabled" />
            <label class="checkbox">
              <input v-model="form.quota_override_enabled" type="checkbox" />
              启用个人覆盖（关闭即恢复继承订阅默认配额）
            </label>
          </div>
        </div>
        <label class="checkbox"><input v-model="form.active" type="checkbox" /> 账户启用（停用会撤销会话）</label>
        <label class="checkbox"><input v-model="form.proxy_enabled" type="checkbox" /> 允许代理访问（关闭会硬撤权）</label>
        <div v-if="formWarnings.length" class="banner banner--warn">
          <div>
            <div class="banner__title">提示</div>
            <ul><li v-for="warning in formWarnings" :key="warning">{{ warning }}</li></ul>
          </div>
        </div>
      </div>
      <template #footer>
        <button type="button" class="btn" :disabled="saving" @click="formOpen = false">取消</button>
        <button type="button" class="btn btn--primary" :disabled="saving" @click="saveMember">
          <span v-if="saving" class="spinner" aria-hidden="true" />
          {{ saving ? '保存中…' : '保存' }}
        </button>
      </template>
    </AppModal>

    <!-- 令牌轮换 -->
    <AppModal :open="tokenOpen" title="订阅令牌轮换" size="md" @close="tokenOpen = false">
      <div class="stack">
        <p class="text-sm text-muted">
          成员：<strong>{{ tokenTarget?.username }}</strong>。轮换只使旧订阅链接失效，不改变代理凭据、不中断现有连接。
        </p>
        <p v-if="tokenLoading" class="text-sm"><span class="spinner" aria-hidden="true" /> 正在轮换…</p>
        <p v-if="tokenError" class="banner banner--danger" role="alert">{{ tokenError }}</p>
        <template v-if="tokenResult">
          <div class="banner banner--info">旧令牌已失效；新链接仅在此处展示，请立即交付给成员。</div>
          <CopyField :value="tokenResult.url" label="订阅链接" mask warning="此链接等同于密码；请通过受控渠道交付。" />
          <CopyField :value="tokenResult.token" label="令牌" mask warning="令牌可重建订阅链接，勿公开。" />
          <QrPanel :value="tokenResult.url" />
        </template>
      </div>
      <template #footer><button type="button" class="btn" @click="tokenOpen = false">关闭</button></template>
    </AppModal>

    <!-- 凭据轮换 -->
    <AppModal :open="credOpen" title="代理凭据轮换" size="md" @close="credOpen = false">
      <div class="stack">
        <p class="text-sm text-muted">成员：<strong>{{ credTarget?.username }}</strong>。旧凭据立即失效（硬撤权），可能导致连接中断。</p>
        <div class="field">
          <label>范围</label>
          <label class="checkbox"><input v-model="credScope" type="radio" value="all_nodes" /> 该成员全部节点</label>
          <label class="checkbox"><input v-model="credScope" type="radio" value="single_node" /> 单个节点</label>
        </div>
        <div v-if="credScope === 'single_node'" class="field">
          <label for="cred-node">节点</label>
          <select id="cred-node" v-model="credNodeId" class="select">
            <option value="">请选择节点</option>
            <option v-for="node in nodes" :key="node.id" :value="node.id">{{ node.name }}（{{ node.protocol }}）</option>
          </select>
        </div>
        <p class="text-xs text-muted">影响：受影响节点上的旧客户端配置立即不可用；新凭据由 Agent 异步下发。</p>
        <p v-if="credError" class="banner banner--danger" role="alert">{{ credError }}</p>
        <p v-if="credDone" class="banner banner--info">轮换已提交；实际生效以 Agent 回执为准（任务页可追踪）。</p>
      </div>
      <template #footer>
        <button type="button" class="btn" @click="credOpen = false">关闭</button>
        <button type="button" class="btn btn--primary" :disabled="credPending" @click="submitCredential">
          <span v-if="credPending" class="spinner" aria-hidden="true" />
          {{ credPending ? '提交中…' : '确认轮换' }}
        </button>
      </template>
    </AppModal>

    <!-- 批量操作 -->
    <AppModal :open="batchOpen" title="批量操作（先预览）" size="md" @close="batchOpen = false">
      <div class="stack">
        <p class="text-sm text-muted">已选择 {{ selected.size }} 名成员。提交前必须生成预览；预览过期或成员集合变化后需重新预览。</p>
        <div class="field">
          <label for="batch-op">操作</label>
          <select id="batch-op" v-model="batchOperation" class="select">
            <option value="assign_subscription">分配 / 切换订阅</option>
            <option value="set_active">启用 / 停用账户</option>
            <option value="set_expiry">调整有效期</option>
            <option value="clear_override">清除个人覆盖</option>
          </select>
        </div>
        <div v-if="batchOperation === 'assign_subscription'" class="field">
          <label for="batch-sub">目标订阅</label>
          <select id="batch-sub" v-model="batchValues.subscription_id" class="select">
            <option value="">请选择</option>
            <option v-for="sub in subscriptions" :key="sub.id" :value="sub.id">{{ sub.name }}</option>
          </select>
        </div>
        <div v-if="batchOperation === 'set_active'" class="field">
          <label for="batch-active">账户状态</label>
          <select id="batch-active" v-model="batchValues.active" class="select">
            <option value="false">停用（撤销会话 + 硬撤权）</option>
            <option value="true">启用</option>
          </select>
        </div>
        <div v-if="batchOperation === 'set_expiry'" class="field">
          <label for="batch-expiry">新的到期时间</label>
          <input id="batch-expiry" v-model="batchValues.expires_at_local" class="input" type="datetime-local" />
          <p class="field__help">这是成员有效期，不是订阅周期规则；周期规则请在订阅页修改（同样需要预览）。</p>
        </div>
        <p v-if="batchOperation === 'clear_override'" class="text-sm">
          将清除所选成员的配额/有效期个人覆盖，恢复继承订阅默认值。
        </p>
        <p v-if="batchError" class="banner banner--warn" role="alert">{{ batchError }}</p>
      </div>
      <template #footer>
        <button type="button" class="btn" @click="batchOpen = false">取消</button>
        <button type="button" class="btn btn--primary" :disabled="!selected.size" @click="startBatchImpact">生成预览</button>
      </template>
    </AppModal>

    <ImpactPreviewDialog
      :open="impact.open"
      :operation="impact.operation"
      :member-ids="[...selected]"
      :values="impact.values"
      title="批量影响预览"
      description="预览只读：显示受影响成员、节点数与不可执行项；确认提交后规则在面板事务内生效，节点异步分发。"
      @close="impact.open = false"
    />

    <ConfirmDialog
      :open="deleteTarget !== null"
      title="删除成员"
      confirm-label="删除"
      danger
      :pending="deleting"
      :error-text="deleteError"
      @cancel="deleteTarget = null"
      @confirm="confirmDelete"
    >
      <p>将删除成员 <strong>{{ deleteTarget?.username }}</strong>（{{ deleteTarget?.id }}）。</p>
      <ul class="text-sm">
        <li>服务端对删除执行归档/墓碑，保留历史账本与审计。</li>
        <li>该成员所有会话与代理权限会被撤销；节点生效为异步。</li>
      </ul>
    </ConfirmDialog>
  </div>
</template>