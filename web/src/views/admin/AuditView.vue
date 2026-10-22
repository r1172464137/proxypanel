<script setup lang="ts">
import { onMounted } from 'vue'
import { adminApi } from '@/api/endpoints'
import type { AuditRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { formatDateTime, prettyJson, utcLabel } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'

const list = usePagedList<AuditRecord>((cursor) => adminApi.auditLogs.list({ limit: 50, cursor }))

onMounted(() => void list.refresh())
</script>

<template>
  <div>
    <PageHeader title="审计" description="管理操作与安全事件留痕；服务端脱敏，不记录密码、令牌或私钥。">
      <button type="button" class="btn" :disabled="list.loading.value" @click="list.refresh()">刷新</button>
    </PageHeader>

    <ErrorState v-if="list.error.value" :error="list.error.value" @retry="list.refresh()" />
    <LoadingBlock v-else-if="list.loading.value && !list.items.value.length" label="正在读取审计" />
    <div v-else class="card card__body--flush">
      <div v-if="!list.items.value.length" class="card__body">
        <EmptyState title="没有审计记录" description="操作发生后会写入审计；此接口不返回秘密值。" />
      </div>
      <template v-else>
        <div class="table-wrap">
          <table class="table">
            <thead>
              <tr>
                <th>时间</th>
                <th>动作</th>
                <th>操作者</th>
                <th>对象 / 来源</th>
                <th>详情</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="entry in list.items.value" :key="entry.id">
                <td class="nowrap" :title="utcLabel(entry.created_at)">{{ formatDateTime(entry.created_at) }}</td>
                <td>{{ entry.action ?? '—' }}</td>
                <td>
                  <template v-if="entry.actor_username">{{ entry.actor_username }}</template>
                  <template v-else-if="entry.actor_id">{{ entry.actor_id }}</template>
                  <span v-else class="text-muted">匿名<template v-if="entry.action === 'auth.login'">（登录事件本身没有操作者）</template></span>
                </td>
                <td class="text-xs">
                  {{ entry.target_type ?? '—' }}
                  <span class="cell-sub mono">{{ entry.target_id ?? '' }}</span><span class="cell-sub" v-if="entry.ip || entry.request_id">来源 {{ entry.ip ?? '—' }} · {{ entry.request_id ?? '—' }}</span>
                </td>
                <td>
                  <details>
                    <summary class="text-xs text-muted">展开</summary>
                    <pre class="code-block">{{ prettyJson(entry.detail ?? entry.details ?? entry) }}</pre>
                  </details>
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
  </div>
</template>