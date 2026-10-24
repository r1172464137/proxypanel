<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import { adminApi } from '@/api/endpoints'
import { allPages } from '@/lib/allPages'
import type { AgentRecord, RegistrationCode, ServerRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText, toApiError } from '@/lib/errors'
import { formatDate, formatDateTime, formatRelative, fromLocalInput, toLocalInput } from '@/lib/format'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import DataTable from '@/components/DataTable.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import CopyField from '@/components/CopyField.vue'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

const list = usePagedList<ServerRecord>((cursor) => adminApi.servers.list({ limit: 50, cursor }))

/* ---------- Agent 在线状态 ---------- */
// 服务端 agents 接口返回真实 Agent 记录（含 RFC3339 的 last_heartbeat_at）。
// 这里只按 server_id 匹配并计算心跳差值，不把 online 字段当作唯一依据。
const agents = ref<AgentRecord[]>([])
const agentsError = ref(false)

// 浏览器时钟可能比服务器慢几秒（实测 WSL 比面板慢 1.3~2s）。
// 心跳时间落在"未来"时 age 为负，但那是刚心跳，不能判离线。
const CLOCK_SKEW_TOLERANCE_MS = 10_000
const HEARTBEAT_ONLINE_WINDOW_MS = 60_000

async function loadAgents(): Promise<void> {
  try {
    agents.value = await allPages(cursor => adminApi.agents.list({ limit: 200, cursor }))
    agentsError.value = false
  } catch {
    agents.value = []
    agentsError.value = true
  }
}

onMounted(async () => {
  void list.refresh()
  await loadAgents()
})

async function refreshAll(): Promise<void> {
  await Promise.all([list.refresh(), loadAgents()])
}

function heartbeatMs(agent: AgentRecord): number | null {
  const raw = agent.last_heartbeat_at ?? agent.heartbeat_at
  if (!raw) return null
  const value = Date.parse(raw)
  return Number.isNaN(value) ? null : value
}

/** 同一 server 可能有多条 Agent 记录（重装/重注册）；取心跳最新的一条。 */
function agentForServer(serverId: string): AgentRecord | null {
  let best: AgentRecord | null = null
  let bestHeartbeat = -1
  for (const agent of agents.value) {
    if (agent.server_id !== serverId) continue
    const heartbeat = heartbeatMs(agent) ?? -1
    if (heartbeat > bestHeartbeat) {
      best = agent
      bestHeartbeat = heartbeat
    }
  }
  return best
}

interface ServerPresence {
  kind: 'online' | 'offline' | 'missing' | 'unknown'
  badge: string
  label: string
  detail: string
}

function presenceOf(server: ServerRecord): ServerPresence {
  if (agentsError.value) {
    return { kind: 'unknown', badge: 'unknown', label: '状态未知', detail: '无法读取 Agent 列表' }
  }
  const agent = agentForServer(server.id)
  if (!agent) {
    return { kind: 'missing', badge: 'unknown', label: '未接入', detail: '该服务器还没有 Agent 注册记录' }
  }
  const heartbeat = heartbeatMs(agent)
  if (heartbeat === null) {
    return { kind: 'offline', badge: 'offline', label: '离线', detail: 'Agent 尚未上报心跳' }
  }
  const lastSeen = agent.last_heartbeat_at ?? agent.heartbeat_at ?? ''
  const age = Date.now() - heartbeat
  if (age > -CLOCK_SKEW_TOLERANCE_MS && age < HEARTBEAT_ONLINE_WINDOW_MS) {
    return { kind: 'online', badge: 'online', label: '在线', detail: `${formatRelative(lastSeen)}上报心跳` }
  }
  return { kind: 'offline', badge: 'offline', label: '离线', detail: `最后心跳 ${formatDateTime(lastSeen)}` }
}

interface ServerFormState {
  id: string | null
  version: number | null
  name: string
  address: string
  region: string
  notes: string
  expires_at_local: string
  cost_minor: string
  currency: string
}

function emptyForm(): ServerFormState {
  return { id: null, version: null, name: '', address: '', region: '', notes: '', expires_at_local: '', cost_minor: '', currency: 'CNY' }
}

const formOpen = ref(false)
const form = reactive<ServerFormState>(emptyForm())
const formError = ref('')
const saving = ref(false)
const formErrors = ref<string[]>([])

