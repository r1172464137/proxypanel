<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { adminApi } from '@/api/endpoints'
import type { BatchOperation, NodeGroupRecord, NodeRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText, isPreviewRequired } from '@/lib/errors'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import ImpactPreviewDialog from '@/components/ImpactPreviewDialog.vue'

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
</script>

<template>
  <div>
    <PageHeader title="节点组" description="资源组织单位；被订阅引用。修改成员会影响已有授权，服务端要求先预览再提交。">
      <button type="button" class="btn btn--primary" @click="openCreate">新建节点组</button>
    </PageHeader>

    <ErrorState v-if="list.error.value" :error="list.error.value" @retry="list.refresh()" />
    <ErrorState v-if="nodesError" :error="nodesError" @retry="list.refresh()" />
    <LoadingBlock v-else-if="list.loading.value && !list.items.value.length" label="正在读取节点组" />
    <div v-else class="card card__body--flush">
      <div v-if="!list.items.value.length" class="card__body">
        <EmptyState title="还没有节点组" description="把相关节点组合成组，再在订阅中引用；同一成员的多组节点会按节点去重输出。">
          <button type="button" class="btn btn--primary" @click="openCreate">新建节点组</button>
        </EmptyState>
      </div>
      <template v-else>
        <div class="table-wrap">
          <table class="table">
            <thead>
              <tr>
                <th>名称</th>
                <th>节点</th>
                <th class="hide-sm">说明</th>
                <th>版本</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="group in list.items.value" :key="group.id">
                <td>
                  {{ group.name }}
                  <span class="cell-sub mono">{{ group.id }}</span>
                </td>
                <td>
                  {{ group.node_ids.length }} 个
                  <span class="cell-sub">{{ group.node_ids.map((id) => nodeNames.get(id) ?? id).join('、') }}</span>
                </td>
                <td class="hide-sm text-sm">{{ group.description || '—' }}</td>
                <td class="num">{{ group.version }}</td>
                <td class="actions">
                  <button type="button" class="btn btn--sm" @click="openEdit(group)">编辑</button>
                  <button type="button" class="btn btn--sm btn--danger" @click="deleteTarget = group">删除</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="row row--between" style="padding: var(--space-3) var(--space-4)">
          <span class="text-xs text-muted">第 {{ list.page.value }} 页</span>
          <div class="row">
            <button type="button" class="btn btn--sm" :disabled="list.page.value <= 1 || list.loading.value" @click="list.prev()">上一页</button>
            <button type="button" class="btn btn--sm" :disabled="!list.nextCursor.value || list.loading.value" @click="list.next()">下一页</button>
          </div>
        </div>
      </template>
    </div>

    <AppModal :open="formOpen" :title="form.id ? '编辑节点组' : '新建节点组'" @close="formOpen = false">
      <div class="stack">
        <p v-if="formError" class="banner banner--danger" role="alert">{{ formError }}</p>
        <p v-for="error in formErrors" :key="error" class="banner banner--warn">{{ error }}</p>
        <div class="field">
          <label for="group-name">名称</label>
          <input id="group-name" v-model="form.name" class="input" required />
        </div>
        <div class="field">
          <label for="group-desc">说明</label>
          <textarea id="group-desc" v-model="form.description" class="textarea" style="min-height: 70px" />
        </div>
        <div class="field">
          <label>包含节点（{{ form.node_ids.length }}）</label>
          <div class="node-picker">
            <label v-for="node in nodes" :key="node.id" class="checkbox node-picker__item">
              <input type="checkbox" :checked="form.node_ids.includes(node.id)" @change="toggleNode(node.id)" />
              <span>
                {{ node.name }}
                <span class="text-xs text-muted">{{ node.protocol }} · {{ node.host }}:{{ node.port }}</span>
              </span>
            </label>
            <p v-if="!nodes.length" class="text-sm text-muted">还没有节点，请先创建节点。</p>
          </div>
        </div>
        <p v-if="form.id" class="text-xs text-muted">若该组已被订阅/成员引用，直接保存会被拒绝并转入影响预览流程。</p>
      </div>
      <template #footer>
        <button type="button" class="btn" :disabled="saving" @click="formOpen = false">取消</button>
        <button type="button" class="btn btn--primary" :disabled="saving" @click="save">
          <span v-if="saving" class="spinner" aria-hidden="true" />
          {{ saving ? '保存中…' : '保存' }}
        </button>
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

<style scoped>
.node-picker {
  display: grid;
  gap: var(--space-2);
  max-height: 280px;
  overflow-y: auto;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  padding: var(--space-3);
}

.node-picker__item {
  align-items: flex-start;
}
</style>