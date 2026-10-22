<script setup lang="ts">
import { computed } from 'vue'
import { Toaster } from '@/components/ui/sonner'
import { themeState } from '@/stores/theme'

// 沿用应用已有的浅色/深色偏好，toast 与应用主题保持一致。
const theme = computed(() => themeState.theme)
</script>

<template>
  <Toaster position="bottom-right" :theme="theme" rich-colors close-button />
</template>
