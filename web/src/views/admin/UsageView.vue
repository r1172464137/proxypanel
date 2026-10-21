<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { adminApi } from '@/api/endpoints'
import type { MemberRecord, NicCounter, NodeRecord, UsageItem } from '@/api/types'
import { formatBytes, formatDateTime, fromLocalInput, prettyJson, utcLabel } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'

const items = ref<UsageItem[]>([])
const nicCounters = ref<NicCounter[]>([])
const nextCursor = ref<string | null>(null)
const loading = ref(false)
const error = ref<unknown>(null)
const members = ref<MemberRecord[]>([])
const nodes = ref<NodeRecord[]>([])

const filters = ref({ member_id: '', node_id: '', from_local: '', to_local: '' })

onMounted(async () => {
  try {
    const [memberPage, nodePage] = await Promise.all([
      adminApi.members.list({ limit: 200 }),
      adminApi.nodes.list({ limit: 200 }),
    ])
    members.value = memberPage.items
    nodes.value = nodePage.items
  } catch {
    members.value = []
    nodes.value = []
  }
  await query()
})

const memberNames = computed(() => new Map(members.value.map((m) => [m.id, m.username])))
const nodeNames = computed(() => new Map(nodes.value.map((n) => [n.id, n.name])))

function requestParams() {
  return {
    member_id: filters.value.member_id || undefined,
    node_id: filters.value.node_id || undefined,
    from: fromLocalInput(filters.value.from_local) ?? undefined,
    to: fromLocalInput(filters.value.to_local) ?? undefined,
  }
}

async function query(append = false): Promise<void> {
  loading.value = true
  error.value = null
  try {
    const result = await adminApi.usage.query({
      ...requestParams(),
      limit: 100,
      cursor: append ? nextCursor.value : null,
    })
    items.value = append ? [...items.value, ...result.items] : result.items
    nicCounters.value = result.nic_counters
    nextCursor.value = result.next_cursor
  } catch (caught) {
    error.value = caught
    if (!append) {
      items.value = []
      nicCounters.value = []
      nextCursor.value = null
    }
  } finally {
    loading.value = false
  }
}

function resetFilters(): void {
  filters.value = { member_id: '', node_id: '', from_local: '', to_local: '' }
  void query()
}

function total(item: UsageItem): number {
  return (item.up_bytes ?? 0) + (item.down_bytes ?? 0)
}

