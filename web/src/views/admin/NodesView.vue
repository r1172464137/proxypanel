<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { adminApi } from '@/api/endpoints'
import type { NodeRecord, ServerRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText } from '@/lib/errors'
import { nodeSummary } from '@/lib/nodeDraft'
import PageHeader from '@/components/PageHeader.vue'
import DataTable from '@/components/DataTable.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { Button } from '@/components/ui/button'

const list = usePagedList<NodeRecord>((cursor) => adminApi.nodes.list({ limit: 50, cursor }))
const servers = ref<ServerRecord[]>([])

const deleteTarget = ref<NodeRecord | null>(null)
const deleting = ref(false)
const deleteError = ref('')

onMounted(async () => {
  void list.refresh()
  try {
    servers.value = (await adminApi.servers.list({ limit: 200 })).items
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

const columns = [
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

    <DataTable
      :list="list"
      :columns="columns"
      empty-title="还没有节点"
      empty-description="先添加服务器，再为该服务器创建节点（VLESS / VMess / Trojan / Shadowsocks，含 2022）。"
      loading-label="正在读取节点"
      :count-label="`本页 ${list.items.value.length} 个节点`"
    >
      <template #row="{ item, columnIndex }">
        <template v-if="columnIndex === 0">
          <router-link :to="`/admin/nodes/${item.id}`" class="font-medium hover:underline">{{ item.name }}</router-link>
          <span class="mt-0.5 block font-mono text-xs text-muted-foreground">{{ item.id }}</span>
        </template>
        <template v-else-if="columnIndex === 1">{{ serverNames.get(item.server_id) ?? item.server_id }}</template>
        <template v-else-if="columnIndex === 2">
          {{ nodeSummary(item) }}
          <span v-if="item.flow" class="mt-0.5 block font-mono text-xs text-muted-foreground">flow: {{ item.flow }}</span>
        </template>
        <template v-else-if="columnIndex === 3">
          <span class="font-mono text-xs">{{ item.listen }}:{{ item.port }} → {{ item.host }}</span>
        </template>
        <template v-else-if="columnIndex === 4">{{ item.sort_order }}</template>
        <template v-else-if="columnIndex === 5">
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
  </div>
</template>
