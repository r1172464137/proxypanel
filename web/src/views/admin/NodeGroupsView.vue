<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import { adminApi } from '@/api/endpoints'
import type { BatchOperation, NodeGroupRecord, NodeRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText, isPreviewRequired } from '@/lib/errors'
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
    nodes.value = (await adminApi.nodes.list({ limit: 200 })).items
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

const columns = [
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

    <DataTable
      :list="list"
      :columns="columns"
      empty-title="还没有节点组"
      empty-description="把相关节点组合成组，再在订阅中引用；同一成员的多组节点会按节点去重输出。"
      loading-label="正在读取节点组"
      :count-label="`本页 ${list.items.value.length} 个节点组`"
    >
      <template #row="{ item, columnIndex }">
        <template v-if="columnIndex === 0">
          <span class="font-medium">{{ item.name }}</span>
          <span class="mt-0.5 block font-mono text-xs text-muted-foreground">{{ item.id }}</span>
        </template>
        <template v-else-if="columnIndex === 1">
          {{ item.node_ids.length }} 个
          <span class="mt-0.5 block text-xs text-muted-foreground">
            {{ item.node_ids.map((id) => nodeNames.get(id) ?? id).join('、') }}
          </span>
        </template>
        <template v-else-if="columnIndex === 2">
          <span class="text-sm">{{ item.description || '—' }}</span>
        </template>
        <template v-else-if="columnIndex === 3">{{ item.version }}</template>
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
  </div>
</template>
