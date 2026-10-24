<script setup lang="ts">
import { useRecoveryMode } from '@/lib/recoveryMode'
const recovery = useRecoveryMode()
</script>
<template>
  <aside v-if="recovery.data.value?.recovery_mode" role="status" data-testid="recovery-mode-banner" class="m-4 rounded border border-amber-500 p-4 text-sm">
    恢复核对进行中：普通授权变更、注册、邀请与连接信息重置暂不可用；账号救援仍可用。
    <router-link to="/admin/runtime#recovery" class="underline">查看核对进度</router-link>
  </aside>
  <aside v-else-if="recovery.error.value" role="alert" class="m-4 rounded border p-3 text-sm">恢复状态暂时无法读取，写操作仍以服务端核对结果为准。<button type="button" class="min-h-11 underline" @click="recovery.reload()">重试读取</button></aside>
</template>
