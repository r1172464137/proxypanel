<script setup lang="ts">
import { onMounted } from 'vue'
import { api } from '@/api/http'
import { adminApi } from '@/api/endpoints'
import type { UpgradeReadiness } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { useResource } from '@/lib/async'
import { formatDateTime } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import DataTable from '@/components/DataTable.vue'
import ErrorState from '@/components/ErrorState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { Button } from '@/components/ui/button'
const base = (import.meta.env.VITE_API_BASE ?? '').replace(/\/+$/, '')
const agents = usePagedList(cursor => adminApi.agents.list({ limit: 50, cursor }))
const readiness = useResource(() => api.get<UpgradeReadiness>(`${base}/api/v1/admin/upgrade-readiness`))
const recovery = useResource(() => adminApi.recoveryMode.get())
function refresh() { void agents.refresh(); void readiness.reload(); void recovery.reload() }
onMounted(refresh)
const columns = [{ label: '管理程序 / 服务器' }, { label: '连接状态' }, { label: '最近心跳' }, { label: '配置同步' }, { label: '处理' }]
</script>
<template>
  <div class="space-y-6">
    <PageHeader title="运行与恢复核对" description="分别核对管理程序在线、配置同步和真实连接。配置已应用不代表客户端已连通。"><Button @click="refresh">刷新</Button></PageHeader>
    <ErrorState v-if="recovery.error.value" :error="recovery.error.value" @retry="recovery.reload()" />
    <section class="rounded border p-4 space-y-2" aria-label="恢复核对">
      <h2 class="font-semibold">恢复核对</h2>
      <p>{{ recovery.data.value ? recovery.data.value.recovery_mode ? '恢复核对进行中：普通授权变更、邀请、注册与连接信息重置暂不可用；账号救援仍可用。' : '未处于恢复核对模式' : '恢复状态未知，正在读取或等待重试。' }}</p>
      <router-link class="underline" to="/admin/settings">进入维护与恢复操作（须核对节点和账本）</router-link>
    </section>
    <DataTable :list="agents" :columns="columns" empty-title="暂无管理程序" empty-description="先接入服务器，再生成注册码。" loading-label="正在读取管理程序" :count-label="`本页 ${agents.items.value.length} 个，不代表全部服务器`">
      <template #row="{ item, columnIndex }">
        <span v-if="columnIndex === 0">{{ item.name || item.id || '未提供名称' }}<span class="block text-xs">{{ item.server_id || '服务器归属未知' }}</span></span>
        <span v-else-if="columnIndex === 1">{{ item.online === true ? '在线' : item.online === false ? '离线' : '未知' }}</span>
        <span v-else-if="columnIndex === 2">{{ formatDateTime(item.last_heartbeat_at) }}</span>
        <span v-else-if="columnIndex === 3">{{ typeof item.applied_revision === 'number' && typeof item.desired_revision === 'number' ? item.applied_revision === item.desired_revision ? '配置已应用 · 真实连接未验证' : `待同步 ${item.applied_revision} → ${item.desired_revision}` : '同步状态未知' }}</span>
        <router-link v-else-if="item.server_id" class="underline" :to="`/admin/servers/${encodeURIComponent(item.server_id)}/runtime`">查看实例与诊断</router-link>
        <span v-else>请核对归属</span>
      </template>
    </DataTable>
    <section class="rounded border p-4 space-y-3" aria-labelledby="upgrade-title">
      <h2 id="upgrade-title" class="font-semibold">升级预检</h2>
      <p>仅报告核对结果，不自动补算历史或改变授权。软件版本退回与数据库恢复是两项独立操作。</p>
      <p v-if="readiness.loading.value">正在读取升级预检…</p>
      <ErrorState v-else-if="readiness.error.value" :error="readiness.error.value" @retry="readiness.reload()" />
      <template v-else-if="readiness.data.value">
        <p role="status">{{ readiness.data.value.status === 'ready' ? '预检通过（仍需按部署说明备份并验证交付）' : readiness.data.value.status === 'blocked' ? '升级已阻断，请先处理以下问题' : '升级条件未知，不应继续升级' }}</p>
        <p>候选版本：{{ readiness.data.value.candidate_version || '未知' }} · 检查时间：{{ formatDateTime(readiness.data.value.checked_at) }}</p>
        <ul class="space-y-3"><li v-for="(check, index) in readiness.data.value.checks" :key="`${check.id}:${index}`" class="rounded border p-3"><StatusBadge :status="check.state" /> {{ check.summary }} · {{ check.severity === 'blocker' ? '阻断项' : '警告' }} · 影响 {{ check.affected_count }} 项<details><summary>技术诊断</summary><pre class="overflow-auto whitespace-pre-wrap break-all text-xs">{{ check.evidence }}</pre></details></li></ul>
        <p>迁移校验和：{{ readiness.data.value.schema.checksum_ok === true ? '已核对' : readiness.data.value.schema.checksum_ok === false ? '不一致' : '未知，未完成核对' }}</p>
        <details><summary>迁移与阻断码</summary><pre class="overflow-auto whitespace-pre-wrap break-all text-xs">{{ readiness.data.value.schema }}
{{ readiness.data.value.blockers }}</pre></details>
      </template>
    </section>
  </div>
</template>
