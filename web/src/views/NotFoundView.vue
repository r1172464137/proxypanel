<script setup lang="ts">
// 404 / 未开放兜底页。
//
// 显示用户实际请求的路径：从前端入口点进来（例如某个按钮指向尚未实现的路径）时，
// 只说"页面不存在"会让用户以为是页面坏了。把路径摆出来，并区分"地址有误"与
// "功能未开放"，用户才能判断是自己点错了还是这个功能还没有。
//
// "返回首页"直接给出目标路由而不是 "/"："/" 会再重定向一次，观感像"点了没反应"。
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import { sessionState } from '@/stores/session'

const route = useRoute()
const router = useRouter()

const requestedPath = computed(() => route.fullPath)

// /admin 与 /me 下的未知路径几乎都是"入口指向了尚未实现的功能"，
// 其它路径则是拼写或过期链接。两种情况的文案应当不同。
const looksLikeFeatureGap = computed(
  () => requestedPath.value.startsWith('/admin') || requestedPath.value.startsWith('/me'),
)

const heading = computed(() =>
  looksLikeFeatureGap.value ? '该功能可能尚未开放' : '该地址不存在',
)

const explanation = computed(() =>
  looksLikeFeatureGap.value
    ? '这个入口指向的页面还没有实现，或者已经被重命名。它和"页面坏了"不是一回事。'
    : '地址可能拼写有误、链接已被截断，或者页面已经改名。如果这是从某个功能入口点进来的，那说明该功能可能尚未开放。',
)

// vue-router 在直接打开链接时会把 history.state.back 置空，
// 这时"返回上一页"会把用户带出面板，所以退化成回首页。
const homeTarget = computed(() => {
  const user = sessionState.user
  if (!user) return '/login'
  return user.role === 'admin' ? '/admin/overview' : '/me/subscription'
})

const hasSameSiteHistory = computed(() => {
  const back = window.history.state?.back
  return typeof back === 'string' && back.length > 0
})

function goBack() {
  if (hasSameSiteHistory.value) {
    router.back()
    return
  }
  router.push(homeTarget.value)
}
</script>

<template>
  <div class="state-block">
    <div class="state-block__icon" aria-hidden="true">
      <AppIcon :name="looksLikeFeatureGap ? 'info' : 'search'" :size="20" />
    </div>
    <h3>{{ heading }}</h3>
    <p>{{ explanation }}</p>
    <p class="state-block__path">
      <span class="cell-sub">请求的地址</span>
      <code class="mono">{{ requestedPath }}</code>
    </p>
    <div class="state-block__actions">
      <button type="button" class="btn" @click="goBack">返回上一页</button>
      <router-link class="btn" :to="homeTarget">返回首页</router-link>
    </div>
  </div>
</template>
