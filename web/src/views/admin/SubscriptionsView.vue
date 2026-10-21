<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { adminApi } from '@/api/endpoints'
import type { SubscriptionRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText } from '@/lib/errors'
import { formatQuota } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

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
</script>

<template>
  <div>
    <PageHeader title="订阅" description="授权规则集合：节点组、每人配额、月周期与时区、默认有效期、访问开关。影响已有成员的修改需预览确认。">
      <router-link class="btn btn--primary" to="/admin/subscriptions/new">新建订阅</router-link>
    </PageHeader>

    <ErrorState v-if="list.error.value" :error="list.error.value" @retry="list.refresh()" />
    <LoadingBlock v-else-if="list.loading.value && !list.items.value.length" label="正在读取订阅" />
    <div v-else class="card card__body--flush">
      <div v-if="!list.items.value.length" class="card__body">
        <EmptyState title="还没有订阅规则" description="创建订阅并引用节点组，再把成员绑定到订阅；规则立即在面板生效，节点分发异步。">
          <router-link class="btn btn--primary" to="/admin/subscriptions/new">新建订阅</router-link>
        </EmptyState>
      </div>
      <template v-else>
        <div class="table-wrap">
          <table class="table">
            <thead>
              <tr>
                <th>名称</th>
                <th>节点组</th>
                <th>每人配额</th>
                <th class="hide-sm">周期</th>
                <th class="hide-sm">绑定成员</th>
                <th>状态</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="sub in list.items.value" :key="sub.id">
                <td>
                  <router-link :to="`/admin/subscriptions/${sub.id}`">{{ sub.name }}</router-link>
                  <span class="cell-sub mono">{{ sub.id }}</span>
                  <span v-if="sub.description" class="cell-sub">{{ sub.description }}</span>
                </td>
                <td>{{ sub.node_group_ids.length }} 组</td>
                <td class="num">{{ formatQuota(sub.quota_bytes) }}</td>
                <td class="hide-sm">
                  每月 {{ sub.anchor_day }} 日 00:00
                  <span class="cell-sub">{{ sub.timezone }}</span>
                </td>
                <td class="hide-sm num">{{ sub.bound_members ?? '—' }}</td>
                <td>
                  <div class="row" style="gap: var(--space-1)">
                    <StatusBadge v-if="sub.archived" status="archived" label="已归档" />
                    <StatusBadge v-else-if="!sub.access_enabled" status="paused" label="访问已暂停" />
                    <StatusBadge v-else status="enabled" label="可访问" />
                    <StatusBadge :status="sub.allow_new_bindings ? 'enabled' : 'disabled'" :label="sub.allow_new_bindings ? '允许新绑定' : '禁止新绑定'" />
                  </div>
                </td>
                <td class="actions">
                  <router-link class="btn btn--sm" :to="`/admin/subscriptions/${sub.id}`">编辑</router-link>
                  <button type="button" class="btn btn--sm btn--danger" @click="deleteTarget = sub">删除</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="row row--between" style="padding: var(--space-3) var(--space-4)">
          <span class="text-xs text-muted">第 {{ list.page.value }} 页 · 当前页绑定成员合计 {{ totalBound }}</span>
          <div class="row">
            <button type="button" class="btn btn--sm" :disabled="list.page.value <= 1 || list.loading.value" @click="list.prev()">上一页</button>
            <button type="button" class="btn btn--sm" :disabled="!list.nextCursor.value || list.loading.value" @click="list.next()">下一页</button>
          </div>
        </div>
      </template>
    </div>

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