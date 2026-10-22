<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { redeem } from '@/stores/session'
import { toApiError, describeError } from '@/lib/errors'
import type { ApiError } from '@/api/http'
import AppIcon from '@/components/AppIcon.vue'

const route = useRoute()
const router = useRouter()

const code = ref(typeof route.query.code === 'string' ? route.query.code : '')
const username = ref('')
const password = ref('')
const confirm = ref('')
const submitting = ref(false)
const error = ref<ApiError | null>(null)
const localError = ref('')

async function submit(): Promise<void> {
  localError.value = ''
  if (password.value.length < 10) {
    localError.value = '密码至少 10 位'
    return
  }
  if (password.value !== confirm.value) {
    localError.value = '两次输入的密码不一致'
    return
  }
  error.value = null
  submitting.value = true
  try {
    await redeem(code.value.trim(), username.value.trim(), password.value)
    await router.replace('/me/subscription?welcome=1')
  } catch (caught) {
    error.value = toApiError(caught)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="auth-page">

    <form class="auth-card card" @submit.prevent="submit">
      <div class="auth-card__head">
        <h1>兑换邀请</h1>
        <span class="auth-card__rule" aria-hidden="true" />
        <p class="text-sm text-muted">邀请码在服务端原子兑换，只能使用一次；兑换后自动登录，但代理权限需管理员绑定订阅后才生效。</p>
      </div>

      <div v-if="error" class="banner banner--danger" role="alert">
        <span class="auth-banner__icon" aria-hidden="true">
          <AppIcon name="alert" :size="16" />
        </span>
        <div class="stack--tight">
          <strong>{{ describeError(error).title }}</strong>
          <p>{{ describeError(error).message }}</p>
          <p class="text-xs">邀请不可用时不区分“已使用 / 已撤销 / 已过期”，避免探测。</p>
        </div>
      </div>
      <p v-if="localError" class="banner banner--warn" role="alert">{{ localError }}</p>

      <div class="field">
        <label for="redeem-code">邀请码</label>
        <input id="redeem-code" v-model="code" class="input mono" required />
      </div>
      <div class="field">
        <label for="redeem-username">用户名</label>
        <input id="redeem-username" v-model="username" class="input" autocomplete="username" required minlength="3" maxlength="64" />
        <p class="field__help">3–64 位字母、数字、点、下划线或连字符</p>
      </div>
      <div class="field">
        <label for="redeem-password">密码</label>
        <input id="redeem-password" v-model="password" class="input" type="password" autocomplete="new-password" required minlength="10" />
        <p class="field__help">至少 10 位</p>
      </div>
      <div class="field">
        <label for="redeem-confirm">确认密码</label>
        <input id="redeem-confirm" v-model="confirm" class="input" type="password" autocomplete="new-password" required />
      </div>

      <button type="submit" class="btn btn--primary btn--block" :disabled="submitting">
        <span v-if="submitting" class="spinner" aria-hidden="true" />
        <AppIcon v-else name="check" :size="15" />
        {{ submitting ? '兑换中…' : '兑换并登录' }}
      </button>
      <p class="text-xs text-muted">已有账号？<router-link to="/login">返回登录</router-link></p>
    </form>
  </div>
</template>

<style scoped>
.auth-page {
  position: relative;
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: var(--space-6) var(--space-5);
}

/* 与登录页一致的网格纹理背景，低透明度、不拦截指针 */

.auth-card {
  position: relative;
  z-index: 1;
  width: 100%;
  max-width: 440px;
  padding: var(--space-6);
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  box-shadow: var(--shadow-pop);
  animation: enter var(--dur-slow) var(--ease-spring) both;
}

.auth-card__head h1 {
  font-family: var(--font-sans);
  font-size: 22px;
  font-weight: 600;
  letter-spacing: -0.02em;
}

/* 珊瑚短块：直角、纯色，与 .page-header__rule 同一套语言 */
.auth-card__rule {
  display: block;
  width: 24px;
  height: 3px;
  margin: var(--space-2) 0;
  border-radius: 0;
  background: var(--primary);
}

.auth-card__head p {
  line-height: 1.6;
}

.auth-banner__icon {
  color: var(--danger-ink);
  padding-top: 1px;
}
</style>
