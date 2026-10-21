<script setup lang="ts">
import AppModal from './AppModal.vue'

withDefaults(
  defineProps<{
    open: boolean
    title: string
    confirmLabel?: string
    danger?: boolean
    pending?: boolean
    errorText?: string
  }>(),
  { confirmLabel: '确认', danger: false, pending: false, errorText: '' },
)
const emit = defineEmits<{ confirm: []; cancel: [] }>()
</script>

<template>
  <AppModal :open="open" :title="title" size="sm" @close="emit('cancel')">
    <div class="stack">
      <slot />
      <p v-if="errorText" class="banner banner--danger text-sm">{{ errorText }}</p>
      <p class="text-xs text-faint">危险操作会写入审计记录；确认后立即生效。</p>
    </div>
    <template #footer>
      <button type="button" class="btn" :disabled="pending" @click="emit('cancel')">取消</button>
      <button
        type="button"
        class="btn"
        :class="danger ? 'btn--danger' : 'btn--primary'"
        :disabled="pending"
        @click="emit('confirm')"
      >
        <span v-if="pending" class="spinner" aria-hidden="true" />
        {{ pending ? '处理中…' : confirmLabel }}
      </button>
    </template>
  </AppModal>
</template>