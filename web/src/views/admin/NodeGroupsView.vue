<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import { adminApi } from '@/api/endpoints'
import { allPages } from '@/lib/allPages'
import type { BatchOperation, NodeGroupRecord, NodeRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText, isPreviewRequired } from '@/lib/errors'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import DataTable from '@/components/DataTable.vue'
import ErrorState from '@/components/ErrorState.vue'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import ImpactPreviewDialog from '@/components/ImpactPreviewDialog.vue'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

const list = usePagedList<NodeGroupRecord>((cursor) => adminApi.nodeGroups.list({ limit: 50, cursor }))
const nodes = ref<NodeRecord[]>([])
const nodesError = ref<unknown>(null)

const formOpen = ref(false)
const form = reactive({ id: '', version: 0, name: '', description: '', node_ids: [] as string[] })
const saving = ref(false)
const formError = ref('')
const formErrors = ref<string[]>([])

const deleteTarget = ref<NodeGroupRecord | null>(null)
const deleting = ref(false)
const deleteError = ref('')

const impact = reactive<{ open: boolean; operation: BatchOperation; values: Record<string, unknown> }>({
  open: false,
  operation: 'update_node_group',
  values: {},
})

const nodeNames = computed(() => {
  const map = new Map<string, string>()
  for (const node of nodes.value) map.set(node.id, `${node.name} (${node.protocol})`)
  return map
})

onMounted(async () => {
  void list.refresh()
  try {
    nodes.value = await allPages(cursor => adminApi.nodes.list({ limit: 200, cursor }))
  } catch (caught) {
    nodesError.value = caught
  }
})

function openCreate(): void {
  Object.assign(form, { id: '', version: 0, name: '', description: '', node_ids: [] })
  formErrors.value = []
  formError.value = ''
  formOpen.value = true
}

function openEdit(group: NodeGroupRecord): void {
  Object.assign(form, {
    id: group.id,
    version: group.version,
    name: group.name,
    description: group.description ?? '',
    node_ids: [...group.node_ids],
  })
  formErrors.value = []
  formError.value = ''
  formOpen.value = true
}

function toggleNode(id: string): void {
  const index = form.node_ids.indexOf(id)
  if (index >= 0) form.node_ids.splice(index, 1)
  else form.node_ids.push(id)
}

async function save(): Promise<void> {
  formErrors.value = []
  formError.value = ''
  if (!form.name.trim()) formErrors.value.push('名称必填')
  if (!form.node_ids.length) formErrors.value.push('至少选择一个节点，否则该组无法输出任何节点')
  if (formErrors.value.length) return

  saving.value = true
  try {
    const payload = {
      name: form.name.trim(),
      description: form.description.trim() || null,
      node_ids: [...form.node_ids],
    }
    if (form.id) {
      try {
        await adminApi.nodeGroups.update(form.id, { ...payload, version: form.version })
      } catch (caught) {
        if (isPreviewRequired(caught)) {
          formOpen.value = false
          impact.values = { id: form.id, version: form.version, patch: payload }
          impact.open = true
          return
        }
        throw caught
      }
    } else {
      await adminApi.nodeGroups.create(payload)
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
    await adminApi.nodeGroups.remove(target.id, target.version)
    deleteTarget.value = null
    await list.refresh()
  } catch (caught) {
    deleteError.value = errorText(caught)
  } finally {
    deleting.value = false
  }
}

/* ---------- 行选择 ---------- */
// 跨页选择保存记录本身：批量删除时要带上各自的 version 发起真实 DELETE。
const selected = reactive(new Map<string, NodeGroupRecord>())

const selectedOnCurrentPage = computed(() => list.items.value.filter((group) => selected.has(group.id)))
const selectedOnOtherPages = computed(() => Math.max(0, selected.size - selectedOnCurrentPage.value.length))
const allVisibleSelected = computed(
  () => list.items.value.length > 0 && list.items.value.every((group) => selected.has(group.id)),
)

const countLabel = computed(() => {
  const base = `本页 ${list.items.value.length} 个节点组`
  if (!selectedOnOtherPages.value) return base
  return `${base} · 已选择 ${selected.size} 个（含其它页 ${selectedOnOtherPages.value} 个）`
})

function toggleSelect(group: NodeGroupRecord): void {
  if (selected.has(group.id)) selected.delete(group.id)
  else selected.set(group.id, group)
}

function toggleSelectAll(): void {
  const rows = list.items.value
  const allSelected = rows.length > 0 && rows.every((group) => selected.has(group.id))
  for (const group of rows) {
    if (allSelected) selected.delete(group.id)
    else selected.set(group.id, group)
  }
}

/* ---------- 批量删除（逐条执行、失败不中断、最后汇总） ---------- */
interface NodeGroupBatchTarget {
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
  targets: [] as NodeGroupBatchTarget[],
  total: 0,
  current: 0,
  currentName: '',
  successes: [] as string[],
  failures: [] as BatchFailure[],
})

