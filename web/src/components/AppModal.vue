<script setup lang="ts">
import AppIcon from './AppIcon.vue'
import { onBeforeUnmount, watch } from 'vue'

const props = withDefaults(defineProps<{ open: boolean; title: string; size?: 'sm' | 'md' | 'lg' }>(), {
  size: 'md',
})
const emit = defineEmits<{ close: [] }>()

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') emit('close')
}

function cleanup(): void {
  document.removeEventListener('keydown', onKeydown)
  document.body.style.overflow = ''
}

watch(
  () => props.open,
  (open) => {
    if (open) {
      document.addEventListener('keydown', onKeydown)
      document.body.style.overflow = 'hidden'
    } else {
      cleanup()
    }
  },
  { immediate: true },
)

onBeforeUnmount(cleanup)
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-backdrop" @click.self="emit('close')">
      <div class="modal" :class="`modal--${size}`" role="dialog" aria-modal="true" :aria-label="title">
        <header class="modal__header">
          <h2>{{ title }}</h2>
          <button type="button" class="btn btn--ghost btn--sm" aria-label="关闭" @click="emit('close')">
            <AppIcon name="close" :size="15" />
          </button>
        </header>
        <div class="modal__body">
          <slot />
        </div>
        <footer v-if="$slots.footer" class="modal__footer">
          <slot name="footer" />
        </footer>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.modal-backdrop {
  position: fixed;
  inset: 0;
  z-index: 50;
  background: var(--overlay);
  backdrop-filter: blur(2px);
  animation: fade-in var(--dur) var(--ease-out);
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding: var(--space-6) var(--space-4);
  overflow-y: auto;
}

.modal {
  background: var(--card);
  color: var(--card-foreground);
  border: 2px solid var(--card-border);
  border-radius: 0;
  box-shadow: var(--shadow-pop);
  width: 100%;
  max-width: 640px;
  margin: auto 0;
  animation: enter var(--dur-slow) var(--ease-spring) both;
}

.modal--sm { max-width: 440px; }
.modal--lg { max-width: 900px; }

.modal__header {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-3) var(--space-5);
  border-bottom: 1px solid var(--border);
}

.modal__header h2 {
  font-family: var(--font-sans);
  font-size: var(--text-lg);
  font-weight: 600;
  letter-spacing: -0.01em;
  flex: 1;
  min-width: 0;
}

.modal__body {
  padding: var(--space-5);
  max-height: min(70vh, 720px);
  overflow-y: auto;
}

.modal__footer {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-2);
  padding: var(--space-4) var(--space-5);
  border-top: 1px solid var(--border);
  background: var(--muted);
  border-radius: 0;
  flex-wrap: wrap;
}
</style>