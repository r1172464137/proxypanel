<script setup lang="ts">
import { computed } from 'vue'
import { ICON_PATHS, type IconName } from '@/lib/icons'

/**
 * 唯一图标出口：24×24 viewBox、1.6 描边、圆头圆角、currentColor。
 * 纯装饰时对辅助技术隐藏；需要语义时传 label（例如仅图标按钮），
 * 但按钮本身仍应保留 aria-label 或可见文字。
 */
const props = withDefaults(
  defineProps<{
    name: IconName
    size?: number
    strokeWidth?: number
    label?: string
  }>(),
  { size: 18, strokeWidth: 1.6, label: '' },
)

const paths = computed(() => ICON_PATHS[props.name] as readonly string[])
</script>

<template>
  <svg
    class="app-icon"
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    :stroke-width="strokeWidth"
    stroke-linecap="round"
    stroke-linejoin="round"
    :role="label ? 'img' : undefined"
    :aria-label="label || undefined"
    :aria-hidden="label ? undefined : 'true'"
    focusable="false"
  >
    <path v-for="(d, index) in paths" :key="index" :d="d" />
  </svg>
</template>

<style scoped>
.app-icon {
  flex: none;
  display: inline-block;
  vertical-align: -0.18em;
}
</style>
