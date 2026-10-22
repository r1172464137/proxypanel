<script setup lang="ts">
import { computed } from 'vue'
import { Skeleton } from '@/components/ui/skeleton'

const props = withDefaults(defineProps<{ rows?: number; label?: string }>(), {
  rows: 3,
  label: '加载中',
})

const count = computed(() => Math.max(1, Math.trunc(props.rows)))
</script>

<template>
  <div role="status" class="space-y-3">
    <span class="sr-only" aria-live="polite">{{ label }}</span>
    <Skeleton
      v-for="row in count"
      :key="row"
      class="h-4"
      :style="{ width: `${92 - row * 7}%` }"
    />
  </div>
</template>