function openBatch(): void {
  if (!selected.size) return
  // 当前页记录可能已被其它请求更新；打开批量时用列表最新 version/name 覆盖选择集。
  const current = new Map(list.items.value.map((group) => [group.id, group]))
  for (const id of [...selected.keys()]) {
    const fresh = current.get(id)
    if (fresh) selected.set(id, fresh)
  }
  batch.phase = 'confirm'
  batch.targets = [...selected.values()].map((group) => ({ id: group.id, name: group.name, version: group.version }))
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
      await adminApi.nodeGroups.remove(target.id, target.version)
      batch.successes.push(target.name)
      selected.delete(target.id)
    } catch (caught) {
      batch.failures.push({ id: target.id, name: target.name, reason: errorText(caught) })
    }
  }

  batch.phase = 'done'
  await list.refresh()
  if (batch.failures.length) {
    pushToast(
      'error',
      `批量删除完成：成功 ${batch.successes.length} / 失败 ${batch.failures.length}`,
      '失败项已保留选择，可按原因处理后重试。',
    )
  } else {
    pushToast('success', '批量删除完成', `成功 ${batch.successes.length} 个，列表已刷新。`)
  }
}

const columns = [
  { label: '选择', width: 'w-12' },
  { label: '名称', width: 'w-56' },
  { label: '节点' },
  { label: '说明', hideOnMobile: true },
  { label: '版本', width: 'w-20', numeric: true },
  { label: '操作', width: 'w-40' },
]
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="节点组" description="资源组织单位；被订阅引用。修改成员会影响已有授权，服务端要求先预览再提交。">
      <Button @click="openCreate">新建节点组</Button>
    </PageHeader>

    <ErrorState v-if="nodesError" :error="nodesError" @retry="list.refresh()" />

    <!-- 批量入口始终可见：没有数据时按钮禁用，但选择语义不变。 -->
    <div class="flex flex-wrap items-center gap-3">
      <span class="text-sm text-muted-foreground">勾选行后可批量删除；逐条执行，失败不会中断其余对象。</span>
      <div class="flex-1" />
      <div class="flex items-center gap-2">
        <Checkbox
          id="node-groups-select-all"
          :model-value="allVisibleSelected"
          :disabled="!list.items.value.length"
          aria-label="选择当前页全部节点组"
          @update:model-value="toggleSelectAll"
        />
        <Label for="node-groups-select-all" class="text-sm font-normal text-muted-foreground">全选本页</Label>
      </div>
      <Button variant="outline" :disabled="!selected.size" @click="openBatch">
        批量操作（已选 {{ selected.size }}）
      </Button>
      <Button v-if="selected.size" variant="ghost" @click="selected.clear()">清空选择</Button>
    </div>

    <p class="text-sm"><router-link class="underline" to="/admin/subscriptions/new">下一步：使用节点组建立服务方案</router-link> · <router-link class="underline" to="/admin/tasks">核对同步进度</router-link></p>
    <DataTable
      :list="list"
      :columns="columns"
      empty-title="还没有节点组"
      empty-description="把相关节点组合成组，再在订阅中引用；同一成员的多组节点会按节点去重输出。"
      loading-label="正在读取节点组"
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
          <span class="font-medium">{{ item.name }}</span>
          <span class="mt-0.5 block font-mono text-xs text-muted-foreground">{{ item.id }}</span>
        </template>
        <template v-else-if="columnIndex === 2">
          {{ item.node_ids.length }} 个
          <span class="mt-0.5 block text-xs text-muted-foreground">
            {{ item.node_ids.map((id) => nodeNames.get(id) ?? id).join('、') }}
          </span>
        </template>
        <template v-else-if="columnIndex === 3">
          <span class="text-sm">{{ item.description || '—' }}</span>
        </template>
        <template v-else-if="columnIndex === 4">{{ item.version }}</template>
        <template v-else>
          <div class="flex flex-wrap items-center gap-1.5">
            <Button size="sm" variant="outline" @click="openEdit(item)">编辑</Button>
            <Button size="sm" variant="destructive" @click="deleteTarget = item">删除</Button>
          </div>
        </template>
      </template>
      <template #empty-action>
        <Button @click="openCreate">新建节点组</Button>
      </template>
    </DataTable>

    <AppModal :open="formOpen" :title="form.id ? '编辑节点组' : '新建节点组'" @close="formOpen = false">
      <div class="space-y-4">
        <Alert v-if="formError" variant="destructive" role="alert">
          <AlertDescription class="break-all">{{ formError }}</AlertDescription>
        </Alert>
        <Alert v-for="error in formErrors" :key="error" variant="destructive" role="alert">
          <AlertDescription>{{ error }}</AlertDescription>
        </Alert>

        <div class="space-y-1.5">
          <Label for="group-name">名称</Label>
          <Input id="group-name" v-model="form.name" required />
        </div>
        <div class="space-y-1.5">
          <Label for="group-desc">说明</Label>
          <Textarea id="group-desc" v-model="form.description" class="min-h-[70px]" />
        </div>
        <div class="space-y-2">
          <Label>包含节点（{{ form.node_ids.length }}）</Label>
          <div class="max-h-72 space-y-1 overflow-y-auto rounded-md border border-border p-3">
            <div
              v-for="node in nodes"
              :key="node.id"
              class="flex cursor-pointer items-start gap-2 rounded-sm px-1 py-1.5 text-sm hover:bg-muted/60"
              @click="toggleNode(node.id)"
            >
              <Checkbox
                class="mt-0.5"
                :model-value="form.node_ids.includes(node.id)"
                @update:model-value="toggleNode(node.id)"
                @click.stop
              />
              <span class="select-none">
                {{ node.name }}
                <span class="block text-xs text-muted-foreground">{{ node.protocol }} · {{ node.host }}:{{ node.port }}</span>
              </span>
            </div>
            <p v-if="!nodes.length" class="text-sm text-muted-foreground">还没有节点，请先创建节点。</p>
          </div>
        </div>
        <p v-if="form.id" class="text-xs text-muted-foreground">
          若该组已被订阅/成员引用，直接保存会被拒绝并转入影响预览流程。
        </p>
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
      title="删除节点组"
      confirm-label="删除"
      danger
      :pending="deleting"
      :error-text="deleteError"
      @cancel="deleteTarget = null"
      @confirm="confirmDelete"
    >
      <p>将删除节点组 <strong>{{ deleteTarget?.name }}</strong>。</p>
      <p class="text-sm">仍被订阅引用时服务端会拒绝；删除不会删除节点本身。</p>
    </ConfirmDialog>

    <ImpactPreviewDialog
      :open="impact.open"
      :operation="impact.operation"
      :member-ids="[]"
      :values="impact.values"
      title="节点组成员变更影响预览"
      description="该节点组成员变化影响已有授权，服务端要求预览确认后再提交。"
      @close="impact.open = false"
    />

    <!-- 批量：确认对象清单 → 逐条执行 → 汇总结果 -->
    <AppModal
      :open="batch.open"
      :title="batch.phase === 'done' ? '批量操作结果' : '批量删除节点组（逐条执行）'"
      size="md"
      @close="closeBatch"
    >
      <div class="space-y-4">
        <template v-if="batch.phase === 'confirm'">
          <p class="text-sm text-muted-foreground">
            已选择 {{ selected.size }} 个节点组<template v-if="selectedOnOtherPages">（含其它页 {{ selectedOnOtherPages }} 个）</template>。
            前端会按顺序逐条调用单个 DELETE；某条失败不会中断后续，全部结束后汇总「成功 / 失败」并刷新列表。
          </p>
          <div class="space-y-1.5">
            <span class="text-sm font-medium">将对以下 {{ batch.targets.length }} 个节点组执行批量删除：</span>
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
            删除不可撤销；仍被订阅引用的节点组会被服务端拒绝，并计入失败明细。删除不会删除节点本身。
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
              批量删除完成：成功 {{ batch.successes.length }} / 失败 {{ batch.failures.length }}
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
          <Button variant="destructive" :disabled="!batch.targets.length" @click="runBatch">确认删除</Button>
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
