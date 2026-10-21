<script setup lang="ts">
import { toggleTheme, themeState } from '@/stores/theme'
import AppIcon from './AppIcon.vue'
</script>

<template>
  <button
    type="button"
    class="btn btn--ghost btn--sm"
    :aria-label="themeState.theme === 'dark' ? '切换到浅色主题' : '切换到深色主题'"
    :title="themeState.theme === 'dark' ? '切换到浅色主题' : '切换到深色主题'"
    @click="toggleTheme"
  >
    <AppIcon :name="themeState.theme === 'dark' ? 'sun' : 'moon'" :size="15" />
    <span class="hide-sm">{{ themeState.theme === 'dark' ? '深色' : '浅色' }}</span>
  </button>
</template>