const deleteTarget = ref<ServerRecord | null>(null)
const deleting = ref(false)
const deleteError = ref('')

const codeOpen = ref(false)
const code = ref<RegistrationCode | null>(null)
const codeError = ref('')
const codeLoading = ref(false)
const codeTarget = ref<ServerRecord | null>(null)

const title = computed(() => (form.id ? `编辑服务器 · ${form.name}` : '新建服务器'))

function openCreate(): void {
  Object.assign(form, emptyForm())
  formErrors.value = []
  formError.value = ''
  formOpen.value = true
}

function openEdit(server: ServerRecord): void {
  Object.assign(form, {
    id: server.id,
    version: server.version,
    name: server.name,
    address: server.address,
    region: server.region ?? '',
    notes: server.notes ?? '',
    expires_at_local: toLocalInput(server.expires_at),
    cost_minor: server.cost_minor === null || server.cost_minor === undefined ? '' : String(server.cost_minor),
    currency: server.currency ?? 'CNY',
  })
  formErrors.value = []
  formError.value = ''
  formOpen.value = true
}

function validate(): string[] {
  const errors: string[] = []
  if (!form.name.trim()) errors.push('名称必填')
  if (!form.address.trim()) errors.push('地址必填')
  if (form.cost_minor.trim()) {
    const cost = Number(form.cost_minor)
    if (!Number.isInteger(cost) || cost < 0) errors.push('成本必须是非负整数（最小货币单位）')
  }
  if (form.currency.trim().length !== 3) errors.push('货币代码必须是 3 位（如 CNY）')
  return errors
}

async function save(): Promise<void> {
  formErrors.value = validate()
  formError.value = ''
  if (formErrors.value.length) return
  saving.value = true
  try {
    const payload = {
      name: form.name.trim(),
      address: form.address.trim(),
      region: form.region.trim() || null,
      notes: form.notes.trim() || null,
      expires_at: fromLocalInput(form.expires_at_local),
      cost_minor: form.cost_minor.trim() ? Number(form.cost_minor) : null,
      currency: form.cost_minor.trim() ? form.currency.trim().toUpperCase() : null,
    }
    if (form.id && form.version !== null) {
      await adminApi.servers.update(form.id, { ...payload, version: form.version })
    } else {
      await adminApi.servers.create(payload)
    }
    formOpen.value = false
    await list.refresh()
  } catch (caught) {
    formError.value = errorText(caught)
  } finally {
    saving.value = false
  }
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target) return
  deleting.value = true
  deleteError.value = ''
  try {
    await adminApi.servers.remove(target.id, target.version)
    deleteTarget.value = null
    await list.refresh()
  } catch (caught) {
    deleteError.value = toApiError(caught).message
  } finally {
    deleting.value = false
  }
}

async function generateCode(server: ServerRecord): Promise<void> {
  codeTarget.value = server
  code.value = null
  codeError.value = ''
  codeOpen.value = true
  codeLoading.value = true
  try {
    code.value = await adminApi.servers.registrationCode(server.id)
  } catch (caught) {
    codeError.value = errorText(caught)
  } finally {
    codeLoading.value = false
  }
}

/* ---------- 行选择 ---------- */
// 跨页选择保存记录本身：批量归档时要带上各自的 version 发起真实 DELETE。
const selected = reactive(new Map<string, ServerRecord>())

const selectedOnCurrentPage = computed(() => list.items.value.filter((server) => selected.has(server.id)))
const selectedOnOtherPages = computed(() => Math.max(0, selected.size - selectedOnCurrentPage.value.length))
const allVisibleSelected = computed(
  () => list.items.value.length > 0 && list.items.value.every((server) => selected.has(server.id)),
)

const countLabel = computed(() => {
  const base = `本页 ${list.items.value.length} 台`
  if (!selectedOnOtherPages.value) return base
  return `${base} · 已选择 ${selected.size} 台（含其它页 ${selectedOnOtherPages.value} 台）`
})

function toggleSelect(server: ServerRecord): void {
  if (selected.has(server.id)) selected.delete(server.id)
  else selected.set(server.id, server)
}

function toggleSelectAll(): void {
  const rows = list.items.value
  const allSelected = rows.length > 0 && rows.every((server) => selected.has(server.id))
  for (const server of rows) {
    if (allSelected) selected.delete(server.id)
    else selected.set(server.id, server)
  }
}

