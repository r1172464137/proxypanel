<script setup lang="ts">
defineProps<{ setup?: { steps: { key: string; state: string; reason?: string }[]; completed_count: number; blocking_reason?: string } }>()
const steps = [
  { key: 'servers', title: '接入服务器', to: '/admin/servers', detail: '登记机器，生成注册码，再核对管理程序心跳。' },
  { key: 'nodes', title: '创建节点', to: '/admin/nodes/new', detail: '配置连接入口，保存后核对同步任务。' },
  { key: 'node_groups', title: '组织节点组', to: '/admin/node-groups', detail: '将交付的节点加入组，供服务方案引用。' },
  { key: 'subscriptions', title: '建立服务方案', to: '/admin/subscriptions/new', detail: '选择节点组、每人额度、周期与有效期。' },
  { key: 'members', title: '邀请或开通成员', to: '/admin/invitations', detail: '选择包含的服务方案；仅建账号的邀请不会开通服务。' },
  { key: 'traffic_observed', title: '核对连接与用量', to: '/admin/usage', detail: '请成员导入客户端并连接，再核对真实流量；观察到用量不等于所有客户端均已验证。' },
]
function label(state?: string): string {
  return ({ completed: '已完成', complete: '已完成', done: '已完成', blocked: '被阻断', pending: '待完成', not_started: '未开始' } as Record<string, string>)[state ?? ''] ?? '状态未知'
}
</script>
<template>
  <section class="rounded-lg border p-5 space-y-4" aria-labelledby="setup-heading">
    <h2 id="setup-heading" class="text-lg font-semibold">服务交付路径</h2>
    <p v-if="!setup" role="status" class="text-sm text-muted-foreground">设置进度暂不可用。可按以下步骤继续，系统不会把未核对的步骤标为完成。</p>
    <p v-if="setup?.blocking_reason" role="alert">{{ setup.blocking_reason }}</p>
    <ol class="grid gap-4 md:grid-cols-2">
      <li v-for="(step, index) in steps" :key="step.key" class="min-w-0 rounded border p-3 space-y-2">
        <div class="flex justify-between gap-2"><router-link :to="step.to" class="font-medium underline">{{ index + 1 }}. {{ step.title }}</router-link><span class="text-sm">{{ label(setup?.steps.find(s => s.key === step.key)?.state) }}</span></div>
        <p class="text-sm text-muted-foreground">{{ setup?.steps.find(s => s.key === step.key)?.reason || step.detail }}</p>
      </li>
    </ol>
    <div class="flex flex-wrap gap-4 text-sm"><router-link class="underline" to="/admin/tasks">查看同步进度与失败重试</router-link><router-link class="underline" to="/admin/events">处理异常</router-link><router-link class="underline" to="/admin/batches">核对变更记录</router-link></div>
  </section>
</template>
