<script setup lang="ts">
import { computed } from 'vue'
import { Button } from '@/components/ui/button'
import AppIcon from './AppIcon.vue'
import { toggleTheme, themeState } from '@/stores/theme'

/** 无 props：主题状态与切换动作都来自 stores/theme。 */
const isDark = computed(() => themeState.theme === 'dark')
const hint = computed(() => (isDark.value ? '切换到浅色主题' : '切换到深色主题'))
</script>

<template>
  <Button
    type="button"
    variant="ghost"
    size="sm"
    aria-label="切换主题"
    :title="hint"
    @click="toggleTheme"
  >
    <AppIcon :name="isDark ? 'sun' : 'moon'" :size="16" />
    <span class="hidden sm:inline">{{ isDark ? '深色' : '浅色' }}</span>
  </Button>
</template>
