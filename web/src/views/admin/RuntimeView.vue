<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { adminApi } from '@/api/endpoints'
import { usePagedList } from '@/lib/paged'
import { useResource } from '@/lib/async'
import { useRecoveryMode, mutateRecoveryMode } from '@/lib/recoveryMode'
import { errorText } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import { readinessCheckText } from '@/lib/readiness'
import PageHeader from '@/components/PageHeader.vue'
import DataTable from '@/components/DataTable.vue'
import ErrorState from '@/components/ErrorState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { Button } from '@/components/ui/button'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
const route = useRoute()
const router = useRouter()
const tab = computed({ get: () => route.hash === '#recovery' ? 'recovery' : route.hash === '#upgrade' ? 'upgrade' : selected.value, set: value => { selected.value = String(value); void router.replace({ hash: value === 'runtime' ? '' : `#${value}` }) } })
const selected = ref('runtime')
const agents = usePagedList(cursor => adminApi.agents.list({ limit: 50, cursor }))
const instances = usePagedList(cursor => adminApi.coreInstances.list({ limit: 50, cursor }))
const readiness = useResource(() => adminApi.upgradeReadiness.get())
const recovery = useRecoveryMode()
const recoveryUpdatedAt = computed(() => {
  const value = recovery.data.value as Record<string, unknown> | null
  return typeof value?.updated_at === 'string' ? value.updated_at : null
})
const checkedAt = ref<string | null>(null)
async function refresh() { await Promise.all([agents.refresh(), instances.refresh(), readiness.reload(), recovery.reload()]); checkedAt.value = new Date().toISOString() }
onMounted(refresh)
const checks = reactive({ agent_revisions_checked: false, ledger_checked: false })
const confirm = ref('')
const pending = ref(false)
const actionError = ref('')
const result = ref<unknown>(null)
async function changeRecovery(enter: boolean) {
  pending.value = true; actionError.value = ''; result.value = null
  try {
    result.value = await mutateRecoveryMode(() => enter ? adminApi.recoveryMode.enter() : adminApi.recoveryMode.exit({ ...checks }))
    checks.agent_revisions_checked = false; checks.ledger_checked = false; confirm.value = ''
    await Promise.all([agents.refresh(), instances.refresh(), readiness.reload()])
  } catch (error) { actionError.value = errorText(error) }
  finally { pending.value = false }
}
const columns = [{ label: '管理程序 / 服务器' }, { label: '在线与心跳' }, { label: '应用 / 目标修订' }, { label: '版本 / 诊断' }]
const blockerLabels: Record<string, string> = {
  member_cycle_unproven: '成员周期无法与绑定历史和账本互相证明，请核对原始记录',
  ledger_rows_without_period: '用量账本缺少可用周期，只报告，不自动补算',
  member_period_overlap: '成员周期存在重叠，请核对历史区间',
  usage_batch_clock_skew: '用量批次时间偏差，请核对服务器时钟',
  batch_task_orphan: '变更任务缺少关联记录，请核对批次与任务',
  agent_state_unreadable: '无法读取管理程序状态，请重试核对',
  migration_checksum_mismatch: '迁移校验和不一致，请核对部署文件与数据库登记',
  pending_migration: '存在待应用迁移，请先备份并按部署说明处理',
  node_unattributable_pending: '待确认用量或回执无法证明节点归属，请先核对队列',
}
const instanceColumns = [{ label: '核心实例 / 节点' }, { label: '进程与配置回执' }, { label: '应用 / 目标修订' }, { label: '更新时间 / 错误' }]
function revision(applied: unknown, desired: unknown) {
  if (typeof applied !== 'number' || typeof desired !== 'number') return '修订状态未知'
  return `${applied} / ${desired} · ${applied === desired ? '配置已应用，连接未验证' : '节点尚未应用最新配置'}`
}
</script>
<template>
  <div class="space-y-6 min-w-0">
    <PageHeader title="运行与恢复核对" description="管理程序在线、核心进程、配置应用是不同事实；真实连接尚未验证。"><Button data-testid="runtime-refresh-button" @click="refresh">刷新运行状态</Button></PageHeader>
    <Tabs v-model="tab" class="space-y-4">
      <TabsList class="flex h-auto flex-wrap"><TabsTrigger value="runtime" class="min-h-11">运行状态</TabsTrigger><TabsTrigger value="recovery" class="min-h-11">恢复核对</TabsTrigger><TabsTrigger value="upgrade" class="min-h-11">升级与恢复</TabsTrigger></TabsList>
      <TabsContent value="runtime" class="space-y-4">
        <p class="text-sm">本次读取时间：{{ formatDateTime(checkedAt) }}。核心实例回执不代表端口可达，端口与客户端连接均未验证。</p>
        <DataTable data-testid="runtime-agent-table" :list="agents" :columns="columns" empty-title="暂无管理程序" empty-description="登记服务器并生成注册码后，等待管理程序接入。" loading-label="正在读取管理程序" :count-label="`本页 ${agents.items.value.length} 个，不代表全部服务器`">
          <template #row="{ item, columnIndex }">
            <span v-if="columnIndex === 0">{{ item.name || item.id || '未提供名称' }}<span class="block text-xs">{{ item.server_id || '服务器归属未知' }}</span></span>
            <span v-else-if="columnIndex === 1">{{ item.online === true ? '管理程序在线' : item.online === false && item.last_heartbeat_at ? '曾在线，现已离线' : item.online === false ? '尚无在线证据' : '在线状态未知' }}<span class="block">{{ formatDateTime(item.last_heartbeat_at) }}</span></span>
            <span v-else-if="columnIndex === 2">{{ revision(item.applied_revision, item.desired_revision) }}</span>
            <span v-else>{{ item.version || '版本未知' }} <router-link v-if="item.server_id" class="inline-flex min-h-11 items-center underline" :to="`/admin/servers/${encodeURIComponent(item.server_id)}/runtime`">查看服务器诊断</router-link></span>
          </template>
        </DataTable>
        <DataTable data-testid="runtime-instance-table" :list="instances" :columns="instanceColumns" empty-title="暂无核心实例" empty-description="配置节点并等待管理程序上报；无实例不表示服务正常。" loading-label="正在读取核心实例">
          <template #row="{ item, columnIndex }">
            <span v-if="columnIndex === 0">{{ item.node_name || item.node_id || item.id }}</span>
            <span v-else-if="columnIndex === 1">{{ item.state === 'rollback_applied' ? '正在使用上一份可运行配置（目标变更未完成）' : item.state === 'applied' ? '已收到应用回执（连接未验证）' : item.state || '进程状态未知' }}</span>
            <span v-else-if="columnIndex === 2">{{ revision(item.applied_revision, item.desired_revision) }}</span>
            <span v-else class="break-all">{{ formatDateTime(item.updated_at || item.applied_at) }} · {{ item.last_error || item.error || '未报告错误（不代表连接可用）' }}</span>
          </template>
        </DataTable>
      </TabsContent>
      <TabsContent id="recovery" value="recovery" class="rounded border p-4 space-y-4">
        <h2 class="font-semibold">恢复核对</h2>
        <ErrorState v-if="recovery.error.value" :error="recovery.error.value" @retry="recovery.reload()" />
        <p>{{ recovery.data.value ? recovery.data.value.recovery_mode ? '恢复核对进行中：普通授权变更、邀请、注册与连接信息重置暂不可用；账号救援仍可用。' : '未处于恢复核对模式（不代表节点均已恢复）' : '恢复状态未知，正在读取或等待重试。' }}</p>
        <p>更新时间：{{ formatDateTime(recoveryUpdatedAt) }}。从备份恢复后，须逐节点核对控制代际、应用修订与用量账本，避免旧授权重新放行。</p>
        <p v-if="actionError" role="alert">{{ actionError }}</p>
        <Button v-if="recovery.data.value?.recovery_mode === false" data-testid="recovery-enter-button" variant="destructive" :disabled="pending" @click="changeRecovery(true)">{{ pending ? '正在进入…' : '进入恢复核对并暂停普通变更' }}</Button>
        <form v-if="recovery.data.value?.recovery_mode" class="space-y-3" @submit.prevent="changeRecovery(false)">
          <label class="flex min-h-11 items-center gap-3"><input v-model="checks.agent_revisions_checked" data-testid="recovery-exit-checkbox-agent" type="checkbox">已核对各节点应用修订与已撤销权限</label>
          <label class="flex min-h-11 items-center gap-3"><input v-model="checks.ledger_checked" data-testid="recovery-exit-checkbox-ledger" type="checkbox">已核对用量账本与待补报队列</label>
          <label class="block" for="recovery-confirm">输入 verified 确认退出（退出不等于节点全部恢复）</label>
          <input id="recovery-confirm" v-model="confirm" data-testid="recovery-exit-confirm-input" class="min-h-11 max-w-full rounded border bg-background px-3" autocomplete="off">
          <Button type="submit" data-testid="recovery-exit-button" :disabled="pending || !checks.agent_revisions_checked || !checks.ledger_checked || confirm !== 'verified'">{{ pending ? '正在退出…' : '退出恢复核对' }}</Button>
        </form>
        <section v-if="result" data-testid="recovery-result-panel" role="status" class="space-y-2"><h3 class="font-semibold">操作结果：请继续核对节点，退出不等于全部恢复</h3><p>以下保留服务端返回的未核对管理程序（unverified_agents）与提示（advisories）。</p><pre class="overflow-auto whitespace-pre-wrap break-all text-xs">{{ result }}</pre></section>
      </TabsContent>
      <TabsContent id="upgrade" value="upgrade" class="rounded border p-4 space-y-3">
        <h2 class="font-semibold">升级预检</h2>
        <p>仅报告，系统不会自动修改历史数据。软件版本退回与数据库恢复是两项独立操作。</p>
        <p v-if="readiness.loading.value">正在读取升级预检…</p>
        <ErrorState v-else-if="readiness.error.value" :error="readiness.error.value" @retry="readiness.reload()" />
        <template v-else-if="readiness.data.value">
          <p role="status" data-testid="upgrade-readiness-status">{{ readiness.data.value.status === 'ready' ? '可升级（仍需备份并验证交付）' : readiness.data.value.status === 'blocked' ? '升级已阻断，先处理下列阻断项再升级' : '升级条件未知，不应继续升级' }}</p>
          <p>候选版本：{{ readiness.data.value.candidate_version || '未知' }} · 检查时间：{{ formatDateTime(readiness.data.value.checked_at) }}</p>
          <ul data-testid="upgrade-readiness-check-list" class="space-y-3"><li v-for="(check, index) in readiness.data.value.checks" :key="`${check.id}:${index}`" class="rounded border p-3"><StatusBadge :status="check.state" /> {{ readinessCheckText(check.id) }} · {{ check.severity === 'blocker' ? '阻断项' : '警告' }} · 影响 {{ check.affected_count }} 项<details><summary class="min-h-11 cursor-pointer">技术诊断</summary><pre class="overflow-auto whitespace-pre-wrap break-all text-xs">检查项：{{ check.id }}
服务端摘要：{{ check.summary }}
证据：{{ check.evidence }}</pre></details></li></ul>
          <p>迁移校验和：{{ readiness.data.value.schema.checksum_ok === true ? '已核验成功' : readiness.data.value.schema.checksum_ok === false ? '已知不一致' : '无法核验（不是成功，也不是已知不匹配）' }}</p>
          <p class="break-all">已应用迁移：{{ readiness.data.value.schema.applied_migrations.join('、') || '无' }}；待应用迁移：{{ readiness.data.value.schema.pending_migrations.join('、') || '无' }}</p>
          <ul data-testid="upgrade-blocker-list"><li v-for="code in readiness.data.value.blockers" :key="code" class="break-all">{{ blockerLabels[code] || '未知检查项，请查看诊断详情' }}（{{ code }}）</li></ul>
        </template>
      </TabsContent>
    </Tabs>
  </div>
</template>