function exportCsv(): void {
  const header = ['member_id', 'member', 'node_id', 'node', 'period_start', 'period_end', 'up_bytes', 'down_bytes', 'estimated']
  const rows = items.value.map((item) => [
    item.member_id ?? '',
    memberNames.value.get(String(item.member_id)) ?? '',
    item.node_id ?? '',
    nodeNames.value.get(String(item.node_id)) ?? '',
    item.period?.start ?? '',
    item.period?.end ?? '',
    String(item.up_bytes ?? 0),
    String(item.down_bytes ?? 0),
    item.estimated ? 'true' : 'false',
  ])
  const csv = [header, ...rows]
    .map((row) => row.map((cell) => `"${String(cell).replaceAll('"', '""')}"`).join(','))
    .join('\r\n')
  const blob = new Blob([`\uFEFF${csv}`], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `usage-${new Date().toISOString().slice(0, 10)}.csv`
  link.click()
  URL.revokeObjectURL(url)
}
</script>

<template>
  <div>
    <PageHeader title="用量" description="按成员 / 节点 / 时间查询。计量以账本为准；跨周期补报按采样区间分摊并标注估计值，网卡流量单列且不参与配额判断。">
      <button type="button" class="btn" :disabled="!items.length" @click="exportCsv">导出当前结果 CSV</button>
      <button type="button" class="btn" :disabled="loading" @click="query()">刷新</button>
    </PageHeader>

    <div class="card" style="margin-bottom: var(--space-4)">
      <div class="card__body grid grid--form">
        <div class="field">
          <label for="usage-member">成员</label>
          <select id="usage-member" v-model="filters.member_id" class="select">
            <option value="">全部</option>
            <option v-for="member in members" :key="member.id" :value="member.id">{{ member.username }}</option>
          </select>
        </div>
        <div class="field">
          <label for="usage-node">节点</label>
          <select id="usage-node" v-model="filters.node_id" class="select">
            <option value="">全部</option>
            <option v-for="node in nodes" :key="node.id" :value="node.id">{{ node.name }}</option>
          </select>
        </div>
        <p class="field__help" style="grid-column: 1 / -1">
          时间范围按契约传给服务端（from/to）；若后端尚未实现该筛选，结果会包含全部周期，请以周期列核对。
        </p>
        <div class="field">
          <label for="usage-from">起始时间</label>
          <input id="usage-from" v-model="filters.from_local" class="input" type="datetime-local" />
        </div>
        <div class="field">
          <label for="usage-to">结束时间</label>
          <input id="usage-to" v-model="filters.to_local" class="input" type="datetime-local" />
        </div>
        <div class="row" style="align-self: end">
          <button type="button" class="btn btn--primary" :disabled="loading" @click="query()">查询</button>
          <button type="button" class="btn" :disabled="loading" @click="resetFilters">重置</button>
        </div>
      </div>
    </div>

    <LoadingBlock v-if="loading && !items.length" label="正在查用量" />
    <ErrorState v-else-if="error" :error="error" @retry="query()" />
    <div v-else class="stack">
      <div class="card card__body--flush">
        <div v-if="!items.length" class="card__body">
          <EmptyState title="当前周期没有上报数据" description="Agent 每 5 秒采样并批量上报；如刚启动或刚刚绑定成员，请稍后再查询。" />
        </div>
        <template v-else>
          <div class="table-wrap">
            <table class="table">
              <thead>
                <tr>
                  <th>成员</th>
                  <th>节点</th>
                  <th class="hide-sm">周期</th>
                  <th>上行</th>
                  <th>下行</th>
                  <th>合计</th>
                  <th>标记</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(item, index) in items" :key="`${item.member_id}-${item.node_id}-${index}`">
                  <td>
                    {{ memberNames.get(String(item.member_id)) ?? item.member_id ?? '—' }}
                    <span class="cell-sub mono">{{ item.member_id ?? '' }}</span>
                  </td>
                  <td>
                    {{ nodeNames.get(String(item.node_id)) ?? item.node_id ?? '—' }}
                  </td>
                  <td class="hide-sm text-xs">
                    <template v-if="item.period">
                      <span :title="utcLabel(item.period_start ?? item.period?.start)">{{ formatDateTime(item.period_start ?? item.period?.start, item.period?.timezone) }}</span>
                      →
                      <span :title="utcLabel(item.period_end ?? item.period?.end)">{{ formatDateTime(item.period_end ?? item.period?.end, item.period?.timezone) }}</span>
                    </template>
                    <span v-else class="text-muted">未提供</span>
                  </td>
                  <td class="num">{{ formatBytes(item.up_bytes) }}</td>
                  <td class="num">{{ formatBytes(item.down_bytes) }}</td>
                  <td class="num">{{ formatBytes(total(item)) }}</td>
                  <td>
                    <div class="row" style="gap: var(--space-1)">
                      <StatusBadge v-if="item.estimated" status="estimated" />
                      <StatusBadge v-if="item.boundary_split" status="estimated" label="跨周期分摊" />
                      <span v-if="item.correction || item.corrections?.length" class="badge badge--info">更正</span>
                      <span v-if="!item.estimated && !item.boundary_split && !item.correction && !item.corrections?.length" class="text-muted text-xs">—</span>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <div class="row row--between" style="padding: var(--space-3) var(--space-4)">
            <span class="text-xs text-muted">已加载 {{ items.length }} 条 · 计量方向 {{ items[0]?.direction ?? '未提供' }} · 算法 {{ items[0]?.algorithm_version ?? '未提供' }}</span>
            <button v-if="nextCursor" type="button" class="btn btn--sm" :disabled="loading" @click="query(true)">加载更多</button>
          </div>
        </template>
      </div>

      <section class="card">
        <div class="card__header"><h2>网卡流量（独立计量）</h2></div>
        <div v-if="!nicCounters.length" class="card__body text-sm text-muted">服务端未返回网卡计数。</div>
        <div v-else class="table-wrap">
          <table class="table">
            <thead><tr><th>服务器</th><th>接收</th><th>发送</th></tr></thead>
            <tbody>
              <tr v-for="(counter, index) in nicCounters" :key="`${counter.server_id}-${index}`">
                <td class="mono">{{ counter.server_id ?? '—' }}</td>
                <td class="num">{{ formatBytes(counter.rx_bytes) }}</td>
                <td class="num">{{ formatBytes(counter.tx_bytes) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="text-xs text-muted" style="padding: var(--space-3) var(--space-4)">网卡流量含协议开销与其它进程流量，不参与成员配额判定。</p>
      </section>

      <details v-if="items.length">
        <summary class="text-sm text-muted">查看服务端原始字段</summary>
        <pre class="code-block">{{ prettyJson(items) }}</pre>
      </details>
    </div>
  </div>
</template>