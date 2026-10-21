<script setup lang="ts">
import AppIcon from '@/components/AppIcon.vue'
</script>

<template>
  <div class="state-block">
    <div class="state-block__icon" aria-hidden="true">
      <AppIcon name="search" :size="20" />
    </div>
    <h3>页面不存在</h3>
    <p>该地址没有对应页面，或链接已失效。</p>
    <router-link class="btn" to="/">返回首页</router-link>
  </div>
</template>
