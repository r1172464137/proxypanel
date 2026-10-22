<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { adminApi } from '@/api/endpoints'
import type { SubscriptionRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText } from '@/lib/errors'
import { formatQuota } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import DataTable from '@/components/DataTable.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { Button } from '@/components/ui/button'

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
    await adminApi.subscriptions.remove(target.id, target.version)
    deleteTarget.value = null
    await list.refresh()
  } catch (caught) {
    deleteError.value = errorText(caught)
  } finally {
    deleting.value = false
  }
}

const columns = [
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
    <PageHeader title="订阅" description="授权规则集合：节点组、每人配额、月周期与时区、默认有效期、访问开关。影响已有成员的修改需预览确认。">
      <Button as-child>
        <router-link to="/admin/subscriptions/new">新建订阅</router-link>
      </Button>
    </PageHeader>

    <DataTable
      :list="list"
      :columns="columns"
      empty-title="还没有订阅规则"
      empty-description="创建订阅并引用节点组，再把成员绑定到订阅；规则立即在面板生效，节点分发异步。"
      loading-label="正在读取订阅"
      :count-label="`本页 ${list.items.value.length} 条 · 当前页绑定成员合计 ${totalBound}`"
    >
      <template #row="{ item, columnIndex }">
        <template v-if="columnIndex === 0">
          <router-link :to="`/admin/subscriptions/${item.id}`" class="font-medium hover:underline">{{ item.name }}</router-link>
          <span class="mt-0.5 block font-mono text-xs text-muted-foreground">{{ item.id }}</span>
          <span v-if="item.description" class="mt-0.5 block text-xs text-muted-foreground">{{ item.description }}</span>
        </template>
        <template v-else-if="columnIndex === 1">{{ item.node_group_ids.length }} 组</template>
        <template v-else-if="columnIndex === 2">{{ formatQuota(item.quota_bytes) }}</template>
        <template v-else-if="columnIndex === 3">
          每月 {{ item.anchor_day }} 日 00:00
          <span class="mt-0.5 block text-xs text-muted-foreground">{{ item.timezone }}</span>
        </template>
        <template v-else-if="columnIndex === 4">{{ item.bound_members ?? '—' }}</template>
        <template v-else-if="columnIndex === 5">
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
      title="删除订阅"
      confirm-label="删除"
      danger
      :pending="deleting"
      :error-text="deleteError"
      @cancel="deleteTarget = null"
      @confirm="confirmDelete"
    >
      <p>将删除订阅 <strong>{{ deleteTarget?.name }}</strong>。</p>
      <p class="text-sm">仍有绑定成员时服务端会拒绝；归档/删除保留历史账本，不物理抹除审计。</p>
    </ConfirmDialog>
  </div>
</template>
