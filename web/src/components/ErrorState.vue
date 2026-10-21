<script setup lang="ts">
import { computed } from 'vue'
import { describeError } from '@/lib/errors'
import AppIcon from './AppIcon.vue'

// Vue 会把未传入的 Boolean prop 转为 false；显式默认 true，避免“可重试但无按钮”。
const props = withDefaults(defineProps<{ error: unknown; retryLabel?: string; showRetry?: boolean }>(), {
  retryLabel: '重试',
  showRetry: true,
})
const emit = defineEmits<{ retry: [] }>()

const view = computed(() => describeError(props.error))
</script>

<template>
  <div class="banner banner--danger" role="alert">
    <span class="banner__icon" aria-hidden="true">
      <AppIcon name="alert" :size="16" />
    </span>
    <div class="stack--tight" style="flex: 1">
      <div class="banner__title">{{ view.title }}</div>
      <!-- 服务端 message 只作为纯文本渲染，绝不使用 v-html -->
      <p class="break-all">{{ view.message }}</p>
      <p v-if="view.hint" class="text-sm text-muted">{{ view.hint }}</p>
      <p class="text-xs text-faint">
        错误码：<span class="mono">{{ view.code }}</span>
        <template v-if="view.requestId"> · 请求编号：<span class="mono">{{ view.requestId }}</span></template>
      </p>
    </div>
    <button
      v-if="showRetry !== false && view.retryable"
      type="button"
      class="btn btn--sm"
      @click="emit('retry')"
    >
      <AppIcon name="refresh" :size="14" />
      <span>{{ retryLabel ?? '重试' }}</span>
    </button>
  </div>
</template>

<style scoped>
.banner__icon {
  color: var(--danger);
  padding-top: 1px;
}
</style>
