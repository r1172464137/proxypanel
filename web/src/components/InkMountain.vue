<script setup lang="ts">
/**
 * 极淡水墨山形：仅作页头/登录页的小范围装饰。
 * 纯描边 + 极低透明度填充，aria-hidden，pointer-events: none，不承载任何信息。
 */
withDefaults(defineProps<{ width?: number }>(), { width: 260 })
</script>

<template>
  <svg
    class="ink-mountain"
    :width="width"
    viewBox="0 0 260 96"
    fill="none"
    aria-hidden="true"
    focusable="false"
  >
    <path
      d="M2 72c20-14 33-22 48-18 13 3 20 12 33 10 16-2 22-22 40-22 19 0 25 17 40 21 17 4 26-2 45-14"
      stroke="currentColor"
      stroke-width="1.1"
      stroke-linecap="round"
      opacity="0.42"
    />
    <path
      d="M2 82c24-12 43-16 61-12 20 4 28 13 46 10 19-3 25-16 41-14 17 2 32 12 58 6"
      stroke="currentColor"
      stroke-width="1"
      stroke-linecap="round"
      opacity="0.26"
    />
    <path
      d="M2 90c30-8 52-9 74-6 24 3 36 6 58 3 22-3 42-8 62-5"
      stroke="currentColor"
      stroke-width="1"
      stroke-linecap="round"
      opacity="0.16"
    />
    <path
      d="M2 96c40-6 70-8 96-4 26 4 44 6 72 2 24-3 44-4 62-2"
      fill="currentColor"
      opacity="0.045"
    />
  </svg>
</template>

<style scoped>
.ink-mountain {
  display: block;
  height: auto;
  max-width: 100%;
  color: var(--text);
  pointer-events: none;
}
</style>
