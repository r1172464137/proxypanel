<script setup lang="ts">
withDefaults(defineProps<{ rows?: number; label?: string }>(), { rows: 4, label: '加载中' })
</script>

<template>
  <div class="stack" role="status" :aria-label="label">
    <div v-for="row in rows" :key="row" class="skeleton" :style="{ height: '18px', width: `${92 - row * 7}%` }" />
    <span class="text-xs text-faint">{{ label }}…</span>
  </div>
</template>