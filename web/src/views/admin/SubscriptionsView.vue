<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref } from 'vue'
import { adminApi } from '@/api/endpoints'
import type { SubscriptionRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText } from '@/lib/errors'
import { formatQuota } from '@/lib/format'
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

const list = usePagedList<SubscriptionRecord>((cursor) => adminApi.subscriptions.list({ limit: 50, cursor }))
const deleteTarget = ref<SubscriptionRecord | null>(null)
const deleting = ref(false)
const deleteError = ref('')

onMounted(() => void list.refresh())

const totalBound = computed(() =>
  list.items.value.reduce((sum, sub) => sum + (typeof sub.bound_members === 'number' ? sub.bound_members : 0), 0),
)

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target) return
  deleting.value = true
  deleteError.value = ''
  try {
    const result = await adminApi.subscriptions.remove(target.id, target.version)
    pushToast('success', result.retired ? '方案已退役，历史保留' : '方案已删除')
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
const selected = reactive(new Map<string, SubscriptionRecord>())

const selectedOnCurrentPage = computed(() => list.items.value.filter((sub) => selected.has(sub.id)))
const selectedOnOtherPages = computed(() => Math.max(0, selected.size - selectedOnCurrentPage.value.length))
const allVisibleSelected = computed(
  () => list.items.value.length > 0 && list.items.value.every((sub) => selected.has(sub.id)),
)

const countLabel = computed(() => {
  const base = `本页 ${list.items.value.length} 条 · 当前页绑定成员合计 ${totalBound.value}`
  if (!selectedOnOtherPages.value) return base
  return `${base} · 已选择 ${selected.size} 条（含其它页 ${selectedOnOtherPages.value} 条）`
})

function toggleSelect(sub: SubscriptionRecord): void {
  if (selected.has(sub.id)) selected.delete(sub.id)
  else selected.set(sub.id, sub)
}

function toggleSelectAll(): void {
  const rows = list.items.value
  const allSelected = rows.length > 0 && rows.every((sub) => selected.has(sub.id))
  for (const sub of rows) {
    if (allSelected) selected.delete(sub.id)
    else selected.set(sub.id, sub)
  }
}

/* ---------- 批量删除（逐条执行、失败不中断、最后汇总） ---------- */
interface SubscriptionBatchTarget {
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
  targets: [] as SubscriptionBatchTarget[],
  total: 0,
  current: 0,
  currentName: '',
  successes: [] as string[],
  failures: [] as BatchFailure[],
})

function openBatch(): void {
  if (!selected.size) return
  // 当前页记录可能已被其它请求更新；打开批量时用列表最新 version/name 覆盖选择集。
  const current = new Map(list.items.value.map((sub) => [sub.id, sub]))
  for (const id of [...selected.keys()]) {
    const fresh = current.get(id)
    if (fresh) selected.set(id, fresh)
  }
  batch.phase = 'confirm'
  batch.targets = [...selected.values()].map((sub) => ({ id: sub.id, name: sub.name, version: sub.version }))
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
      await adminApi.subscriptions.remove(target.id, target.version)
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
    pushToast('success', '批量删除完成', `成功 ${batch.successes.length} 条，列表已刷新。`)
  }
}

