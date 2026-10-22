<script setup lang="ts">
import AppIcon from './AppIcon.vue'
import { dismissToast, toasts } from '@/lib/toast'
</script>

<template>
  <div class="toast-area" aria-live="polite">
    <TransitionGroup name="toast">
      <div v-for="item in toasts" :key="item.id" class="toast" :class="`toast--${item.tone}`">
        <div class="stack--tight" style="flex: 1">
          <strong>{{ item.title }}</strong>
          <span v-if="item.detail" class="text-sm">{{ item.detail }}</span>
        </div>
        <button type="button" class="btn btn--ghost btn--icon" aria-label="关闭提示" @click="dismissToast(item.id)">
          <AppIcon name="close" :size="13" />
        </button>
      </div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
.toast-area {
  position: fixed;
  right: var(--space-4);
  bottom: var(--space-4);
  z-index: 60;
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  max-width: min(420px, calc(100vw - 32px));
}

.toast {
  display: flex;
  gap: var(--space-3);
  align-items: flex-start;
  padding: var(--space-3) var(--space-4);
  border-radius: 0;
  background: var(--card);
  color: var(--card-foreground);
  border: 1px solid var(--border);
  border-left: 3px solid var(--text-faint);
  box-shadow: var(--shadow-pop);
}

.toast--success { border-left-color: var(--ok); }
.toast--error { border-left-color: var(--danger); }
.toast--info { border-left-color: var(--info); }

/* 进场：从下方弹起；退场：向上淡出（reduced-motion 时由 base.css 关闭） */
.toast-enter-active {
  animation: slideUp var(--dur) var(--ease-spring) both;
}

.toast-leave-active {
  animation: exit var(--dur-fast) var(--ease-out) both;
}

.toast-move {
  transition: transform var(--dur) var(--ease-out);
}
</style>
