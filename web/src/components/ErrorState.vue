<script setup lang="ts">
import { computed } from 'vue'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { errorHint, errorText, errorTitle, errorCode, toApiError } from '@/lib/errors'
import AppIcon from './AppIcon.vue'

// Vue 会把未传入的 Boolean prop 转为 false；显式默认 true，避免“可重试但无按钮”。
const props = withDefaults(defineProps<{ error: unknown; retryLabel?: string; showRetry?: boolean }>(), {
  retryLabel: '重试',
  showRetry: true,
})
const emit = defineEmits<{ retry: [] }>()

const message = computed(() => errorText(props.error))
const title = computed(() => errorTitle(props.error))
const hint = computed(() => errorHint(props.error))
const code = computed(() => errorCode(props.error))
const requestId = computed(() => toApiError(props.error).requestId)
</script>

<template>
  <Alert variant="destructive" role="alert" class="flex flex-col gap-3 sm:flex-row sm:items-start">
    <AppIcon name="alert" :size="16" class="mt-0.5 hidden sm:block" />
    <div class="flex-1 space-y-1">
      <AlertTitle>{{ title }}</AlertTitle>
      <AlertDescription>
        <!-- 服务端 message 只作为纯文本渲染，绝不使用 v-html -->
        <p class="break-all">{{ message }}</p>
        <p v-if="hint" class="mt-1 text-muted-foreground">{{ hint }}</p>
        <p class="mt-1 text-xs text-muted-foreground">
          错误码：<span class="font-mono">{{ code }}</span>
          <template v-if="requestId"> · 请求编号：<span class="font-mono">{{ requestId }}</span></template>
        </p>
      </AlertDescription>
    </div>
    <Button
      v-if="showRetry"
      type="button"
      variant="outline"
      size="sm"
      class="shrink-0"
      @click="emit('retry')"
    >
      <AppIcon name="refresh" :size="14" aria-hidden="true" />
      <span>{{ retryLabel }}</span>
    </Button>
  </Alert>
</template>
