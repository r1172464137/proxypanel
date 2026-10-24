<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { allPages } from '@/lib/allPages'
import { adminApi } from '@/api/endpoints'
import type { NodeRecord, NodeWrite, ServerRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText } from '@/lib/errors'
import { nodeSummary } from '@/lib/nodeDraft'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import DataTable from '@/components/DataTable.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'

const searchText = ref('')
const query = ref('')
const list = usePagedList<NodeRecord>((cursor) => adminApi.nodes.list({ limit: 50, cursor, q: query.value }))
function search(): void { query.value = searchText.value.trim(); selected.clear(); void list.refresh() }
watch(list.error, error => { if (error) selected.clear() })
const servers = ref<ServerRecord[]>([])

const deleteTarget = ref<NodeRecord | null>(null)
const deleting = ref(false)
const deleteError = ref('')

onMounted(async () => {
  void list.refresh()
  try {
    servers.value = await allPages(cursor => adminApi.servers.list({ limit: 200, cursor }))
  } catch {
    servers.value = []
  }
})

const serverNames = computed(() => {
  const map = new Map<string, string>()
  for (const server of servers.value) map.set(server.id, server.name)
  return map
})

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target) return
  deleting.value = true
  deleteError.value = ''
  try {
    await adminApi.nodes.remove(target.id, target.version)
    deleteTarget.value = null
    await list.refresh()
  } catch (caught) {
    deleteError.value = errorText(caught)
  } finally {
    deleting.value = false
  }
}

/* ---------- 行选择 ---------- */
// 用 Map 保存记录本身（而不只是 id）：跨页选择时仍能拿到 version 与节点字段，
// 批量操作才能对每一行发起真实的单条 PATCH / DELETE。
const selected = reactive(new Map<string, NodeRecord>())

const selectedOnCurrentPage = computed(() => list.items.value.filter((node) => selected.has(node.id)))
const selectedOnOtherPages = computed(() => Math.max(0, selected.size - selectedOnCurrentPage.value.length))
const allVisibleSelected = computed(
  () => list.items.value.length > 0 && list.items.value.every((node) => selected.has(node.id)),
)

const countLabel = computed(() => {
  const base = `本页 ${list.items.value.length} 个节点`
  if (!selectedOnOtherPages.value) return base
  return `${base} · 已选择 ${selected.size} 个（含其它页 ${selectedOnOtherPages.value} 个）`
})

function toggleSelect(node: NodeRecord): void {
  if (selected.has(node.id)) selected.delete(node.id)
  else selected.set(node.id, node)
}

function toggleSelectAll(): void {
  const rows = list.items.value
  const allSelected = rows.length > 0 && rows.every((node) => selected.has(node.id))
  for (const node of rows) {
    if (allSelected) selected.delete(node.id)
    else selected.set(node.id, node)
  }
}

/* ---------- 批量（逐条执行、失败不中断、最后汇总） ---------- */
type NodeBatchAction = 'enable' | 'disable' | 'delete'

interface NodeBatchTarget {
  id: string
  name: string
  version: number
  record: NodeRecord
}

interface BatchFailure {
  id: string
  name: string
  reason: string
}

const BATCH_ACTION_LABELS: Record<NodeBatchAction, string> = {
  enable: '批量启用',
  disable: '批量停用',
  delete: '批量删除',
}

const batch = reactive({
  open: false,
  action: 'enable' as NodeBatchAction,
  phase: 'confirm' as 'confirm' | 'running' | 'done',
  targets: [] as NodeBatchTarget[],
  total: 0,
  current: 0,
  currentName: '',
  successes: [] as string[],
  failures: [] as BatchFailure[],
})

const batchActionLabel = computed(() => BATCH_ACTION_LABELS[batch.action])