const columns = [
  { label: '选择', width: 'w-12' },
  { label: '名称', width: 'w-64' },
  { label: '节点组', width: 'w-20' },
  { label: '每人配额', width: 'w-28', numeric: true },
  { label: '周期', width: 'w-36', hideOnMobile: true },
  { label: '绑定成员', width: 'w-24', hideOnMobile: true, numeric: true },
  { label: '状态', width: 'w-48' },
  { label: '操作', width: 'w-40' },
]
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="服务方案" description="授权规则集合：节点组、每人配额、月周期与时区、默认有效期、访问开关。影响已有成员的修改需预览确认。">
      <Button as-child>
        <router-link to="/admin/subscriptions/new">新建订阅</router-link>
      </Button>
    </PageHeader>

    <!-- 批量入口始终可见：没有数据时按钮禁用，但选择语义不变。 -->
    <div class="flex flex-wrap items-center gap-3">
      <span class="text-sm text-muted-foreground">勾选行后可批量删除；逐条执行，失败不会中断其余对象。</span>
      <div class="flex-1" />
      <div class="flex items-center gap-2">
        <Checkbox
          id="subscriptions-select-all"
          :model-value="allVisibleSelected"
          :disabled="!list.items.value.length"
          aria-label="选择当前页全部订阅"
          @update:model-value="toggleSelectAll"
        />
        <Label for="subscriptions-select-all" class="text-sm font-normal text-muted-foreground">全选本页</Label>
      </div>
      <Button variant="outline" :disabled="!selected.size" @click="openBatch">
        批量操作（已选 {{ selected.size }}）
      </Button>
      <Button v-if="selected.size" variant="ghost" @click="selected.clear()">清空选择</Button>
    </div>

    <DataTable
      :list="list"
      :columns="columns"
      empty-title="还没有订阅规则"
      empty-description="创建订阅并引用节点组，再把成员绑定到订阅；规则立即在面板生效，节点分发异步。"
      loading-label="正在读取订阅"
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
          <router-link :to="`/admin/subscriptions/${item.id}`" class="font-medium hover:underline">{{ item.name }}</router-link>
          <span class="mt-0.5 block font-mono text-xs text-muted-foreground">{{ item.id }}</span>
          <span v-if="item.description" class="mt-0.5 block text-xs text-muted-foreground">{{ item.description }}</span>
        </template>
        <template v-else-if="columnIndex === 2">{{ item.node_group_ids.length }} 组</template>
        <template v-else-if="columnIndex === 3">{{ formatQuota(item.quota_bytes) }}</template>
        <template v-else-if="columnIndex === 4">
          每月 {{ item.anchor_day }} 日 00:00
          <span class="mt-0.5 block text-xs text-muted-foreground">{{ item.timezone }}</span>
        </template>
        <template v-else-if="columnIndex === 5">{{ item.bound_members ?? '—' }}</template>
        <template v-else-if="columnIndex === 6">
          <div class="flex flex-wrap items-center gap-1.5">
            <StatusBadge v-if="item.archived" status="archived" label="已归档" />
            <StatusBadge v-else-if="!item.access_enabled" status="paused" label="访问已暂停" />
            <StatusBadge v-else status="enabled" label="可访问" />
            <StatusBadge
              :status="item.allow_new_bindings ? 'enabled' : 'disabled'"
              :label="item.allow_new_bindings ? '允许新绑定' : '禁止新绑定'"
            />
          </div>
        </template>
        <template v-else>
          <div class="flex flex-wrap items-center gap-1.5">
            <Button as-child size="sm" variant="outline">
              <router-link :to="`/admin/subscriptions/${item.id}`">编辑</router-link>
            </Button>
            <Button size="sm" variant="destructive" @click="deleteTarget = item">删除</Button>
          </div>
        </template>
      </template>
      <template #empty-action>
        <Button as-child>
          <router-link to="/admin/subscriptions/new">新建订阅</router-link>
        </Button>
      </template>
    </DataTable>

    <ConfirmDialog
      :open="deleteTarget !== null"
      title="退役服务方案"
      confirm-label="退役方案"
      danger
      :pending="deleting"
      :error-text="deleteError"
      @cancel="deleteTarget = null"
      @confirm="confirmDelete"
    >
      <p>将退役服务方案 <strong>{{ deleteTarget?.name }}</strong>。</p>
      <p class="text-sm">仍有绑定成员时不能退役，请先迁移或解除绑定。有绑定历史时归档保留；没有历史时永久删除。历史账本和审计保留。</p>
    </ConfirmDialog>

    <!-- 批量：确认对象清单 → 逐条执行 → 汇总结果 -->
    <AppModal
      :open="batch.open"
      :title="batch.phase === 'done' ? '批量操作结果' : '批量删除订阅（逐条执行）'"
      size="md"
      @close="closeBatch"
    >
      <div class="space-y-4">
        <template v-if="batch.phase === 'confirm'">
          <p class="text-sm text-muted-foreground">
            已选择 {{ selected.size }} 条订阅<template v-if="selectedOnOtherPages">（含其它页 {{ selectedOnOtherPages }} 条）</template>。
            前端会按顺序逐条调用单个 DELETE；某条失败不会中断后续，全部结束后汇总「成功 / 失败」并刷新列表。
          </p>
          <div class="space-y-1.5">
            <span class="text-sm font-medium">将对以下 {{ batch.targets.length }} 条订阅执行批量删除：</span>
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
            删除不可撤销；仍有绑定成员的订阅会被服务端拒绝，并计入失败明细。归档/删除保留历史账本，不物理抹除审计。
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
              （共 {{ batch.total }} 条）。列表已刷新。
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
