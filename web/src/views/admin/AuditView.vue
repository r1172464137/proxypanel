<script setup lang="ts">
import { onMounted } from 'vue'
import { adminApi } from '@/api/endpoints'
import type { AuditRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { formatDateTime, prettyJson, utcLabel } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import DataTable from '@/components/DataTable.vue'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'

const list = usePagedList<AuditRecord>((cursor) => adminApi.auditLogs.list({ limit: 50, cursor }))

onMounted(() => void list.refresh())

const columns = [
  { label: '时间', width: 'w-44' },
  { label: '动作' },
  { label: '操作者' },
  { label: '对象 / 来源' },
  { label: '详情', width: 'w-32' },
]
</script>

<template>
  <div class="space-y-4">
    <PageHeader title="审计" description="管理操作与安全事件留痕；服务端脱敏，不记录密码、令牌或私钥。">
      <Button variant="outline" :disabled="list.loading.value" @click="list.refresh()">刷新</Button>
    </PageHeader>

    <DataTable
      :list="list"
      :columns="columns"
      empty-title="没有审计记录"
      empty-description="操作发生后会写入审计；此接口不返回秘密值。"
      loading-label="正在读取审计"
    >
      <template #row="{ item, columnIndex }">
        <template v-if="columnIndex === 0">
          <span class="whitespace-nowrap" :title="utcLabel(item.created_at)">{{ formatDateTime(item.created_at) }}</span>
        </template>
        <template v-else-if="columnIndex === 1">
          {{ item.action ?? '—' }}
        </template>
        <template v-else-if="columnIndex === 2">
          <template v-if="item.actor_username">{{ item.actor_username }}</template>
          <template v-else-if="item.actor_id">{{ item.actor_id }}</template>
          <span v-else class="text-muted-foreground">
            匿名<template v-if="item.action === 'auth.login'">（登录事件本身没有操作者）</template>
          </span>
        </template>
        <template v-else-if="columnIndex === 3">
          <div class="flex flex-col text-xs">
            <span>{{ item.target_type ?? '—' }}</span>
            <span class="font-mono text-muted-foreground">{{ item.target_id ?? '' }}</span>
            <span v-if="item.ip || item.request_id" class="text-muted-foreground">
              来源 {{ item.ip ?? '—' }} · {{ item.request_id ?? '—' }}
            </span>
          </div>
        </template>
        <template v-else-if="columnIndex === 4">
          <Collapsible>
            <CollapsibleTrigger as-child>
              <Button variant="ghost" size="sm" class="text-muted-foreground">展开</Button>
            </CollapsibleTrigger>
            <CollapsibleContent>
              <pre class="mt-2 max-h-80 min-w-60 overflow-auto rounded-md border border-border bg-muted p-3 font-mono text-xs whitespace-pre-wrap break-all">{{ prettyJson(item.detail ?? item.details ?? item) }}</pre>
            </CollapsibleContent>
          </Collapsible>
        </template>
      </template>
      <template #empty-action>
        <Button variant="outline" @click="list.refresh()">刷新</Button>
      </template>
    </DataTable>
  </div>
</template>
