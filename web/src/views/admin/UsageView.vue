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
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'

const items = ref<UsageItem[]>([])
const nicCounters = ref<NicCounter[]>([])
const nextCursor = ref<string | null>(null)
const loading = ref(false)
const error = ref<unknown>(null)
const members = ref<MemberRecord[]>([])
const nodes = ref<NodeRecord[]>([])

const filters = ref({ member_id: '', node_id: '', from_local: '', to_local: '' })

// reka-ui 的 Select 用非空字符串作为选项值；空串表示“全部”，这里做一层映射，
// 保证 requestParams() 仍然按原来的语义（空串 → undefined）发请求。
const ALL = '__all__'
const memberFilter = computed({
  get: () => filters.value.member_id || ALL,
  set: (value: string) => {
    filters.value.member_id = value === ALL ? '' : value
  },
})
const nodeFilter = computed({
  get: () => filters.value.node_id || ALL,
  set: (value: string) => {
    filters.value.node_id = value === ALL ? '' : value
  },
})

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
  <div class="space-y-4">
    <PageHeader
      title="用量"
      description="按成员或节点查流量。数字来自实际计费记录；补报的历史数据会标注「估算」。网卡总流量单独列出，不参与额度判断。"
    >
      <Button variant="outline" :disabled="!items.length" @click="exportCsv">导出当前结果 CSV</Button>
      <Button variant="outline" :disabled="loading" @click="query()">刷新</Button>
    </PageHeader>

    <Card>
      <CardContent class="grid gap-4 pt-6 sm:grid-cols-2 lg:grid-cols-4">
        <div class="grid gap-2">
          <Label for="usage-member">成员</Label>
          <Select v-model="memberFilter">
            <SelectTrigger id="usage-member" class="w-full">
              <SelectValue placeholder="全部" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem :value="ALL">全部</SelectItem>
              <SelectItem v-for="member in members" :key="member.id" :value="member.id">
                {{ member.username }}
              </SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div class="grid gap-2">
          <Label for="usage-node">节点</Label>
          <Select v-model="nodeFilter">
            <SelectTrigger id="usage-node" class="w-full">
              <SelectValue placeholder="全部" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem :value="ALL">全部</SelectItem>
              <SelectItem v-for="node in nodes" :key="node.id" :value="node.id">
                {{ node.name }}
              </SelectItem>
            </SelectContent>
          </Select>
        </div>
        <p class="text-xs text-muted-foreground sm:col-span-2 lg:col-span-4">
          时间范围按周期开始时间筛选。留空表示不限。
        </p>
        <div class="grid gap-2">
          <Label for="usage-from">统计起始时间</Label>
          <Input id="usage-from" v-model="filters.from_local" type="datetime-local" />
        </div>
        <div class="grid gap-2">
          <Label for="usage-to">统计结束时间</Label>
          <Input id="usage-to" v-model="filters.to_local" type="datetime-local" />
        </div>
        <div class="flex flex-wrap items-end gap-2 sm:col-span-2 lg:col-span-4">
          <Button :disabled="loading" @click="query()">查询</Button>
          <Button variant="outline" :disabled="loading" @click="resetFilters">重置</Button>
        </div>
      </CardContent>
    </Card>

    <LoadingBlock v-if="loading && !items.length" label="正在查用量" />
    <ErrorState v-else-if="error" :error="error" @retry="query()" />
    <div v-else class="space-y-4">
      <Card class="overflow-hidden">
        <CardContent v-if="!items.length" class="pt-6">
          <EmptyState
            title="当前周期没有上报数据"
            description="Agent 每 5 秒采样并批量上报；如刚启动或刚刚绑定成员，请稍后再查询。"
          />
        </CardContent>
        <template v-else>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>成员</TableHead>
                <TableHead>节点</TableHead>
                <TableHead class="hidden md:table-cell">周期</TableHead>
                <TableHead class="text-right">上行</TableHead>
                <TableHead class="text-right">下行</TableHead>
                <TableHead class="text-right">合计</TableHead>
                <TableHead>标记</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow
                v-for="(item, index) in items"
                :key="`${item.member_id}-${item.node_id}-${index}`"
              >
                <TableCell>
                  <div class="flex flex-col">
                    <span>{{ memberNames.get(String(item.member_id)) ?? item.member_id ?? '—' }}</span>
                    <span class="font-mono text-xs text-muted-foreground">{{ item.member_id ?? '' }}</span>
                  </div>
                </TableCell>
                <TableCell>
                  {{ nodeNames.get(String(item.node_id)) ?? item.node_id ?? '—' }}
                </TableCell>
                <TableCell class="hidden text-xs md:table-cell">
                  <template v-if="item.period">
                    <span :title="utcLabel(item.period_start ?? item.period?.start)">{{ formatDateTime(item.period_start ?? item.period?.start, item.period?.timezone) }}</span>
                    →
                    <span :title="utcLabel(item.period_end ?? item.period?.end)">{{ formatDateTime(item.period_end ?? item.period?.end, item.period?.timezone) }}</span>
                  </template>
                  <span v-else class="text-muted-foreground">未提供</span>
                </TableCell>
                <TableCell class="text-right tabular">{{ formatBytes(item.up_bytes) }}</TableCell>
                <TableCell class="text-right tabular">{{ formatBytes(item.down_bytes) }}</TableCell>
                <TableCell class="text-right tabular">{{ formatBytes(total(item)) }}</TableCell>
                <TableCell>
                  <div class="flex flex-wrap items-center gap-1">
                    <StatusBadge v-if="item.estimated" status="estimated" />
                    <StatusBadge v-if="item.boundary_split" status="estimated" label="跨周期分摊" />
                    <StatusBadge v-if="item.correction || item.corrections?.length" status="corrected" tone="info" label="更正" />
                    <span
                      v-if="!item.estimated && !item.boundary_split && !item.correction && !item.corrections?.length"
                      class="text-xs text-muted-foreground"
                    >
                      —
                    </span>
                  </div>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
          <div class="flex flex-wrap items-center justify-between gap-2 border-t border-border px-4 py-3">
            <span class="text-xs text-muted-foreground">
              已加载 {{ items.length }} 条 · 计量方向 {{ items[0]?.direction ?? '未提供' }} · 算法 {{ items[0]?.algorithm_version ?? '未提供' }}
            </span>
            <Button v-if="nextCursor" variant="outline" size="sm" :disabled="loading" @click="query(true)">
              加载更多
            </Button>
          </div>
        </template>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle class="text-base">网卡流量（独立计量）</CardTitle>
        </CardHeader>
        <CardContent v-if="!nicCounters.length" class="text-sm text-muted-foreground">
          服务端未返回网卡计数。
        </CardContent>
        <CardContent v-else class="px-0 pb-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>服务器</TableHead>
                <TableHead class="text-right">接收</TableHead>
                <TableHead class="text-right">发送</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="(counter, index) in nicCounters" :key="`${counter.server_id}-${index}`">
                <TableCell class="font-mono">{{ counter.server_id ?? '—' }}</TableCell>
                <TableCell class="text-right tabular">{{ formatBytes(counter.rx_bytes) }}</TableCell>
                <TableCell class="text-right tabular">{{ formatBytes(counter.tx_bytes) }}</TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </CardContent>
        <p class="px-6 py-3 text-xs text-muted-foreground">
          网卡流量含协议开销与其它进程流量，不参与成员配额判定。
        </p>
      </Card>

      <Collapsible v-if="items.length">
        <CollapsibleTrigger as-child>
          <Button variant="ghost" size="sm" class="text-muted-foreground">查看服务端原始字段</Button>
        </CollapsibleTrigger>
        <CollapsibleContent>
          <pre class="mt-2 max-h-96 overflow-auto rounded-md border border-border bg-muted p-3 font-mono text-xs whitespace-pre-wrap break-all">{{ prettyJson(items) }}</pre>
        </CollapsibleContent>
      </Collapsible>
    </div>
  </div>
</template>
