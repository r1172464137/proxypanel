<script setup lang="ts">
import { computed, onMounted, reactive, ref, shallowRef, watch } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import { adminApi } from '@/api/endpoints'
import type { BatchOperation, MemberRecord, NodeRecord, SubscriptionRecord, TokenRotationResult } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText } from '@/lib/errors'
import { formatBytes, formatDateTime, formatQuota, fromLocalInput, utcLabel } from '@/lib/format'
import { buildMemberWrite, draftFromMember, emptyMemberDraft, quotaSource, type MemberDraft } from '@/lib/memberDraft'
import { pushToast } from '@/lib/toast'
import { accessReasonSummary, describeQuotaSource } from '@/lib/accessReasons'
import PageHeader from '@/components/PageHeader.vue'
import DataTable from '@/components/DataTable.vue'
import ErrorState from '@/components/ErrorState.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import CopyField from '@/components/CopyField.vue'
import QrPanel from '@/components/QrPanel.vue'
import ImpactPreviewDialog from '@/components/ImpactPreviewDialog.vue'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'

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

// DataTable 只负责呈现，数据仍来自后端游标分页；过滤只裁剪当前页的行，
// 因此把同一个 PagedList 的 items 换成过滤后的 shallowRef，其余（分页/错误/加载）沿用原对象。
const filteredItems = shallowRef<MemberRecord[]>([])
watch(visibleMembers, (value) => (filteredItems.value = value), { immediate: true })
const filteredList = { ...list, items: filteredItems }

const countLabel = computed(() => {
  const base = `本页 ${visibleMembers.value.length} 名`
  if (!selectedOnOtherPages.value) return base
  return `${base} · 已选择 ${selected.size} 名（含其它页 ${selectedOnOtherPages.value} 名）`
})

/* ---------- 创建 / 编辑 ---------- */
const formOpen = ref(false)
const form = reactive<MemberDraft>(emptyMemberDraft())

// 选中订阅的"每人配额"，用来在"跟随订阅"这一项上直接显示当前值，
// 让用户不点进订阅页也能知道跟的是多少。
const inheritedQuotaLabel = computed(() => {
  const sub = subscriptions.value.find((item) => item.id === form.subscription_id)
  if (!sub) return ''
  return formatQuota(sub.quota_bytes)
})
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

function setCredScope(value: unknown): void {
  credScope.value = value === 'single_node' ? 'single_node' : 'all_nodes'
}

