<script setup lang="ts">
import AppIcon from './AppIcon.vue'
import type { IconName } from '@/lib/icons'

withDefaults(defineProps<{ title: string; description?: string; icon?: IconName }>(), {
  description: '',
  icon: 'empty',
})
</script>

<template>
  <div class="state-block">
    <div class="state-block__icon" aria-hidden="true">
      <AppIcon :name="icon" :size="20" />
    </div>
    <h3>{{ title }}</h3>
    <p v-if="description">{{ description }}</p>
    <slot />
  </div>
</template>