/* ---------- 批量归档 / 删除（逐条执行、失败不中断、最后汇总） ---------- */
interface ServerBatchTarget {
  id: string
  name: string
  version: number
}

interface BatchFailure {
  id: string
  name: string
  reason: string
}

const batch = reactive({
  open: false,
  phase: 'confirm' as 'confirm' | 'running' | 'done',
  targets: [] as ServerBatchTarget[],
  total: 0,
  current: 0,
  currentName: '',
  successes: [] as string[],
  failures: [] as BatchFailure[],
})

function openBatch(): void {
  if (!selected.size) return
  // 当前页记录可能已被其它请求更新；打开批量时用列表最新 version/name 覆盖选择集。
  const current = new Map(list.items.value.map((server) => [server.id, server]))
  for (const id of [...selected.keys()]) {
    const fresh = current.get(id)
    if (fresh) selected.set(id, fresh)
  }
  batch.phase = 'confirm'
  batch.targets = [...selected.values()].map((server) => ({
    id: server.id,
    name: server.name,
    version: server.version,
  }))
  batch.total = batch.targets.length
  batch.current = 0
  batch.currentName = ''
  batch.successes = []
  batch.failures = []
  batch.open = true
}

function closeBatch(): void {
  if (batch.phase === 'running') return
  batch.open = false
}

async function runBatch(): Promise<void> {
  const targets = [...batch.targets]
  batch.phase = 'running'
  batch.total = targets.length
  batch.current = 0
  batch.currentName = ''
  batch.successes = []
  batch.failures = []

  for (let index = 0; index < targets.length; index += 1) {
    const target = targets[index]!
    batch.current = index + 1
    batch.currentName = target.name
    await nextTick()
    try {
      // 服务端 DELETE 对服务器执行归档（保留历史账本），与单条「归档」一致。
      await adminApi.servers.remove(target.id, target.version)
      batch.successes.push(target.name)
      selected.delete(target.id)
    } catch (caught) {
      batch.failures.push({ id: target.id, name: target.name, reason: errorText(caught) })
    }
  }

  batch.phase = 'done'
  await Promise.all([list.refresh(), loadAgents()])
  if (batch.failures.length) {
    pushToast(
      'error',
      `批量归档完成：成功 ${batch.successes.length} / 失败 ${batch.failures.length}`,
      '失败项已保留选择，可按原因处理后重试。',
    )
  } else {
    pushToast('success', '批量归档完成', `成功 ${batch.successes.length} 台，列表已刷新。`)
  }
}