function setQuotaMode(value: unknown): void {
  form.quota_override_enabled = value === 'override'
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

// 表单里的下拉保持原生 select：jsdom 测试可直接设值，提交逻辑与重写前一致。
const selectClass =
  'flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2'

const columns = [
  { label: '选择', width: 'w-12' },
  { label: '成员', width: 'w-52' },
  { label: '账户 / 代理', width: 'w-56' },
  { label: '订阅', width: 'w-40' },
  { label: '有效期', width: 'w-40', hideOnMobile: true },
  { label: '用量 / 额度', width: 'w-56' },
  { label: '周期', width: 'w-40', hideOnMobile: true },
  { label: '操作', width: 'w-80' },
]
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="成员" description="成员能不能登录、能不能用代理、有多少流量，是三件独立的事。批量改之前会先让你看一遍影响。">
      <Button variant="outline" :disabled="list.loading.value" @click="list.refresh()">刷新</Button>
      <Button @click="openCreate">新建成员</Button>
    </PageHeader>

    <ErrorState v-if="referenceError" :error="referenceError" @retry="list.refresh()" />

    <!-- 过滤与批量入口始终可见：空页/错误态下也能清空过滤或重新选择。 -->
    <div class="flex flex-wrap items-center gap-3">
      <Input
        v-model="filter"
        class="max-w-[280px]"
        placeholder="按用户名或 ID 过滤当前页"
        aria-label="过滤成员"
      />
      <span class="text-xs text-muted-foreground">过滤仅作用于当前页数据；跨页请用批量操作的显式成员集合。</span>
      <div class="flex-1" />
      <div class="flex items-center gap-2">
        <Checkbox
          id="members-select-all"
          :model-value="allVisibleSelected"
          :disabled="!visibleMembers.length"
          aria-label="选择当前页全部成员"
          @update:model-value="toggleSelectAll"
        />
        <Label for="members-select-all" class="text-sm font-normal text-muted-foreground">全选本页</Label>
      </div>
      <Button variant="outline" :disabled="!selected.size" @click="openBatch">
        批量操作（已选 {{ selected.size }}）
      </Button>
      <Button v-if="selected.size" variant="ghost" @click="selected.clear()">清空选择</Button>
    </div>

    <EmptyState
      v-if="list.items.value.length && !visibleMembers.length"
      title="当前页没有匹配的成员"
      description="过滤只作用于当前页数据；清空过滤词即可恢复。跨页请使用批量操作的显式成员集合。"
      icon="search"
    >
      <Button variant="outline" @click="filter = ''">清空过滤</Button>
    </EmptyState>

    <DataTable
      v-else
      :list="filteredList"
      :columns="columns"
      empty-title="还没有成员"
      empty-description="创建成员或生成邀请码；成员可登录查看状态，但代理权限取决于订阅与授权判定。"
      loading-label="正在读取成员"
      :count-label="countLabel"
    >
      <template #row="{ item, columnIndex }">
        <template v-if="columnIndex === 0">
          <Checkbox
            :model-value="selected.has(item.id)"
            :aria-label="`选择 ${item.username}`"
            @update:model-value="toggleSelect(item.id)"
          />
        </template>
        <template v-else-if="columnIndex === 1">
          <router-link :to="`/admin/members/${item.id}`" class="font-medium hover:underline">{{ item.username }}</router-link>
          <span class="mt-0.5 flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
            {{ item.role === 'admin' ? '管理员' : '成员' }}
            <Badge v-if="item.quota_override_enabled" variant="outline" class="border-transparent bg-brand text-brand-foreground">配额覆盖</Badge>
          </span>
        </template>
        <template v-else-if="columnIndex === 2">
          <div class="flex flex-wrap items-center gap-1.5">
            <StatusBadge :status="item.active ? 'active' : 'suspended'" :label="item.active ? '正常' : '已停用'" />
            <StatusBadge :status="item.proxy_access ?? (item.proxy_enabled ? 'enabled' : 'paused_by_admin')" />
            <Badge v-if="item.over_quota" variant="destructive">已超额</Badge>
          </div>
          <span v-if="item.access_reasons?.length" class="mt-0.5 block text-xs text-muted-foreground">
            {{ accessReasonSummary(item.access_reasons) }}
          </span>
        </template>
        <template v-else-if="columnIndex === 3">
          {{ item.subscription_id ? subscriptionNames.get(item.subscription_id) ?? item.subscription_id : '未绑定' }}
        </template>
        <template v-else-if="columnIndex === 4">
          <span :title="utcLabel(item.expires_at)">{{ formatDateTime(item.expires_at) }}</span>
        </template>
        <template v-else-if="columnIndex === 5">
          {{ formatBytes(item.used_bytes) }} / {{ formatQuota(item.effective_quota_bytes) }}
          <span class="mt-0.5 block text-xs text-muted-foreground">额度来源：{{ describeQuotaSource(quotaSource(item)) }}</span>
        </template>
        <template v-else-if="columnIndex === 6">
          <div v-if="item.period_start || item.period_end" class="text-xs">
            {{ formatDateTime(item.period_start, item.period_timezone) }} →<br />{{ formatDateTime(item.period_end, item.period_timezone) }}
          </div>
          <span v-else class="text-xs text-muted-foreground">未提供</span>
        </template>
        <template v-else>
          <div class="flex flex-wrap items-center gap-1.5">
            <Button size="sm" variant="outline" @click="openEdit(item)">编辑</Button>
            <Button size="sm" variant="outline" @click="toggleActive(item)">
              {{ item.active ? '停用' : '启用' }}
            </Button>
            <Button size="sm" variant="outline" @click="openCredential(item)">凭据</Button>
            <Button size="sm" variant="outline" @click="rotateToken(item)">订阅链接</Button>
            <Button size="sm" variant="destructive" @click="deleteTarget = item">删除</Button>
          </div>
        </template>
      </template>
      <template #empty-action>
        <Button @click="openCreate">新建成员</Button>
        <Button as-child variant="outline">
          <router-link to="/admin/invitations">生成邀请</router-link>
        </Button>
      </template>
    </DataTable>

    <div v-if="selected.size" class="sticky bottom-4 flex flex-wrap items-center gap-3 rounded-lg border border-border bg-muted/80 px-4 py-2 backdrop-blur">
      <span class="text-sm">已选择 {{ selected.size }} 名成员</span>
      <span class="text-xs text-muted-foreground">
        {{ selectedMembers.map((m) => m.username).join('、') }}{{ selectedOnOtherPages ? ` 等 ${selected.size} 名` : '' }}
      </span>
      <div class="flex-1" />
      <Button size="sm" @click="openBatch">批量操作</Button>
      <Button size="sm" variant="ghost" @click="selected.clear()">清空</Button>
    </div>

    <!-- 创建 / 编辑 -->
    <AppModal :open="formOpen" :title="formTitle" size="md" @close="formOpen = false">
      <div class="space-y-4">
        <Alert v-if="formError" variant="destructive" role="alert">
          <AlertTitle>保存失败</AlertTitle>
          <AlertDescription class="break-all">{{ formError }}</AlertDescription>
        </Alert>
        <Alert v-for="error in formErrors" :key="error" variant="destructive" role="alert">
          <AlertDescription>{{ error }}</AlertDescription>
        </Alert>

        <div class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-1.5">
            <Label for="member-username">用户名</Label>
            <Input id="member-username" v-model="form.username" autocomplete="off" />
          </div>
          <div class="space-y-1.5">
            <Label for="member-password">{{ editing ? '重置密码（留空不修改）' : '初始密码' }}</Label>
            <Input id="member-password" v-model="form.password" type="password" autocomplete="new-password" />
            <p class="text-xs text-muted-foreground">至少 10 位；成员输出永不包含密码或哈希</p>
          </div>
          <div class="space-y-1.5">
            <Label for="member-role">角色</Label>
            <select id="member-role" v-model="form.role" :class="selectClass">
              <option value="member">成员</option>
              <option value="admin">管理员</option>
            </select>
          </div>
          <div class="space-y-1.5">
            <Label for="member-sub">订阅</Label>
            <select id="member-sub" v-model="form.subscription_id" :class="selectClass">
              <option value="">不绑定</option>
              <option v-for="sub in subscriptions" :key="sub.id" :value="sub.id">{{ sub.name }}</option>
            </select>
          </div>
          <div class="space-y-1.5">
            <Label for="member-expires">到期时间</Label>
            <Input id="member-expires" v-model="form.expires_at_local" type="datetime-local" />
            <p class="text-xs text-muted-foreground">留空表示跟随订阅默认有效期</p>
          </div>
          <div class="space-y-2">
            <span class="text-sm font-medium leading-none">流量额度</span>
            <RadioGroup
              :model-value="form.quota_override_enabled ? 'override' : 'inherit'"
              @update:model-value="setQuotaMode"
            >
              <div class="flex items-start gap-2">
                <RadioGroupItem id="member-quota-inherit" value="inherit" class="mt-0.5" />
                <Label for="member-quota-inherit" class="font-normal leading-snug">
                  跟随订阅<template v-if="inheritedQuotaLabel">（当前 {{ inheritedQuotaLabel }}）</template>
                </Label>
              </div>
              <div class="flex items-start gap-2">
                <RadioGroupItem id="member-quota-override" value="override" class="mt-0.5" />
                <Label for="member-quota-override" class="font-normal leading-snug">单独给这个人设置</Label>
              </div>
            </RadioGroup>
            <div v-if="form.quota_override_enabled" class="flex items-center gap-2">
              <Input
                id="member-override"
                v-model="form.quota_override_gib"
                inputmode="decimal"
                placeholder="留空表示不限量"
                aria-label="单独设置的流量额度（GiB）"
              />
              <span class="text-sm text-muted-foreground">GiB</span>
            </div>
            <p class="text-xs text-muted-foreground">
              跟随订阅时，这个人的额度等于订阅里设的每人配额；
              单独设置只影响他自己，其他人不变。留空表示不限量。
            </p>
          </div>
        </div>

        <div class="space-y-1">
          <div class="flex cursor-pointer items-start gap-2" @click="form.active = !form.active">
            <Checkbox
              class="mt-0.5"
              :model-value="form.active"
              aria-label="允许登录"
              @update:model-value="(value) => (form.active = value === true)"
              @click.stop
            />
            <span class="text-sm font-medium leading-none">允许登录</span>
          </div>
          <p class="text-xs text-muted-foreground">
            关掉后这个人立刻无法登录，正在使用的会话失效。重新开启即可恢复，但他需要重新登录。
          </p>
        </div>

        <div class="space-y-1">
          <div class="flex cursor-pointer items-start gap-2" @click="form.proxy_enabled = !form.proxy_enabled">
            <Checkbox
              class="mt-0.5"
              :model-value="form.proxy_enabled"
              aria-label="允许使用代理"
              @update:model-value="(value) => (form.proxy_enabled = value === true)"
              @click.stop
            />
            <span class="text-sm font-medium leading-none">允许使用代理</span>
          </div>
          <p class="text-xs text-muted-foreground">
            关掉后他所有代理连接立即断开，客户端连不上。重新开启后需要重新拉取订阅。
          </p>
        </div>

        <Alert v-if="formWarnings.length">
          <AlertTitle>提示</AlertTitle>
          <AlertDescription>
            <ul class="list-disc space-y-1 pl-4">
              <li v-for="warning in formWarnings" :key="warning">{{ warning }}</li>
            </ul>
          </AlertDescription>
        </Alert>
      </div>
      <template #footer>
        <Button variant="outline" :disabled="saving" @click="formOpen = false">取消</Button>
        <Button :disabled="saving" @click="saveMember">
          <Loader2 v-if="saving" class="animate-spin" aria-hidden="true" />
          {{ saving ? '保存中…' : '保存' }}
        </Button>
      </template>
    </AppModal>

    <!-- 令牌轮换 -->
    <AppModal :open="tokenOpen" title="订阅令牌轮换" size="md" @close="tokenOpen = false">
      <div class="space-y-4">
        <p class="text-sm text-muted-foreground">
          成员：<strong class="text-foreground">{{ tokenTarget?.username }}</strong>。轮换只使旧订阅链接失效，不改变代理凭据、不中断现有连接。
        </p>
        <p v-if="tokenLoading" class="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 class="animate-spin" aria-hidden="true" /> 正在轮换…
        </p>
        <Alert v-if="tokenError" variant="destructive" role="alert">
          <AlertDescription class="break-all">{{ tokenError }}</AlertDescription>
        </Alert>
        <template v-if="tokenResult">
          <div class="rounded-lg border border-sky-200 bg-sky-50 px-4 py-3 text-sm text-sky-800 dark:border-sky-900 dark:bg-sky-950 dark:text-sky-200">
            旧令牌已失效；新链接仅在此处展示，请立即交付给成员。
          </div>
          <CopyField :value="tokenResult.url" label="订阅链接" mask warning="此链接等同于密码；请通过受控渠道交付。" />
          <CopyField :value="tokenResult.token" label="令牌" mask warning="令牌可重建订阅链接，勿公开。" />
          <QrPanel :value="tokenResult.url" />
        </template>
      </div>
      <template #footer>
        <Button variant="outline" @click="tokenOpen = false">关闭</Button>
      </template>
    </AppModal>

    <!-- 凭据轮换 -->
    <AppModal :open="credOpen" title="代理凭据轮换" size="md" @close="credOpen = false">
      <div class="space-y-4">
        <p class="text-sm text-muted-foreground">
          成员：<strong class="text-foreground">{{ credTarget?.username }}</strong>。旧凭据立即失效（硬撤权），可能导致连接中断。
        </p>
        <div class="space-y-2">
          <span class="text-sm font-medium leading-none">范围</span>
          <RadioGroup :model-value="credScope" @update:model-value="setCredScope">
            <div class="flex items-center gap-2">
              <RadioGroupItem id="cred-scope-all" value="all_nodes" />
              <Label for="cred-scope-all" class="font-normal">该成员全部节点</Label>
            </div>
            <div class="flex items-center gap-2">
              <RadioGroupItem id="cred-scope-single" value="single_node" />
              <Label for="cred-scope-single" class="font-normal">单个节点</Label>
            </div>
          </RadioGroup>
        </div>
        <div v-if="credScope === 'single_node'" class="space-y-1.5">
          <Label for="cred-node">节点</Label>
          <select id="cred-node" v-model="credNodeId" :class="selectClass">
            <option value="">请选择节点</option>
            <option v-for="node in nodes" :key="node.id" :value="node.id">{{ node.name }}（{{ node.protocol }}）</option>
          </select>
        </div>
        <p class="text-xs text-muted-foreground">影响：受影响节点上的旧客户端配置立即不可用；新凭据由 Agent 异步下发。</p>
        <Alert v-if="credError" variant="destructive" role="alert">
          <AlertDescription class="break-all">{{ credError }}</AlertDescription>
        </Alert>
        <div v-if="credDone" class="rounded-lg border border-sky-200 bg-sky-50 px-4 py-3 text-sm text-sky-800 dark:border-sky-900 dark:bg-sky-950 dark:text-sky-200">
          轮换已提交；实际生效以 Agent 回执为准（任务页可追踪）。
        </div>
      </div>
      <template #footer>
        <Button variant="outline" @click="credOpen = false">关闭</Button>
        <Button :disabled="credPending" @click="submitCredential">
          <Loader2 v-if="credPending" class="animate-spin" aria-hidden="true" />
          {{ credPending ? '提交中…' : '确认轮换' }}
        </Button>
      </template>
    </AppModal>

    <!-- 批量操作 -->
    <AppModal :open="batchOpen" title="批量操作（先预览）" size="md" @close="batchOpen = false">
      <div class="space-y-4">
        <p class="text-sm text-muted-foreground">
          已选择 {{ selected.size }} 名成员。提交前必须生成预览；预览过期或成员集合变化后需重新预览。
        </p>
        <div class="space-y-1.5">
          <Label for="batch-op">操作</Label>
          <select id="batch-op" v-model="batchOperation" :class="selectClass">
            <option value="assign_subscription">分配 / 切换订阅</option>
            <option value="set_active">启用 / 停用账户</option>
            <option value="set_expiry">调整有效期</option>
            <option value="clear_override">清除个人覆盖</option>
          </select>
        </div>
        <div v-if="batchOperation === 'assign_subscription'" class="space-y-1.5">
          <Label for="batch-sub">目标订阅</Label>
          <select id="batch-sub" v-model="batchValues.subscription_id" :class="selectClass">
            <option value="">请选择</option>
            <option v-for="sub in subscriptions" :key="sub.id" :value="sub.id">{{ sub.name }}</option>
          </select>
        </div>
        <div v-if="batchOperation === 'set_active'" class="space-y-1.5">
          <Label for="batch-active">账户状态</Label>
          <select id="batch-active" v-model="batchValues.active" :class="selectClass">
            <option value="false">停用（撤销会话 + 硬撤权）</option>
            <option value="true">启用</option>
          </select>
        </div>
        <div v-if="batchOperation === 'set_expiry'" class="space-y-1.5">
          <Label for="batch-expiry">新的到期时间</Label>
          <Input id="batch-expiry" v-model="batchValues.expires_at_local" type="datetime-local" />
          <p class="text-xs text-muted-foreground">这是成员有效期，不是订阅周期规则；周期规则请在订阅页修改（同样需要预览）。</p>
        </div>
        <p v-if="batchOperation === 'clear_override'" class="text-sm">
          将清除所选成员的配额/有效期个人覆盖，恢复继承订阅默认值。
        </p>
        <Alert v-if="batchError" variant="destructive" role="alert">
          <AlertDescription>{{ batchError }}</AlertDescription>
        </Alert>
      </div>
      <template #footer>
        <Button variant="outline" @click="batchOpen = false">取消</Button>
        <Button :disabled="!selected.size" @click="startBatchImpact">生成预览</Button>
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
      <ul class="list-disc space-y-1 pl-5 text-sm">
        <li>服务端对删除执行归档/墓碑，保留历史账本与审计。</li>
        <li>该成员所有会话与代理权限会被撤销；节点生效为异步。</li>
      </ul>
    </ConfirmDialog>
  </div>
</template>
