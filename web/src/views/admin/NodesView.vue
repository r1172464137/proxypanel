<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { adminApi } from '@/api/endpoints'
import type { NodeRecord, ServerRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText } from '@/lib/errors'
import { nodeSummary } from '@/lib/nodeDraft'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

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
</script>

<template>
  <div>
    <PageHeader title="节点" description="节点是代理入口：协议、传输、安全与客户端参数分别编辑；保存后默认重启该节点的独立核心实例。">
      <router-link class="btn btn--primary" to="/admin/nodes/new">新建节点</router-link>
    </PageHeader>

    <ErrorState v-if="list.error.value" :error="list.error.value" @retry="list.refresh()" />
    <LoadingBlock v-else-if="list.loading.value && !list.items.value.length" label="正在读取节点" />
    <div v-else class="card card__body--flush">
      <div v-if="!list.items.value.length" class="card__body">
        <EmptyState title="还没有节点" description="先添加服务器，再为该服务器创建节点（VLESS / VMess / Trojan / Shadowsocks，含 2022）。">
          <router-link class="btn btn--primary" to="/admin/nodes/new">新建节点</router-link>
        </EmptyState>
      </div>
      <template v-else>
        <div class="table-wrap">
          <table class="table">
            <thead>
              <tr>
                <th>节点</th>
                <th>服务器</th>
                <th>协议 / 传输</th>
                <th>监听</th>
                <th class="hide-sm">排序</th>
                <th>状态</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="node in list.items.value" :key="node.id">
                <td>
                  <router-link :to="`/admin/nodes/${node.id}`">{{ node.name }}</router-link>
                  <span class="cell-sub mono">{{ node.id }}</span>
                </td>
                <td>{{ serverNames.get(node.server_id) ?? node.server_id }}</td>
                <td>
                  {{ nodeSummary(node) }}
                  <span v-if="node.flow" class="cell-sub">flow: {{ node.flow }}</span>
                </td>
                <td class="mono">{{ node.listen }}:{{ node.port }} → {{ node.host }}</td>
                <td class="hide-sm num">{{ node.sort_order }}</td>
                <td><StatusBadge :status="node.enabled ? 'enabled' : 'disabled'" /></td>
                <td class="actions">
                  <router-link class="btn btn--sm" :to="`/admin/nodes/${node.id}`">编辑</router-link>
                  <button type="button" class="btn btn--sm btn--danger" @click="deleteTarget = node">删除</button>
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
      <ul class="text-sm">
        <li>仍被节点组/订阅引用时服务端会拒绝（NODE_IN_USE）。</li>
        <li>删除后该节点对应核心实例会被停止，相关连接中断。</li>
      </ul>
    </ConfirmDialog>
  </div>
</template>