const columns = [
  { label: '选择', width: 'w-12' },
  { label: '名称', width: 'w-56' },
  { label: '地址' },
  { label: '地区', width: 'w-28', hideOnMobile: true },
  { label: '到期', width: 'w-36', hideOnMobile: true },
  { label: '成本', width: 'w-32', hideOnMobile: true, numeric: true },
  { label: '在线状态', width: 'w-32' },
  { label: '操作', width: 'w-56' },
]
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="服务器" description="机器档案与 Agent 注册入口。注册码一次性、15 分钟内有效。">
      <Button variant="outline" :disabled="list.loading.value" @click="refreshAll">刷新</Button>
      <Button @click="openCreate">新建服务器</Button>
    </PageHeader>

    <!-- 批量入口始终可见：没有数据时按钮禁用，但选择语义不变。 -->
    <div class="flex flex-wrap items-center gap-3">
      <span class="text-sm text-muted-foreground">勾选行后可批量归档/删除；逐条执行，失败不会中断其余对象。</span>
      <div class="flex-1" />
      <div class="flex items-center gap-2">
        <Checkbox
          id="servers-select-all"
          :model-value="allVisibleSelected"
          :disabled="!list.items.value.length"
          aria-label="选择当前页全部服务器"
          @update:model-value="toggleSelectAll"
        />
        <Label for="servers-select-all" class="text-sm font-normal text-muted-foreground">全选本页</Label>
      </div>
      <Button variant="outline" :disabled="!selected.size" @click="openBatch">
        批量操作（已选 {{ selected.size }}）
      </Button>
      <Button v-if="selected.size" variant="ghost" @click="selected.clear()">清空选择</Button>
    </div>

    <DataTable
      :list="list"
      :columns="columns"
      empty-title="还没有服务器档案"
      empty-description="登记服务器地址与到期信息后，可生成一次性注册码给该机器的 Agent。"
      loading-label="正在读取服务器"
      :count-label="countLabel"
    >
      <template #row="{ item, columnIndex }">
        <template v-if="columnIndex === 0">
          <Checkbox
            :model-value="selected.has(item.id)"
            :aria-label="`选择 ${item.name}`"
            @update:model-value="toggleSelect(item)"
          />
        </template>
        <template v-else-if="columnIndex === 1">
          <router-link :to="`/admin/servers/${item.id}/runtime`" class="font-medium hover:underline">{{ item.name }}</router-link>
          <span class="mt-0.5 block font-mono text-xs text-muted-foreground">{{ item.id }}</span>
        </template>
        <template v-else-if="columnIndex === 2">
          <span class="font-mono text-xs">{{ item.address }}</span>
        </template>
        <template v-else-if="columnIndex === 3">{{ item.region || '—' }}</template>
        <template v-else-if="columnIndex === 4">
          <span :title="item.expires_at ?? ''">{{ formatDate(item.expires_at) }}</span>
        </template>
        <template v-else-if="columnIndex === 5">
          <template v-if="item.cost_minor !== null && item.cost_minor !== undefined">
            {{ item.cost_minor }} {{ item.currency ?? '' }}
          </template>
          <template v-else>—</template>
        </template>
        <template v-else-if="columnIndex === 6">
          <StatusBadge :status="presenceOf(item).badge" :label="presenceOf(item).label" />
          <span class="mt-0.5 block text-xs text-muted-foreground">{{ presenceOf(item).detail }}</span>
        </template>
        <template v-else>
          <div class="flex flex-wrap items-center gap-1.5">
            <Button size="sm" variant="outline" @click="generateCode(item)">注册码</Button>
            <Button size="sm" variant="outline" @click="openEdit(item)">编辑</Button>
            <Button size="sm" variant="destructive" @click="deleteTarget = item">归档</Button>
          </div>
        </template>
      </template>
      <template #empty-action>
        <Button @click="openCreate">新建服务器</Button>
      </template>
    </DataTable>

    <AppModal :open="formOpen" :title="title" size="md" @close="formOpen = false">
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
            <Label for="server-name">名称</Label>
            <Input id="server-name" v-model="form.name" required />
          </div>
          <div class="space-y-1.5">
            <Label for="server-address">地址</Label>
            <Input id="server-address" v-model="form.address" class="font-mono" placeholder="IP 或域名" required />
          </div>
          <div class="space-y-1.5">
            <Label for="server-region">地区</Label>
            <Input id="server-region" v-model="form.region" />
          </div>
          <div class="space-y-1.5">
            <Label for="server-expires">到期时间</Label>
            <Input id="server-expires" v-model="form.expires_at_local" type="datetime-local" />
            <p class="text-xs text-muted-foreground">按浏览器本地时间输入，提交为 UTC</p>
          </div>
          <div class="space-y-1.5">
            <Label for="server-cost">成本（元）</Label>
            <Input id="server-cost" v-model="form.cost_minor" inputmode="numeric" placeholder="如 5000 = 50.00" />
          </div>
          <div class="space-y-1.5">
            <Label for="server-currency">货币</Label>
            <Input id="server-currency" v-model="form.currency" maxlength="3" />
          </div>
        </div>

        <div class="space-y-1.5">
          <Label for="server-notes">备注</Label>
          <Textarea id="server-notes" v-model="form.notes" class="min-h-20" />
        </div>
      </div>
      <template #footer>
        <Button variant="outline" :disabled="saving" @click="formOpen = false">取消</Button>
        <Button :disabled="saving" @click="save">
          <Loader2 v-if="saving" class="animate-spin" aria-hidden="true" />
          {{ saving ? '保存中…' : '保存' }}
        </Button>
      </template>
    </AppModal>

    <ConfirmDialog
      :open="deleteTarget !== null"
      title="归档服务器"
      confirm-label="归档"
      danger
      :pending="deleting"
      :error-text="deleteError"
      @cancel="deleteTarget = null"
      @confirm="confirmDelete"
    >
      <p>将归档服务器 <strong>{{ deleteTarget?.name }}</strong>。</p>
      <ul class="list-disc space-y-1 pl-5 text-sm">
        <li>仍有关联 Agent/节点时服务端会拒绝并给出原因。</li>
        <li>归档保留历史记录，不物理删除账本。</li>
      </ul>
    </ConfirmDialog>

    <AppModal :open="codeOpen" title="一次性 Agent 注册码" size="sm" @close="codeOpen = false">
      <div class="space-y-4">
        <p class="text-sm text-muted-foreground">
          服务器：<strong class="text-foreground">{{ codeTarget?.name }}</strong> · 仅可使用一次，15 分钟内有效；关闭后不保证能再次查看。
        </p>
        <p v-if="codeLoading" class="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 class="animate-spin" aria-hidden="true" /> 正在生成…
        </p>
        <Alert v-if="codeError" variant="destructive" role="alert">
          <AlertDescription class="break-all">{{ codeError }}</AlertDescription>
        </Alert>
        <template v-if="code">
          <CopyField :value="code.code" label="注册码" warning="注册码等同于 Agent 身份凭据；交付后不要留存明文。" />
          <p class="text-xs text-muted-foreground">过期时间：{{ formatDateTime(code.expires_at) }}</p>
        </template>
      </div>
      <template #footer>
        <Button variant="outline" @click="codeOpen = false">关闭</Button>
      </template>
    </AppModal>

    <!-- 批量：确认对象清单 → 逐条执行 → 汇总结果 -->
    <AppModal
      :open="batch.open"
      :title="batch.phase === 'done' ? '批量操作结果' : '批量归档服务器（逐条执行）'"
      size="md"
      @close="closeBatch"
    >
      <div class="space-y-4">
        <template v-if="batch.phase === 'confirm'">
          <p class="text-sm text-muted-foreground">
            已选择 {{ selected.size }} 台服务器<template v-if="selectedOnOtherPages">（含其它页 {{ selectedOnOtherPages }} 台）</template>。
            前端会按顺序逐条调用单个 DELETE（服务端对服务器执行归档，保留历史账本）；某条失败不会中断后续，
            全部结束后汇总「成功 / 失败」并刷新列表。
          </p>
          <div class="space-y-1.5">
            <span class="text-sm font-medium">将对以下 {{ batch.targets.length }} 台服务器执行批量归档/删除：</span>
            <ul
              aria-label="批量操作对象"
              class="max-h-56 space-y-1 overflow-y-auto rounded-md border border-border p-3 text-sm"
            >
              <li v-for="target in batch.targets" :key="target.id">
                {{ target.name }}
                <span class="font-mono text-xs text-muted-foreground">{{ target.id }}</span>
              </li>
            </ul>
          </div>
          <p class="text-sm text-destructive">
            归档不可撤销；仍有关联 Agent/节点的服务器会被服务端拒绝，并计入失败明细。归档保留历史记录，不物理删除账本。
          </p>
        </template>

        <template v-else-if="batch.phase === 'running'">
          <p role="status" class="text-sm">
            正在处理 {{ batch.current }}/{{ batch.total }}：<strong>{{ batch.currentName }}</strong>
          </p>
          <p class="text-sm text-muted-foreground">
            已完成 {{ batch.successes.length }} 条，失败 {{ batch.failures.length }} 条。
          </p>
        </template>

        <template v-else>
          <Alert :variant="batch.failures.length ? 'destructive' : 'default'">
            <AlertDescription>
              批量归档完成：成功 {{ batch.successes.length }} / 失败 {{ batch.failures.length }}
              （共 {{ batch.total }} 台）。列表已刷新。
            </AlertDescription>
          </Alert>
          <div v-if="batch.failures.length" class="space-y-1.5">
            <span class="text-sm font-medium">失败对象与原因</span>
            <ul aria-label="失败明细" class="max-h-56 space-y-1 overflow-y-auto rounded-md border border-border p-3 text-sm">
              <li v-for="failure in batch.failures" :key="failure.id">
                <strong>{{ failure.name }}</strong>：<span class="break-all">{{ failure.reason }}</span>
              </li>
            </ul>
          </div>
        </template>
      </div>

      <template #footer>
        <template v-if="batch.phase === 'confirm'">
          <Button variant="outline" @click="closeBatch">取消</Button>
          <Button variant="destructive" :disabled="!batch.targets.length" @click="runBatch">确认归档</Button>
        </template>
        <template v-else-if="batch.phase === 'running'">
          <Button disabled>处理中…</Button>
        </template>
        <template v-else>
          <Button @click="closeBatch">关闭</Button>
        </template>
      </template>
    </AppModal>
  </div>
</template>
