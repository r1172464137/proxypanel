<script setup lang="ts">
import AppIcon from './AppIcon.vue'
import { Card } from '@/components/ui/card'
import type { IconName } from '@/lib/icons'

withDefaults(defineProps<{ title: string; description?: string; icon?: IconName }>(), {
  description: '',
  icon: 'empty',
})
</script>

<template>
  <Card class="flex flex-col items-center justify-center gap-3 px-6 py-10 text-center">
    <span class="flex size-11 items-center justify-center rounded-full bg-muted text-muted-foreground">
      <AppIcon :name="icon" :size="22" aria-hidden="true" />
    </span>
    <h3 class="text-base font-medium">{{ title }}</h3>
    <p v-if="description" class="max-w-prose text-sm text-muted-foreground">{{ description }}</p>
    <div v-if="$slots.default" class="mt-1 flex flex-wrap items-center justify-center gap-2">
      <slot />
    </div>
  </Card>
</template>
