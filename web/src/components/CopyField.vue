<script setup lang="ts">
import AppIcon from './AppIcon.vue'
import { computed, ref } from 'vue'
import { copyText } from '@/lib/clipboard'
import { pushToast } from '@/lib/toast'

const props = withDefaults(
  defineProps<{
    value: string
    label?: string
    mask?: boolean
    warning?: string
    revealable?: boolean
  }>(),
  { label: '', mask: true, warning: '', revealable: true },
)

const revealed = ref(false)
const copied = ref(false)

const display = computed(() => {
  if (!props.mask || revealed.value) return props.value
  if (!props.value) return '—'
  if (props.value.length <= 16) return `${props.value.slice(0, 4)}…`
  return `${props.value.slice(0, 10)}…${props.value.slice(-4)}`
})

async function doCopy(): Promise<void> {
  const ok = await copyText(props.value)
  if (ok) {
    copied.value = true
    window.setTimeout(() => (copied.value = false), 2000)
    pushToast('success', '已复制到剪贴板', props.warning || undefined)
  } else {
    pushToast('error', '复制失败', '浏览器拒绝访问剪贴板，请手动选中文本复制')
  }
}
</script>

<template>
  <div class="copy-field">
    <div v-if="label || $slots.label" class="copy-field__label">
      <slot name="label">{{ label }}</slot>
    </div>
    <div class="copy-field__row">
      <code class="copy-field__value break-all">{{ display }}</code>
      <div class="copy-field__actions">
        <button
          v-if="revealable && mask"
          type="button"
          class="btn btn--sm"
          :aria-label="revealed ? '隐藏数值' : '显示数值'"
          @click="revealed = !revealed"
        >
          <AppIcon :name="revealed ? 'eyeOff' : 'eye'" :size="14" />
          <span>{{ revealed ? '隐藏' : '显示' }}</span>
        </button>
        <button type="button" class="btn btn--sm" :disabled="!value" @click="doCopy">
          <AppIcon :name="copied ? 'check' : 'copy'" :size="14" />
          <span>{{ copied ? '已复制' : '复制' }}</span>
        </button>
      </div>
    </div>
    <p v-if="warning" class="text-xs text-muted">{{ warning }}</p>
    <p v-else-if="mask" class="text-xs text-muted">默认脱敏显示；此值等同于凭据，请勿转发到不可信渠道。</p>
  </div>
</template>

<style scoped>
.copy-field {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}

.copy-field__label {
  font-size: var(--text-sm);
  font-weight: 500;
}

.copy-field__row {
  display: flex;
  gap: var(--space-2);
  align-items: flex-start;
  background: var(--muted);
  border: 1px solid var(--border);
  border-radius: 0;
  padding: var(--space-2) var(--space-3);
}

.copy-field__value {
  flex: 1;
  min-width: 0;
  padding-top: 4px;
}

.copy-field__actions {
  display: flex;
  gap: var(--space-1);
  flex-shrink: 0;
}
</style>