function openBatch(): void {
  if (!selected.size || list.loading.value || list.error.value || searchText.value.trim() !== query.value) return
  // 当前页的记录可能已被其它请求更新（例如上一次失败后列表已刷新）：
  // 打开批量时用列表里的最新 version/name 覆盖选择集，避免拿旧 version 重试必然 409。
  const current = new Map(list.items.value.map((node) => [node.id, node]))
  for (const id of [...selected.keys()]) {
    const fresh = current.get(id)
    if (fresh) selected.set(id, fresh)
  }
  batch.action = 'enable'
  batch.phase = 'confirm'
  // 打开时快照目标：执行过程中即使列表刷新，实际执行的仍是这一批对象。
  batch.targets = [...selected.values()].map((node) => ({
    id: node.id,
    name: node.name,
    version: node.version,
    record: node,
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
  const action = batch.action
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
    // 让进度文案先渲染出来，再发起这一条的请求。
    await nextTick()
    try {
      if (action === 'delete') {
        await adminApi.nodes.remove(target.id, target.version)
      } else {
        const record = target.record
        const payload: NodeWrite & { version: number } = {
          name: record.name,
          server_id: record.server_id,
          enabled: action === 'enable',
          host: record.host,
          port: record.port,
          listen: record.listen,
          protocol: record.protocol,
          flow: record.flow ?? null,
          cipher: record.cipher ?? null,
          stream_settings: record.stream_settings,
          protocol_settings: record.protocol_settings,
          client_settings: record.client_settings,
          sort_order: record.sort_order,
          version: target.version,
        }
        await adminApi.nodes.update(target.id, payload)
      }
      batch.successes.push(target.name)
      selected.delete(target.id)
    } catch (caught) {
      // 失败不中断：记录对象名与服务端原因，继续下一条。
      batch.failures.push({ id: target.id, name: target.name, reason: errorText(caught) })
    }
  }

  batch.phase = 'done'
  await list.refresh()
  if (batch.failures.length) {
    pushToast(
      'error',
      `${batchActionLabel.value}完成：成功 ${batch.successes.length} / 失败 ${batch.failures.length}`,
      '失败项已保留选择，可按原因处理后重试。',
    )
  } else {
    pushToast('success', `${batchActionLabel.value}完成`, `成功 ${batch.successes.length} 个，列表已刷新。`)
  }
}

// 原生 select 保持与其它页面一致的样式钩子（便于键盘/读屏与既有测试定位）。
const selectClass =
  'flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2'

const columns = [
  { label: '选择', width: 'w-12' },
  { label: '节点', width: 'w-56' },
  { label: '服务器', width: 'w-40' },
  { label: '协议 / 传输' },
  { label: '监听' },
  { label: '排序', width: 'w-20', hideOnMobile: true, numeric: true },
  { label: '状态', width: 'w-24' },
  { label: '操作', width: 'w-40' },
]
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="节点" description="节点就是一条代理线路。改完保存会让这条线路短暂断开几秒，正在用的连接会自动重连。">
      <Button as-child>
        <router-link to="/admin/nodes/new">新建节点</router-link>
      </Button>
    </PageHeader>

    <!-- 批量入口始终可见：没有数据时按钮禁用，但选择语义不变。 -->
    <div class="flex flex-wrap items-center gap-3">
      <span class="text-sm text-muted-foreground">勾选行后可批量启用、停用或删除；逐条执行，失败不会中断其余对象。</span>
      <div class="flex-1" />
      <div class="flex items-center gap-2">
        <Checkbox
          id="nodes-select-all"
          :model-value="allVisibleSelected"
          :disabled="!list.items.value.length"
          aria-label="选择当前页全部节点"
          @update:model-value="toggleSelectAll"
        />
        <Label for="nodes-select-all" class="text-sm font-normal text-muted-foreground">全选本页</Label>
      </div>
      <Button variant="outline" :disabled="!selected.size" @click="openBatch">
        批量操作（已选 {{ selected.size }}）
      </Button>
      <Button v-if="selected.size" variant="ghost" @click="selected.clear()">清空选择</Button>
    </div>

    <form class="flex flex-wrap gap-2" @submit.prevent="search"><label>搜索节点 <input v-model="searchText" class="rounded border p-2" aria-label="搜索节点" /></label><Button type="submit" :disabled="list.loading.value">搜索全部节点</Button></form>
    <DataTable
      :list="list"
      :columns="columns"
      empty-title="还没有节点"
      empty-description="先添加服务器，再为该服务器创建节点（VLESS / VMess / Trojan / Shadowsocks，含 2022）。"
      loading-label="正在读取节点"
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
          <router-link :to="`/admin/nodes/${item.id}`" class="font-medium hover:underline">{{ item.name }}</router-link>
          <span class="mt-0.5 block font-mono text-xs text-muted-foreground">{{ item.id }}</span>
        </template>
        <template v-else-if="columnIndex === 2">{{ serverNames.get(item.server_id) ?? item.server_id }}</template>
        <template v-else-if="columnIndex === 3">
          {{ nodeSummary(item) }}
          <span v-if="item.flow" class="mt-0.5 block font-mono text-xs text-muted-foreground">flow: {{ item.flow }}</span>
        </template>
        <template v-else-if="columnIndex === 4">
          <span class="font-mono text-xs">{{ item.listen }}:{{ item.port }} → {{ item.host }}</span>
        </template>
        <template v-else-if="columnIndex === 5">{{ item.sort_order }}</template>
        <template v-else-if="columnIndex === 6">
          <StatusBadge :status="item.enabled ? 'enabled' : 'disabled'" />
        </template>
        <template v-else>
          <div class="flex flex-wrap items-center gap-1.5">
            <Button as-child size="sm" variant="outline">
              <router-link :to="`/admin/nodes/${item.id}`">编辑</router-link>
            </Button>
            <Button size="sm" variant="destructive" @click="deleteTarget = item">删除</Button>
          </div>
        </template>
      </template>
      <template #empty-action>
        <Button as-child>
          <router-link to="/admin/nodes/new">新建节点</router-link>
        </Button>
      </template>
    </DataTable>

    <ConfirmDialog
      :open="deleteTarget !== null"
      title="删除节点"
      confirm-label="删除"
      danger
      :pending="deleting"
      :error-text="deleteError"
      @cancel="deleteTarget = null"
      @confirm="confirmDelete"
    >
      <p>将删除节点 <strong>{{ deleteTarget?.name }}</strong>（{{ deleteTarget?.host }}:{{ deleteTarget?.port }}）。</p>
      <ul class="list-disc space-y-1 pl-5 text-sm">
        <li>仍被节点组/订阅引用时服务端会拒绝（NODE_IN_USE）。</li>
        <li>删除后该节点对应核心实例会被停止，相关连接中断。</li>
      </ul>
    </ConfirmDialog>

    <!-- 批量：选择操作 → 确认对象清单 → 逐条执行 → 汇总结果 -->
    <AppModal
      :open="batch.open"
      :title="batch.phase === 'done' ? '批量操作结果' : '批量操作（逐条执行）'"
      size="md"
      @close="closeBatch"
    >
      <div class="space-y-4">
        <template v-if="batch.phase === 'confirm'">
          <p class="text-sm text-muted-foreground">
            已选择 {{ selected.size }} 个节点<template v-if="selectedOnOtherPages">（含其它页 {{ selectedOnOtherPages }} 个）</template>。
            前端会按顺序逐条调用单个接口；某条失败不会中断后续，全部结束后汇总「成功 / 失败」并刷新列表。
          </p>

          <div class="space-y-1.5">
            <Label for="node-batch-action">操作</Label>
            <select id="node-batch-action" v-model="batch.action" :class="selectClass">
              <option value="enable">批量启用</option>
              <option value="disable">批量停用</option>
              <option value="delete">批量删除</option>
            </select>
          </div>

          <div class="space-y-1.5">
            <span class="text-sm font-medium">将对以下 {{ batch.targets.length }} 个节点执行「{{ batchActionLabel }}」：</span>
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

          <p v-if="batch.action === 'delete'" class="text-sm text-destructive">
            删除不可撤销；仍被节点组/订阅引用的节点会被服务端拒绝，并计入失败明细。
          </p>
          <p v-else class="text-sm text-muted-foreground">
            启用/停用会更新节点的 enabled 字段；节点分发为异步，实际生效以任务/同步回执为准。
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
              {{ batchActionLabel }}完成：成功 {{ batch.successes.length }} / 失败 {{ batch.failures.length }}
              （共 {{ batch.total }} 个）。列表已刷新。
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
          <Button :variant="batch.action === 'delete' ? 'destructive' : 'default'" :disabled="!batch.targets.length" @click="runBatch">
            确认执行
          </Button>
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
