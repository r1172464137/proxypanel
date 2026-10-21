<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { adminApi } from '@/api/endpoints'
import { useResource } from '@/lib/async'
import { formatBytes, formatDateTime, formatNumber, utcLabel } from '@/lib/format'
import AppIcon from '@/components/AppIcon.vue'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'

const overview = useResource(() => adminApi.overview())

onMounted(() => void overview.reload())

const data = computed(() => overview.data.value)
const counts = computed(() => data.value?.counts)
const isEmpty = computed(() => {
  const c = counts.value
  return !!c && c.members === 0 && c.servers === 0 && c.nodes === 0 && c.subscriptions === 0
})
</script>

<template>
  <div>
    <PageHeader title="总览" description="面板实时聚合值；节点/Agent 的实际生效状态请以任务与运行时页面为准。">
      <button type="button" class="btn" :disabled="overview.loading.value" @click="overview.reload()">
        <span v-if="overview.loading.value" class="spinner" aria-hidden="true" />
        <AppIcon v-else name="refresh" :size="15" />
        刷新
      </button>
    </PageHeader>

    <LoadingBlock v-if="overview.loading.value && !data" label="正在读取总览" />
    <ErrorState v-else-if="overview.error.value" :error="overview.error.value" @retry="overview.reload()" />

    <template v-else-if="data">
      <!-- 主要计数：4 项；用量/在线/待办作为次要 3 项，信息层级分开但仍全部保留 -->
      <div class="overview-grid overview-grid--primary">
        <div class="kpi kpi--primary">
          <span class="kpi__label">
            <AppIcon class="kpi__icon" name="members" :size="14" />
            成员
          </span>
          <span class="kpi__value">{{ formatNumber(data.counts.members) }}</span>
          <span class="kpi__meta">账户总数（含停用/归档）</span>
        </div>
        <div class="kpi kpi--primary">
          <span class="kpi__label">
            <AppIcon class="kpi__icon" name="server" :size="14" />
            服务器
          </span>
          <span class="kpi__value">{{ formatNumber(data.counts.servers) }}</span>
          <span class="kpi__meta">机器档案</span>
        </div>
        <div class="kpi kpi--primary">
          <span class="kpi__label">
            <AppIcon class="kpi__icon" name="node" :size="14" />
            节点
          </span>
          <span class="kpi__value">{{ formatNumber(data.counts.nodes) }}</span>
          <span class="kpi__meta">代理入口</span>
        </div>
        <div class="kpi kpi--primary">
          <span class="kpi__label">
            <AppIcon class="kpi__icon" name="subscription" :size="14" />
            订阅规则
          </span>
          <span class="kpi__value">{{ formatNumber(data.counts.subscriptions) }}</span>
          <span class="kpi__meta">授权规则集合</span>
        </div>
      </div>

      <div class="overview-grid overview-grid--secondary">
        <div class="kpi kpi--compact">
          <span class="kpi__label">
            <AppIcon class="kpi__icon" name="usage" :size="14" />
            当期用量合计
          </span>
          <span class="kpi__value">{{ formatBytes(data.usage_bytes) }}</span>
          <span class="kpi__meta">字节基数 1024，按当前周期归属</span>
        </div>
        <div class="kpi kpi--compact">
          <span class="kpi__label">
            <AppIcon class="kpi__icon" name="status" :size="14" />
            在线 Agent
          </span>
          <span class="kpi__value">{{ formatNumber(data.agents_online) }}</span>
          <span class="kpi__meta">心跳在窗口内</span>
        </div>
        <div class="kpi kpi--compact">
          <span class="kpi__label">
            <AppIcon class="kpi__icon" name="tasks" :size="14" />
            待处理任务
          </span>
          <span class="kpi__value">{{ formatNumber(data.pending_tasks) }}</span>
          <span class="kpi__meta">
            <router-link to="/admin/tasks">查看任务与回执</router-link>
          </span>
        </div>
      </div>

      <div v-if="isEmpty" class="card" style="margin-top: var(--space-5)">
        <EmptyState title="尚未添加服务器" description="先登记服务器并生成一次性注册码，再添加节点与订阅规则。" icon="server">
          <router-link class="btn btn--primary" to="/admin/servers">新建服务器</router-link>
        </EmptyState>
      </div>

      <section class="card events-card">
        <div class="card__header">
          <AppIcon class="events-card__icon" name="events" :size="16" />
          <h2>近期事件</h2>
          <div class="card__header-actions">
            <router-link class="btn btn--sm" to="/admin/events">
              全部事件
              <AppIcon name="chevronRight" :size="14" />
            </router-link>
          </div>
        </div>
        <EmptyState v-if="!data.recent_events.length" title="暂无事件记录" icon="events" />
        <ul v-else class="event-list">
          <li v-for="event in data.recent_events" :key="event.id" class="event-list__item">
            <StatusBadge :status="event.severity ?? event.kind ?? 'unknown'" :label="event.severity ?? event.kind ?? '事件'" />
            <div class="event-list__body">
              <strong>{{ event.title ?? event.message ?? event.code ?? '事件' }}</strong>
              <p v-if="event.title && event.message" class="text-sm text-muted">{{ event.message }}</p>
            </div>
            <time class="text-xs text-faint nowrap" :title="utcLabel(event.created_at)">
              {{ formatDateTime(event.created_at) }}
            </time>
          </li>
        </ul>
      </section>
    </template>
  </div>
</template>

<style scoped>
.overview-grid {
  display: grid;
  gap: var(--space-4);
}

.overview-grid--primary {
  grid-template-columns: repeat(4, minmax(0, 1fr));
}

.overview-grid--secondary {
  grid-template-columns: repeat(3, minmax(0, 1fr));
  margin-top: var(--space-4);
}

.events-card {
  margin-top: var(--space-5);
}

.events-card__icon {
  color: var(--text-faint);
}

.event-list {
  list-style: none;
  margin: 0;
  padding: 0;
}

.event-list__item {
  display: flex;
  align-items: flex-start;
  gap: var(--space-3);
  padding: var(--space-3) var(--space-5);
  border-bottom: 1px solid var(--border);
  animation: rise var(--dur-slow) var(--ease-out) both;
}

.event-list__item:nth-child(2) { animation-delay: 30ms; }
.event-list__item:nth-child(3) { animation-delay: 60ms; }
.event-list__item:nth-child(4) { animation-delay: 90ms; }
.event-list__item:nth-child(5) { animation-delay: 120ms; }

.event-list__item:last-child {
  border-bottom: 0;
}

.event-list__body {
  flex: 1;
  min-width: 0;
}

@media (max-width: 1180px) {
  .overview-grid--primary {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 900px) {
  .overview-grid--secondary {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 640px) {
  .overview-grid--primary,
  .overview-grid--secondary {
    grid-template-columns: minmax(0, 1fr);
  }

  .event-list__item {
    flex-wrap: wrap;
  }
}
</style>
