<script setup lang="ts">
import { computed } from 'vue'
type SetupStep = { key: string; state: string; reason?: string }
const props = defineProps<{ setup?: { steps: SetupStep[]; completed_count: number; blocking_reason?: string } }>()
const steps = [
  { key: 'servers', title: '接入服务器', to: '/admin/servers', detail: '登记机器，生成注册码，再核对管理程序心跳。' },
  { key: 'nodes', title: '创建节点', to: '/admin/nodes/new', detail: '配置连接入口，保存后核对同步任务。' },
  { key: 'node_groups', title: '组织节点组', to: '/admin/node-groups', detail: '将交付的节点加入组，供服务方案引用。' },
  { key: 'subscriptions', title: '建立服务方案', to: '/admin/subscriptions/new', detail: '选择节点组、每人额度、周期与有效期。' },
  { key: 'members', title: '邀请或开通成员', to: '/admin/invitations', detail: '选择包含的服务方案；仅建账号的邀请不会开通服务。' },
  { key: 'traffic_observed', title: '核对连接与用量', to: '/admin/usage', detail: '请成员导入客户端并连接，再核对真实流量；观察到用量不等于所有客户端均已验证。' },
]
const completedStates = ['completed', 'complete', 'done']
const knownStates = [...completedStates, 'blocked', 'pending', 'not_started']
function stateOf(key: string): string | undefined {
  return props.setup?.steps.find(step => step.key === key)?.state
}
const states = computed(() => steps.map(step => stateOf(step.key)))
// A step the API did not report, or reported as unknown, is not pending and
// must never be counted as completed. Only completed_count is trusted for the
// confirmed total, and only when it is a legal aggregate for the six steps.
const unknownCount = computed(() => states.value.filter(state => !state || state === 'unknown' || !knownStates.includes(state)).length)
const blockedCount = computed(() => states.value.filter(state => state === 'blocked').length)
const countKnown = computed(() => !!props.setup && Number.isInteger(props.setup.completed_count) && props.setup.completed_count >= 0 && props.setup.completed_count <= 6)
const blocked = computed(() => !!props.setup?.blocking_reason || blockedCount.value > 0)
const complete = computed(() => countKnown.value && !blocked.value && unknownCount.value === 0 && props.setup!.completed_count === 6 && steps.every(step => completedStates.includes(stateOf(step.key) ?? '')))
const progressText = computed(() => countKnown.value ? `已确认完成 ${props.setup!.completed_count}/6 步` : '完成数量未知：等待完整核对结果')
const unknownText = computed(() => `${unknownCount.value} 步状态未知，未计入已完成`)
function label(state?: string): string {
  return ({ completed: '已完成', complete: '已完成', done: '已完成', blocked: '被阻断', pending: '待完成', not_started: '未开始' } as Record<string, string>)[state ?? ''] ?? '状态未知'
}
</script>
<template>
  <section data-testid="workbench-setup-checklist" class="rounded-lg border p-5 space-y-4" aria-labelledby="setup-heading">
    <h2 id="setup-heading" class="text-lg font-semibold">服务交付路径</h2>
    <p data-testid="setup-progress-label" role="status">{{ progressText }}</p>
    <p v-if="setup && unknownCount" data-testid="setup-progress-unknown" role="status" class="text-sm text-muted-foreground">{{ unknownText }}</p>
    <p v-if="setup && blockedCount" data-testid="setup-progress-blocked" role="status" class="text-sm text-muted-foreground">设置被阻断：请先处理下方原因</p>
    <router-link to="/admin/setup" class="inline-flex min-h-11 items-center underline">{{ complete ? '设置已完成，查看或修改' : '继续完成设置' }}</router-link>
    <p v-if="!setup" role="status" class="text-sm text-muted-foreground">设置进度暂不可用。可按以下步骤继续，系统不会把未核对的步骤标为完成。</p>
    <p v-if="setup?.blocking_reason" role="alert">{{ setup.blocking_reason }}</p>
    <ol v-if="!complete" class="grid gap-4 md:grid-cols-2">
      <li v-for="(step, index) in steps" :key="step.key" :data-testid="`setup-step-${step.key}`" class="min-w-0 rounded border p-3 space-y-2">
        <div class="flex justify-between gap-2"><router-link :to="step.to" class="inline-flex min-h-11 items-center font-medium underline">{{ index + 1 }}. {{ step.title }}</router-link><span class="text-sm">{{ label(stateOf(step.key)) }}</span></div>
        <p class="text-sm text-muted-foreground">{{ setup?.steps.find(s => s.key === step.key)?.reason || step.detail }}</p>
      </li>
    </ol>
    <div class="flex flex-wrap gap-4 text-sm"><router-link class="underline" to="/admin/tasks">查看同步进度与失败重试</router-link><router-link class="underline" to="/admin/events">处理异常</router-link><router-link class="underline" to="/admin/batches">核对变更记录</router-link></div>
  </section>
</template>
