<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import AppShell from '@/components/AppShell.vue'
import ToastHost from '@/components/ToastHost.vue'
import { ensureSession, sessionState } from '@/stores/session'

const route = useRoute()
const isPublic = computed(() => route.meta.public === true)

onMounted(() => {
  void ensureSession()
})
</script>

<template>
  <div v-if="sessionState.status === 'unknown' || sessionState.status === 'loading'" class="boot">
    <span class="spinner" aria-hidden="true" /> 正在校验会话…
  </div>
  <template v-else>
    <AppShell v-if="!isPublic">
      <router-view />
    </AppShell>
    <router-view v-else />
  </template>
  <ToastHost />
</template>

<style scoped>
.boot {
  min-height: 100vh;
  display: grid;
  place-items: center;
  gap: var(--space-3);
  color: var(--text-muted);
  font-size: var(--text-sm);
}
</style>