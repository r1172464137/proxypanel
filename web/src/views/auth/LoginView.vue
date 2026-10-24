<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Loader2, ShieldCheck } from 'lucide-vue-next'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { homeRoute, login } from '@/stores/session'
import { safeLoginNext } from '@/lib/clientImport'
import type { ApiError } from '@/api/http'
import { toApiError } from '@/lib/errors'

const route = useRoute()
const router = useRouter()

const username = ref('')
const password = ref('')
const submitting = ref(false)
const error = ref<ApiError | null>(null)
const feedback = computed(() => {
  if (error.value?.status === 429) return '尝试过于频繁，请稍等一段时间再登录。用户名已保留，无需刷新页面。'
  if (error.value?.status === 403) return '登录请求被安全策略拒绝。请从管理员提供的面板地址重新打开；仍失败请联系管理员核对访问来源。'
  if (error.value?.status === 0) return '无法连接面板，请检查网络后重试。输入内容已保留。'
  if (error.value?.status === 401) return '登录未成功，请核对用户名与密码；仍无法登录请联系管理员。'
  return '暂时无法登录，请稍后重试或联系管理员。'
})

async function submit(): Promise<void> {
  if (submitting.value) return
  error.value = null
  submitting.value = true
  try {
    await login(username.value.trim(), password.value)
    const target = safeLoginNext(route.query.next, homeRoute())
    await router.replace(target)
  } catch (caught) {
    error.value = toApiError(caught)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="flex min-h-svh items-center justify-center bg-muted/40 px-4 py-10">
    <Card class="w-full max-w-sm p-6">
      <div class="flex items-center gap-3">
        <span
          class="grid size-10 shrink-0 place-items-center rounded-md bg-primary font-mono text-lg font-bold text-primary-foreground select-none"
          aria-hidden="true"
        >
          代
        </span>
        <div class="min-w-0">
          <h1 class="text-xl font-semibold tracking-tight">代理面板</h1>
          <p class="text-sm text-muted-foreground">私人合租 · 管理员与成员入口</p>
        </div>
      </div>

      <Alert v-if="error" variant="destructive" class="mt-5">
        <AlertTitle>登录未成功</AlertTitle>
        <AlertDescription><p data-testid="login-error-message" class="break-words">{{ feedback }}</p></AlertDescription>
      </Alert>

      <form data-testid="login-form" class="mt-5 flex flex-col gap-4" @submit.prevent="submit">
        <div class="grid gap-2">
          <Label for="login-username">用户名</Label>
          <Input id="login-username" v-model="username" autocomplete="username" required />
        </div>

        <div class="grid gap-2">
          <Label for="login-password">密码</Label>
          <Input
            id="login-password"
            v-model="password"
            type="password"
            autocomplete="current-password"
            required
          />
        </div>

        <Button type="submit" class="min-h-11 w-full" :disabled="submitting || !username || !password">
          <Loader2 v-if="submitting" class="animate-spin" aria-hidden="true" />
          <ShieldCheck v-else aria-hidden="true" />
          {{ submitting ? '登录中…' : '登录' }}
        </Button>
      </form>

      <p class="mt-4 text-xs leading-relaxed text-muted-foreground">
        登录失败不会区分账号是否存在；连续失败会被限流。收到邀请码？
        <router-link
          to="/redeem"
          class="font-medium text-foreground underline underline-offset-4 hover:text-primary"
        >
          兑换邀请
        </router-link>
      </p>
    </Card>
  </div>
</template>
