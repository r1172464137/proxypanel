<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { homeRoute, login } from '@/stores/session'
import { describeError } from '@/lib/errors'
import type { ApiError } from '@/api/http'
import { toApiError } from '@/lib/errors'
import AppIcon from '@/components/AppIcon.vue'

const route = useRoute()
const router = useRouter()

const username = ref('')
const password = ref('')
const submitting = ref(false)
const error = ref<ApiError | null>(null)

async function submit(): Promise<void> {
  if (submitting.value) return
  error.value = null
  submitting.value = true
  try {
    await login(username.value.trim(), password.value)
    const target = typeof route.query.next === 'string' && route.query.next.startsWith('/') ? route.query.next : homeRoute()
    await router.replace(target)
  } catch (caught) {
    error.value = toApiError(caught)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="auth-page">
    <div class="auth-page__grid texture-grid" aria-hidden="true" />

    <form class="auth-card card" @submit.prevent="submit">
      <div class="auth-brand">
        <span class="auth-brand__seal" aria-hidden="true">代</span>
        <div class="auth-brand__text">
          <h1>代理面板</h1>
          <p class="text-sm text-muted">私人合租 · 管理员与成员入口</p>
        </div>
      </div>

      <div v-if="error" class="banner banner--danger" role="alert">
        <span class="auth-banner__icon" aria-hidden="true">
          <AppIcon name="alert" :size="16" />
        </span>
        <div class="stack--tight">
          <strong>{{ describeError(error).title }}</strong>
          <p>{{ describeError(error).message }}</p>
          <p v-if="describeError(error).hint" class="text-xs text-muted">{{ describeError(error).hint }}</p>
        </div>
      </div>

      <div class="field">
        <label for="login-username">用户名</label>
        <input id="login-username" v-model="username" class="input" autocomplete="username" required />
      </div>
      <div class="field">
        <label for="login-password">密码</label>
        <input id="login-password" v-model="password" class="input" type="password" autocomplete="current-password" required />
      </div>

      <button type="submit" class="btn btn--primary btn--block" :disabled="submitting || !username || !password">
        <span v-if="submitting" class="spinner" aria-hidden="true" />
        <AppIcon v-else name="shield" :size="15" />
        {{ submitting ? '登录中…' : '登录' }}
      </button>

      <p class="text-xs text-muted">
        登录失败不会区分账号是否存在；连续失败会被限流。收到邀请码？
        <router-link to="/redeem">兑换邀请</router-link>
      </p>
    </form>
  </div>
</template>

<style scoped>
.auth-page {
  position: relative;
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: var(--space-5);
}

/* 网格纹理铺满登录页背景，低透明度、不拦截指针 */
.auth-page__grid {
  position: absolute;
  inset: 0;
  pointer-events: none;
  opacity: 0.6;
  mask-image: radial-gradient(70% 60% at 50% 35%, #000 20%, transparent 100%);
}

.auth-card {
  position: relative;
  z-index: 1;
  width: 100%;
  max-width: 388px;
  padding: var(--space-6);
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  box-shadow: var(--shadow-pop);
  animation: enter var(--dur-slow) var(--ease-spring) both;
}

.auth-brand {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding-bottom: var(--space-2);
}

/* 品牌标记：珊瑚实心方块，直角 + 硬边阴影 */
.auth-brand__seal {
  width: 34px;
  height: 34px;
  flex: none;
  display: grid;
  place-items: center;
  border: 1px solid color-mix(in srgb, var(--foreground) 30%, var(--primary));
  border-radius: 0;
  background: var(--primary);
  color: var(--primary-foreground);
  box-shadow: 2px 2px #0000002e, inset 0 0 0 1px #ffffff1f;
  font-family: var(--font-mono);
  font-size: 17px;
  font-weight: 700;
  line-height: 1;
  user-select: none;
}

.auth-brand__text h1 {
  font-family: var(--font-sans);
  font-size: 22px;
  font-weight: 600;
  letter-spacing: -0.02em;
}

.auth-brand__text p {
  margin-top: 2px;
}

.auth-banner__icon {
  color: var(--danger-ink);
  padding-top: 1px;
}
</style>
